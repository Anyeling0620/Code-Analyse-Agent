import { useCallback, useEffect, useRef, useState } from 'react';
import { InterruptModal } from './InterruptModal';
import { fetchJSON, updateQuotaFromHeaders } from './api/client';
import { consumeSSE } from './api/sse';
import { MessageList } from './chat/MessageList';
import {
  addToolCallWithSegment,
  appendErrorSegment,
  appendStreamingDeltaSegment,
  appendTextBlockSegment,
  applyToolResultWithSegment,
  finalizeSegments,
  finalizeToolEvents,
  hasMeaningfulAssistantState,
  hasToolName,
  messageRecordToChatMessage,
} from './chat/messageSegments';
import { Composer } from './composer/Composer';
import { initialProfile } from './constants/profile';
import { normalizeMarkdown } from './markdown/normalizeMarkdown';
import { ProfileModal } from './profile/ProfileModal';
import { SessionPanel } from './sessions/SessionPanel';
import type { ChatMessage, CostDailyTotal, PaginatedSessionList, PendingInterruptEvent, ProfileForm, QuotaToday, SessionDetail, SessionListItem, StreamPayload, TraceEvent } from './types/chat';

const SESSION_PAGE_LIMIT = 20;
const MESSAGE_PAGE_LIMIT = 50;

export default function App() {
  const [profile, setProfile] = useState<ProfileForm>(initialProfile);
  const [sessionId, setSessionId] = useState('');
  const [interruptEvent, setInterruptEvent] = useState<PendingInterruptEvent | null>(null);
  const [messages, setMessages] = useState<ChatMessage[]>([]);
  const [sessions, setSessions] = useState<SessionListItem[]>([]);
  const [isStreaming, setIsStreaming] = useState(false);
  const [health, setHealth] = useState<'checking' | 'online' | 'offline'>('checking');
  const [lastTraceId, setLastTraceId] = useState('');
  const [costDaily, setCostDaily] = useState<CostDailyTotal | null>(null);
  const [quota, setQuota] = useState<QuotaToday | null>(null);
  const [panelError, setPanelError] = useState('');
  const [sessionPage, setSessionPage] = useState(1);
  const [hasMoreSessions, setHasMoreSessions] = useState(false);
  const [isLoadingSessions, setIsLoadingSessions] = useState(false);
  const [isLoadingMoreSessions, setIsLoadingMoreSessions] = useState(false);
  const [messagePage, setMessagePage] = useState(1);
  const [hasMoreMessages, setHasMoreMessages] = useState(false);
  const [isLoadingOlderMessages, setIsLoadingOlderMessages] = useState(false);
  const [isComposerExpanded, setIsComposerExpanded] = useState(true);
  const [isProfileModalOpen, setIsProfileModalOpen] = useState(false);
  const controllerRef = useRef<AbortController | null>(null);
  const sessionIdRef = useRef('');
  const isStreamingRef = useRef(false);
  const bottomRef = useRef<HTMLDivElement | null>(null);
  const conversationScrollRef = useRef<HTMLDivElement | null>(null);
  const shouldStickToBottomRef = useRef(true);
  const isLoadingMoreSessionsRef = useRef(false);
  const isLoadingOlderMessagesRef = useRef(false);
  const assistantUpdateQueueRef = useRef<Map<string, Array<(item: ChatMessage) => ChatMessage>>>(new Map());
  const assistantUpdateTimerRef = useRef<number | null>(null);

  useEffect(() => {
    void checkHealth();
    void refreshMetrics();
    void refreshSessions();
  }, []);

  useEffect(() => {
    sessionIdRef.current = sessionId;
  }, [sessionId]);

  useEffect(() => {
    isStreamingRef.current = isStreaming;
  }, [isStreaming]);

  useEffect(() => {
    if (!shouldStickToBottomRef.current) {
      return;
    }
    const frame = window.requestAnimationFrame(() => {
      scrollConversationToBottom('auto');
      window.requestAnimationFrame(() => scrollConversationToBottom('auto'));
    });
    return () => window.cancelAnimationFrame(frame);
  }, [messages, isStreaming]);

  useEffect(() => {
    return () => {
      if (assistantUpdateTimerRef.current !== null) {
        window.clearTimeout(assistantUpdateTimerRef.current);
      }
    };
  }, []);

  async function checkHealth() {
    try {
      const response = await fetch('/healthz');
      setHealth(response.ok ? 'online' : 'offline');
    } catch {
      setHealth('offline');
    }
  }

  async function refreshMetrics() {
    try {
      const today = new Date().toISOString().slice(0, 10);
      const [daily, quotaResp] = await Promise.all([
        fetchJSON<CostDailyTotal>(`/api/cost/daily?date=${today}`),
        fetchJSON<QuotaToday>('/api/quota/today'),
      ]);
      setCostDaily(daily);
      setQuota(quotaResp);
      setPanelError('');
    } catch (error) {
      setPanelError(error instanceof Error ? error.message : '指标加载失败');
    }
  }

  async function refreshSessions() {
    setIsLoadingSessions(true);
    try {
      const page = await fetchJSON<PaginatedSessionList>(`/api/sessions?page=1&limit=${SESSION_PAGE_LIMIT}`);
      setSessions(page.list);
      setSessionPage(page.page);
      setHasMoreSessions(page.has_more);
      setPanelError('');
    } catch (error) {
      setPanelError(error instanceof Error ? error.message : '会话记录加载失败');
    } finally {
      setIsLoadingSessions(false);
    }
  }

  async function loadMoreSessions() {
    if (isLoadingSessions || isLoadingMoreSessionsRef.current || !hasMoreSessions) {
      return;
    }
    isLoadingMoreSessionsRef.current = true;
    setIsLoadingMoreSessions(true);
    try {
      const nextPage = sessionPage + 1;
      const page = await fetchJSON<PaginatedSessionList>(`/api/sessions?page=${nextPage}&limit=${SESSION_PAGE_LIMIT}`);
      setSessions((current) => mergeSessions(current, page.list));
      setSessionPage(page.page);
      setHasMoreSessions(page.has_more);
      setPanelError('');
    } catch (error) {
      setPanelError(error instanceof Error ? error.message : '更多会话加载失败');
    } finally {
      isLoadingMoreSessionsRef.current = false;
      setIsLoadingMoreSessions(false);
    }
  }

  async function loadSession(nextSessionId: string) {
    if (isStreaming) {
      return;
    }
    try {
      shouldStickToBottomRef.current = true;
      const detail = await fetchJSON<SessionDetail>(`/api/sessions/info?session_id=${encodeURIComponent(nextSessionId)}&page=1&limit=${MESSAGE_PAGE_LIMIT}`);
      setSessionId(detail.session.session_id);
      setMessages(detail.list.map(messageRecordToChatMessage));
      setMessagePage(detail.page);
      setHasMoreMessages(detail.has_more);
      setPanelError('');
      window.requestAnimationFrame(() => scrollConversationToBottom('auto'));
    } catch (error) {
      setPanelError(error instanceof Error ? error.message : '会话详情加载失败');
    }
  }

  async function loadOlderMessages() {
    if (isStreaming || isLoadingOlderMessagesRef.current || !hasMoreMessages || !sessionIdRef.current) {
      return;
    }
    const scrollElement = conversationScrollRef.current;
    const previousScrollHeight = scrollElement?.scrollHeight ?? 0;
    const previousScrollTop = scrollElement?.scrollTop ?? 0;
    shouldStickToBottomRef.current = false;
    isLoadingOlderMessagesRef.current = true;
    setIsLoadingOlderMessages(true);
    try {
      const nextPage = messagePage + 1;
      const detail = await fetchJSON<SessionDetail>(`/api/sessions/info?session_id=${encodeURIComponent(sessionIdRef.current)}&page=${nextPage}&limit=${MESSAGE_PAGE_LIMIT}`);
      setMessages((current) => mergeOlderMessages(current, detail.list.map(messageRecordToChatMessage)));
      setMessagePage(detail.page);
      setHasMoreMessages(detail.has_more);
      setPanelError('');
      window.requestAnimationFrame(() => {
        const nextScrollElement = conversationScrollRef.current;
        if (!nextScrollElement) {
          return;
        }
        nextScrollElement.scrollTop = previousScrollTop + nextScrollElement.scrollHeight - previousScrollHeight;
      });
    } catch (error) {
      setPanelError(error instanceof Error ? error.message : '更早消息加载失败');
    } finally {
      isLoadingOlderMessagesRef.current = false;
      setIsLoadingOlderMessages(false);
    }
  }

  function mergeSessions(current: SessionListItem[], nextItems: SessionListItem[]) {
    const seen = new Set(current.map((item) => item.session_id));
    const merged = [...current];
    for (const item of nextItems) {
      if (seen.has(item.session_id)) {
        continue;
      }
      seen.add(item.session_id);
      merged.push(item);
    }
    return merged;
  }

  function mergeOlderMessages(current: ChatMessage[], olderItems: ChatMessage[]) {
    const seen = new Set(current.map((item) => item.id));
    return [...olderItems.filter((item) => !seen.has(item.id)), ...current];
  }

  async function deleteSession(targetSessionId: string) {
    if (isStreaming) {
      return;
    }
    if (!window.confirm('确定删除这条会话记录吗？删除后不可恢复。')) {
      return;
    }

    try {
      await fetchJSON<null>(`/api/sessions/delete?session_id=${encodeURIComponent(targetSessionId)}`, { method: 'DELETE' });
      setSessions((items) => items.filter((item) => item.session_id !== targetSessionId));
      if (targetSessionId === sessionId) {
        setSessionId('');
        setMessages([]);
        setMessagePage(1);
        setHasMoreMessages(false);
        setLastTraceId('');
      }
      setPanelError('');
    } catch (error) {
      setPanelError(error instanceof Error ? error.message : '会话删除失败');
    }
  }

  function startNewSession() {
    if (isStreaming) {
      return;
    }
    setSessionId('');
    setMessages([]);
    setMessagePage(1);
    setHasMoreMessages(false);
    setLastTraceId('');
  }

  const handleSubmit = useCallback(async (messageText: string) => {
    const message = messageText.trim();
    if (!message || isStreamingRef.current) {
      return;
    }

    const userMessage: ChatMessage = {
      id: crypto.randomUUID(),
      role: 'user',
      content: message,
      tools: [],
      toolEvents: [],
      segments: [],
      traceEvents: [],
      status: 'done',
    };

    const assistantId = crypto.randomUUID();
    const assistantPlaceholder: ChatMessage = {
      id: assistantId,
      role: 'assistant',
      content: '',
      tools: [],
      toolEvents: [],
      segments: [],
      traceEvents: [],
      status: 'streaming',
    };

    shouldStickToBottomRef.current = true;
    setIsComposerExpanded(true);
    setMessages((current) => [...current, userMessage, assistantPlaceholder]);
    setIsStreaming(true);

    const controller = new AbortController();
    controllerRef.current = controller;
    let sawDone = false;

    try {
      const response = await fetch('/api/chat/stream', {
        method: 'POST',
        headers: {
          'Content-Type': 'application/json; charset=utf-8',
        },
        body: JSON.stringify({
          user_id: 'demo-user',
          session_id: sessionIdRef.current,
          message,
          profile,
        }),
        signal: controller.signal,
      });

      updateQuotaFromHeaders(response);
      if (!response.ok || !response.body) {
        const text = await response.text();
        throw new Error(text || `stream request failed: ${response.status}`);
      }

      await consumeSSE(response.body, (eventName, payload) => {
        if (eventName === 'done') {
          sawDone = true;
        }
        handleStreamEvent(assistantId, eventName, payload);
      });

      patchAssistant(assistantId, (item) => ({
        ...item,
        toolEvents: finalizeToolEvents(item.toolEvents),
        segments: finalizeSegments(item.segments),
        status: sawDone || hasMeaningfulAssistantState(item) ? 'done' : item.status,
      }));
    } catch (error) {
      const isAbort = error instanceof Error && error.name === 'AbortError';
      const messageText = isAbort ? '本轮已停止。' : error instanceof Error ? error.message : '请求失败，请检查后端是否已经启动。';

      patchAssistant(assistantId, (item) => {
        const meaningful = hasMeaningfulAssistantState(item);
        const doneLike = meaningful || isAbort;
        return {
          ...item,
          content: item.content || messageText,
          toolEvents: finalizeToolEvents(item.toolEvents),
          segments: finalizeSegments(item.segments),
          status: doneLike ? 'done' : 'error',
        };
      });
    } finally {
      controllerRef.current = null;
      setIsStreaming(false);
      void checkHealth();
      void refreshMetrics();
      void refreshSessions();
    }
  }, [profile]);

  function handleStreamEvent(assistantId: string, eventName: string, payload: StreamPayload) {
    switch (eventName) {
      case 'ready':
        updateTrace(payload);
        break;
      case 'session':
        if (payload.session_id) {
          setSessionId(payload.session_id);
        }
        updateTrace(payload);
        break;
      case 'observe':
        updateTrace(payload);
        appendTraceEvent(assistantId, payload);
        break;
      case 'heartbeat':
        updateTrace(payload);
        break;
      case 'tool_call':
        updateTrace(payload);
        if (!hasToolName(payload)) {
          break;
        }
        appendTraceEvent(assistantId, { ...payload, stage: `tool_call:${payload.tool_name}`, detail: payload.tool_arguments });
        patchAssistant(assistantId, (item) => addToolCallWithSegment(item, payload));
        break;
      case 'tool_result':
        updateTrace(payload);
        if (!hasToolName(payload)) {
          break;
        }
        appendTraceEvent(assistantId, { ...payload, stage: `tool_result:${payload.tool_name}`, detail: payload.tool_result });
        patchAssistant(assistantId, (item) => applyToolResultWithSegment(item, payload));
        break;
      case 'interrupt':
        updateTrace(payload);
        if (payload.pending_approval_id) {
          setInterruptEvent({
            pending_approval_id: payload.pending_approval_id,
            pending_command: payload.pending_command ?? '',
            pending_risk_reason: payload.pending_risk_reason,
            pending_risk_level: payload.pending_risk_level,
            pending_workdir: payload.pending_workdir,
            pending_timeout_sec: payload.pending_timeout_sec,
            session_id: payload.session_id,
            assistant_message_id: assistantId,
          });
        }
        break;
      case 'progress':
        updateTrace(payload);
        patchAssistant(assistantId, (item) => appendTextBlockSegment(item, payload.delta ?? payload.message ?? payload.detail ?? '', payload, false, 'progress'));
        break;
      case 'delta':
        updateTrace(payload);
        patchAssistant(assistantId, (item) => appendStreamingDeltaSegment(item, payload.delta ?? '', payload));
        break;
      case 'done':
        updateTrace(payload);
        if (payload.result?.session_id) {
          setSessionId(payload.result.session_id);
        }
        patchAssistant(assistantId, (item) => ({
          ...item,
          content: normalizeMarkdown(item.content),
          tools: payload.result?.used_tools ?? item.tools,
          toolEvents: finalizeToolEvents(item.toolEvents),
          segments: finalizeSegments(item.segments),
          status: 'done',
        }));
        break;
      case 'error':
        patchAssistant(assistantId, (item) => {
          const meaningful = hasMeaningfulAssistantState(item);
          const errorText = payload.message || '流式请求失败，请稍后再试。';
          const withError = appendErrorSegment(item, errorText);
          return {
            ...withError,
            content: meaningful ? withError.content : errorText,
            toolEvents: finalizeToolEvents(withError.toolEvents),
            segments: finalizeSegments(withError.segments),
            status: meaningful ? 'done' : 'error',
          };
        });
        break;
      default:
        break;
    }
  }

  async function resumeInterruptedRun(ev: PendingInterruptEvent, approved: boolean) {
    shouldStickToBottomRef.current = true;
    setIsStreaming(true);
    patchAssistant(ev.assistant_message_id, (item) => ({ ...item, status: 'streaming' }));
    try {
      const response = await fetch('/api/chat/resume', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json; charset=utf-8' },
        body: JSON.stringify({
          session_id: ev.session_id ?? sessionId,
          pending_id: ev.pending_approval_id,
          approved,
        }),
      });
      updateQuotaFromHeaders(response);
      if (!response.ok || !response.body) {
        const text = await response.text();
        throw new Error(text || `resume request failed: ${response.status}`);
      }
      await consumeSSE(response.body, (eventName, payload) => {
        handleStreamEvent(ev.assistant_message_id, eventName, payload);
      });
      patchAssistant(ev.assistant_message_id, (item) => ({
        ...item,
        toolEvents: finalizeToolEvents(item.toolEvents),
        segments: finalizeSegments(item.segments),
        status: hasMeaningfulAssistantState(item) ? 'done' : item.status,
      }));
    } catch (error) {
      const messageText = error instanceof Error ? error.message : '审批恢复请求失败，请稍后再试。';
      patchAssistant(ev.assistant_message_id, (item) => {
        const meaningful = hasMeaningfulAssistantState(item);
        return {
          ...item,
          content: meaningful ? item.content : messageText,
          toolEvents: finalizeToolEvents(item.toolEvents),
          segments: finalizeSegments(item.segments),
          status: meaningful ? 'done' : 'error',
        };
      });
    } finally {
      setIsStreaming(false);
      void checkHealth();
      void refreshMetrics();
      void refreshSessions();
    }
  }

  function updateTrace(payload: StreamPayload) {
    if (payload.trace_id) {
      setLastTraceId(payload.trace_id);
    }
  }

  function appendTraceEvent(messageId: string, payload: StreamPayload) {
    if (!payload.stage && !payload.detail) {
      return;
    }
    const traceEvent: TraceEvent = {
      id: crypto.randomUUID(),
      stage: payload.stage ?? payload.type,
      detail: payload.detail ?? '',
      elapsedMs: payload.elapsed_ms,
      timestamp: payload.timestamp,
    };
    patchAssistant(messageId, (item) => ({ ...item, traceEvents: [...item.traceEvents, traceEvent] }));
  }

  function patchAssistant(id: string, updater: (item: ChatMessage) => ChatMessage) {
    const queuedUpdates = assistantUpdateQueueRef.current.get(id);
    if (queuedUpdates) {
      queuedUpdates.push(updater);
    } else {
      assistantUpdateQueueRef.current.set(id, [updater]);
    }
    scheduleAssistantUpdateFlush();
  }

  function scheduleAssistantUpdateFlush() {
    if (assistantUpdateTimerRef.current !== null) {
      return;
    }
    assistantUpdateTimerRef.current = window.setTimeout(flushAssistantUpdates, 40);
  }

  function flushAssistantUpdates() {
    assistantUpdateTimerRef.current = null;
    const queuedUpdates = new Map(assistantUpdateQueueRef.current);
    assistantUpdateQueueRef.current.clear();
    if (queuedUpdates.size === 0) {
      return;
    }

    setMessages((current) =>
        current.map((item) => {
          const itemUpdates = queuedUpdates.get(item.id);
          if (!itemUpdates) {
            return item;
          }
          return itemUpdates.reduce((next, update) => update(next), item);
        }),
    );
  }

  function handleConversationScroll() {
    const scrollElement = conversationScrollRef.current;
    if (!scrollElement) {
      return;
    }
    const distanceToBottom = scrollElement.scrollHeight - scrollElement.scrollTop - scrollElement.clientHeight;
    const isAtBottom = distanceToBottom < 96;
    if (scrollElement.scrollTop < 96 && hasMoreMessages && !isLoadingOlderMessages && !isStreamingRef.current) {
      void loadOlderMessages();
    }
    shouldStickToBottomRef.current = isAtBottom;
    setIsComposerExpanded((current) => (current === isAtBottom ? current : isAtBottom));
  }

  const stopStreaming = useCallback(() => {
    controllerRef.current?.abort();
  }, []);

  function scrollConversationToBottom(behavior: ScrollBehavior) {
    const scrollElement = conversationScrollRef.current;
    if (!scrollElement) return;
    scrollElement.scrollTo({ top: scrollElement.scrollHeight, behavior });
  }

  const scrollToTop = useCallback(() => {
    const scrollElement = conversationScrollRef.current;
    if (!scrollElement) return;
    shouldStickToBottomRef.current = false;
    setIsComposerExpanded(false);
    scrollElement.scrollTo({ top: 0, behavior: 'smooth' });
  }, []);

  const scrollToBottom = useCallback(() => {
    shouldStickToBottomRef.current = true;
    setIsComposerExpanded(true);
    scrollConversationToBottom('smooth');
  }, []);

  return (
      <div className="app-shell">
        <aside className="control-panel">
          <SessionPanel
              health={health}
              panelError={panelError}
              sessions={sessions}
              sessionId={sessionId}
              isStreaming={isStreaming}
              isLoadingMoreSessions={isLoadingMoreSessions}
              hasMoreSessions={hasMoreSessions}
              onStartNewSession={startNewSession}
              onLoadSession={loadSession}
              onLoadMoreSessions={loadMoreSessions}
              onDeleteSession={deleteSession}
              onOpenProfile={() => setIsProfileModalOpen(true)}
          />
        </aside>

        <main className="chat-stage">
          <div ref={conversationScrollRef} className="conversation-scroll" onScroll={handleConversationScroll}>
            {(sessionId || lastTraceId) && (
                <section className="workspace-meta-bar">
                  {sessionId && <span className="workspace-meta-chip">Session: {sessionId}</span>}
                  {lastTraceId && <span className="workspace-meta-chip">Trace: {lastTraceId}</span>}
                </section>
            )}

            <MessageList messages={messages} bottomRef={bottomRef} isLoadingOlderMessages={isLoadingOlderMessages} hasMoreMessages={hasMoreMessages} />
          </div>

          <Composer
              isStreaming={isStreaming}
              isExpanded={isComposerExpanded}
              costDaily={costDaily}
              quota={quota}
              onSubmit={handleSubmit}
              onStop={stopStreaming}
              onScrollTop={scrollToTop}
              onScrollBottom={scrollToBottom}
          />
        </main>
        {isProfileModalOpen && (
            <ProfileModal
                profile={profile}
                onSave={(nextProfile) => {
                  setProfile(nextProfile);
                  setIsProfileModalOpen(false);
                }}
                onClose={() => setIsProfileModalOpen(false)}
            />
        )}
        {interruptEvent && (
            <InterruptModal
                event={interruptEvent}
                onResolve={async (approved) => {
                  const ev = interruptEvent;
                  setInterruptEvent(null);
                  if (!ev) return;
                  await resumeInterruptedRun(ev, approved);
                }}
            />
        )}
      </div>
  );
}
