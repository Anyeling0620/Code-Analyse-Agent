import { useCallback, useEffect, useLayoutEffect, useRef, useState } from 'react';
import { InterruptModal } from './InterruptModal';
import { fetchAuthorized, fetchJSON, updateQuotaFromHeaders } from './api/client';
import { consumeSSE } from './api/sse';
import { LoginModal } from './auth/LoginModal';
import { clearSession, getSession, saveSession, subscribeSession } from './auth/session';
import type { AuthSession } from './auth/session';
import { MessageList } from './chat/MessageList';
import { ChatSkeleton } from './chat/ChatSkeleton';
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
  resolveStreamEventType,
} from './chat/messageSegments';
import { Composer } from './composer/Composer';
import { initialProfile } from './constants/profile';
import { ExportPrintRoot } from './export/ExportPrintRoot';
import {
  buildEntryAt,
  buildExportFilename,
  buildMarkdownDocument,
  downloadTextFile,
  type ExportEntry,
  type ExportFormat,
} from './export/exportDocument';
import { normalizeMarkdown } from './markdown/normalizeMarkdown';
import { ElevatorNav, shouldShowElevatorNav } from './nav/ElevatorNav';
import { ProfileModal } from './profile/ProfileModal';
import { SessionPanel } from './sessions/SessionPanel';
import { createShareLink } from './share/api';
import { SharedSessionView } from './share/SharedSessionView';
import type { ChatMessage, CostDailyTotal, LoginResult, PaginatedSessionList, PendingInterruptEvent, ProfileForm, QuotaToday, SessionDetail, SessionListItem, StreamPayload, TraceEvent } from './types/chat';

// 会话列表项在后端已截断到 200 字，负载很小，保持 20 条一页。
const SESSION_PAGE_LIMIT = 20;
// 历史消息首屏只取一小页：切换标签页时先出骨架、再尽快出内容。
const MESSAGE_FIRST_PAGE_LIMIT = 8;
// 后台补齐与上滑加载更早消息时的页大小。
const MESSAGE_PAGE_LIMIT = 20;
// 后台最多补齐到多少条历史消息，避免超大会话一次性渲染整段历史。
const MESSAGE_HYDRATE_TARGET = 30;
// 会话内容缓存条数上限：来回切换最近打开过的会话不再重新请求。
const SESSION_CACHE_MAX = 10;
// SHARE_NOTICE_TTL_MS 是「已复制分享链接」提示的停留时长。
const SHARE_NOTICE_TTL_MS = 4000;

export default function App() {
  const [authSession, setAuthSession] = useState(() => getSession());

  useEffect(() => subscribeSession(setAuthSession), []);

  async function handleLogin(username: string, password: string) {
    const result = await fetchJSON<LoginResult>(
        '/api/auth/login',
        {
          method: 'POST',
          headers: { 'Content-Type': 'application/json; charset=utf-8' },
          body: JSON.stringify({ username, password }),
        },
        { handleUnauthorized: false },
    );
    saveSession({ token: result.token, user_id: result.user_id, plan: result.plan });
  }

  // 分享链接是公开只读页：命中 ?share=<token> 时直接渲染快照，
  // 既不弹登录框，也不进入需要登录的工作区。
  const shareToken = readShareToken();
  if (shareToken) {
    return <SharedSessionView token={shareToken} />;
  }

  if (!authSession) {
    return <LoginModal onSubmit={handleLogin} />;
  }
  // 用 user_id 作 key：换账号时整棵工作区重新挂载，
  // 画像、会话、消息等本地状态不会残留到下一个账号。
  return <ChatWorkspace key={authSession.user_id} authSession={authSession} />;
}

// readShareToken 从地址栏读取分享令牌；没有就返回空串，走正常的登录流程。
function readShareToken(): string {
  if (typeof window === 'undefined') {
    return '';
  }
  const params = new URLSearchParams(window.location.search);
  return (params.get('share') ?? '').trim();
}

// CachedSessionDetail 是已打开会话的本地快照，用于瞬时来回切换标签页。
type CachedSessionDetail = {
  messages: ChatMessage[];
  page: number;
  hasMore: boolean;
};

// resolveHasMore 判断历史消息是否还有更早的一页。
// 优先用后端返回的 has_more；旧版本后端没这个字段时，用 total 兜底推断，
// 这样前端分页不会因为后端版本不同而整体失效。
function resolveHasMore(detail: SessionDetail, loadedCount: number): boolean {
  if (typeof detail.has_more === 'boolean') {
    return detail.has_more;
  }
  return typeof detail.total === 'number' && detail.total > loadedCount;
}

// ChatWorkspace 承载单个登录用户的全部界面状态。换账号由上层通过 key 重新挂载来清空。
function ChatWorkspace({ authSession }: { authSession: AuthSession }) {
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
  const [isLoadingSessionDetail, setIsLoadingSessionDetail] = useState(false);
  const [isComposerExpanded, setIsComposerExpanded] = useState(true);
  const [isProfileModalOpen, setIsProfileModalOpen] = useState(false);
  const [shareNotice, setShareNotice] = useState('');
  const [printEntries, setPrintEntries] = useState<ExportEntry[] | null>(null);
  const controllerRef = useRef<AbortController | null>(null);
  const sessionIdRef = useRef('');
  // 导出入口在消息卡片上，回调需要保持引用稳定（见 handleExportMessage），
  // 所以最新的消息列表通过 ref 读取。
  const messagesRef = useRef<ChatMessage[]>([]);
  const isStreamingRef = useRef(false);
  const bottomRef = useRef<HTMLDivElement | null>(null);
  const conversationScrollRef = useRef<HTMLDivElement | null>(null);
  const shouldStickToBottomRef = useRef(true);
  const isLoadingMoreSessionsRef = useRef(false);
  const isLoadingOlderMessagesRef = useRef(false);
  // 会话切换令牌：每次切换自增，异步回来后令牌不一致就丢弃结果，
  // 避免快速连点多个会话时旧请求覆盖新会话。
  const sessionLoadTokenRef = useRef(0);
  // 已打开的会话内容缓存，命中时切换是瞬时的。
  const sessionCacheRef = useRef<Map<string, CachedSessionDetail>>(new Map());
  // 正在后台补齐历史的会话 id（空串表示没有）。
  const hydratingSessionIdRef = useRef('');
  // 插入更早消息后需要恢复的滚动位置，避免视口跳动。
  const scrollAnchorRef = useRef<{ scrollHeight: number; scrollTop: number } | null>(null);
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
    messagesRef.current = messages;
  }, [messages]);

  // 向上插入更早消息时保持视口内容不跳动：
  // 先记下滚动高度差，DOM 更新后一次性补回 scrollTop。
  useLayoutEffect(() => {
    const anchor = scrollAnchorRef.current;
    if (!anchor) {
      return;
    }
    scrollAnchorRef.current = null;
    const element = conversationScrollRef.current;
    if (!element) {
      return;
    }
    element.scrollTop = anchor.scrollTop + (element.scrollHeight - anchor.scrollHeight);
  }, [messages]);

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

  useEffect(() => {
    if (!shareNotice) {
      return;
    }
    const timer = window.setTimeout(() => setShareNotice(''), SHARE_NOTICE_TTL_MS);
    return () => window.clearTimeout(timer);
  }, [shareNotice]);
  // PDF 导出：先把打印文档挂到 DOM，等 Mermaid 这类异步渲染完成后再唤起系统打印，
  // 打印结束（或兜底超时）后卸载这份隐藏文档。
  //
  // 这里不能只靠固定延时：Mermaid 图是异步渲染的，过早打印会把还没画出来的图
  // 变成一段代码块。判定标准是"所有 Mermaid 块都已经产出 svg"。
  //
  // 等待期间界面上有 .print-wait-overlay 遮罩（跟随 printEntries 挂载/卸载），
  // 否则用户点完「导出 PDF」到打印框弹出之间看不到任何反馈。
  useEffect(() => {
    if (!printEntries) {
      return;
    }
    let cancelled = false;
    let timer = 0;
    const startedAt = Date.now();
    const MIN_WAIT_MS = 500;
    const MAX_WAIT_MS = 2500;

    const hasPendingDiagram = () => {
      const root = document.getElementById('export-print-root');
      if (!root) {
        return false;
      }
      const blocks = root.querySelectorAll('.mermaid-fallback, .mermaid-diagram').length;
      if (blocks === 0) {
        return false;
      }
      return root.querySelectorAll('.mermaid-diagram svg').length < blocks;
    };

    const attemptPrint = () => {
      if (cancelled) {
        return;
      }
      const waited = Date.now() - startedAt;
      if (waited < MIN_WAIT_MS || (hasPendingDiagram() && waited < MAX_WAIT_MS)) {
        timer = window.setTimeout(attemptPrint, 150);
        return;
      }
      // window.print() 会阻塞主线程。遮罩与打印文档是同一次提交挂载的，
      // MIN_WAIT_MS 已保证它有足够时间绘制；这里再让出两帧，
      // 避免打印框弹出时遮罩还没画出来。
      window.requestAnimationFrame(() => {
        window.requestAnimationFrame(() => {
          if (cancelled) {
            return;
          }
          try {
            window.print();
          } catch {
            // 个别环境（无打印能力的嵌入式浏览器）会直接抛错，
            // 兜底卸载打印文档与等待遮罩，避免界面卡在等待态。
            setPrintEntries(null);
          }
        });
      });
    };

    timer = window.setTimeout(attemptPrint, 150);
    const fallback = window.setTimeout(() => {
      if (!cancelled) {
        setPrintEntries(null);
      }
    }, 60000);
    const afterPrint = () => {
      if (!cancelled) {
        setPrintEntries(null);
      }
    };
    window.addEventListener('afterprint', afterPrint);
    return () => {
      cancelled = true;
      window.clearTimeout(timer);
      window.clearTimeout(fallback);
      window.removeEventListener('afterprint', afterPrint);
    };
  }, [printEntries]);

  async function checkHealth() {
    try {
      const response = await fetch('/healthz');
      setHealth(response.ok ? 'online' : 'offline');
    } catch {
      setHealth('offline');
    }
  }

  async function handleLogout() {
    try {
      await fetchJSON<null>('/api/auth/logout', { method: 'POST' });
    } catch {
      // 令牌可能已过期，服务端吊销失败不影响本地退出。
    }
    clearSession();
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

  // 切换会话：先把标签页切过去、露出骨架，历史消息在后台拉。
  // 之前是"等接口返回再 setSessionId"，大会话会有明显卡住不动的手感。
  async function loadSession(nextSessionId: string) {
    if (isStreaming) {
      return;
    }
    const token = sessionLoadTokenRef.current + 1;
    sessionLoadTokenRef.current = token;
    shouldStickToBottomRef.current = true;
    // ① 立刻切到点中的标签页。
    setSessionId(nextSessionId);
    setLastTraceId('');
    setPanelError('');

    const cached = sessionCacheRef.current.get(nextSessionId);
    if (cached) {
      // 命中缓存直接出内容，连骨架都不闪。
      setIsLoadingSessionDetail(false);
      setMessages(cached.messages);
      setMessagePage(cached.page);
      setHasMoreMessages(cached.hasMore);
      window.requestAnimationFrame(() => scrollConversationToBottom('auto'));
      return;
    }

    // ② 清掉上一个会话的内容，露出骨架。
    setMessages([]);
    setMessagePage(1);
    setHasMoreMessages(false);
    setIsLoadingSessionDetail(true);

    try {
      // ③ 首屏只拉一小页，保证"先出内容"。
      const detail = await fetchJSON<SessionDetail>(
          `/api/sessions/info?session_id=${encodeURIComponent(nextSessionId)}&page=1&limit=${MESSAGE_FIRST_PAGE_LIMIT}`,
      );
      if (token !== sessionLoadTokenRef.current) {
        return;
      }
      const loadedMessages = detail.list.map(messageRecordToChatMessage);
      const page = detail.page || 1;
      const hasMore = resolveHasMore(detail, loadedMessages.length);
      setIsLoadingSessionDetail(false);
      setMessages(loadedMessages);
      setMessagePage(page);
      setHasMoreMessages(hasMore);
      window.requestAnimationFrame(() => scrollConversationToBottom('auto'));
      cacheSessionDetail(nextSessionId, loadedMessages, page, hasMore);
      // ④ 剩下的历史在后台安静补齐，用户不用等也不用滚。
      void hydrateSessionHistory(nextSessionId, token, loadedMessages, page, hasMore);
    } catch (error) {
      if (token !== sessionLoadTokenRef.current) {
        return;
      }
      setIsLoadingSessionDetail(false);
      setPanelError(error instanceof Error ? error.message : '会话详情加载失败');
    }
  }

  // 后台补齐历史：从第二页起持续拉取，直到达到目标条数或没有更多。
  // 失败不打扰用户，更早的消息仍可上滑手动加载。
  async function hydrateSessionHistory(
      targetSessionId: string,
      token: number,
      seedMessages: ChatMessage[],
      seedPage: number,
      seedHasMore: boolean,
  ) {
    if (!seedHasMore || hydratingSessionIdRef.current === targetSessionId) {
      return;
    }
    hydratingSessionIdRef.current = targetSessionId;

    let currentMessages = seedMessages;
    let page = seedPage;
    // 显式标注 boolean：上面 `if (!seedHasMore) return` 已把参数收窄成 true 字面量，
    // 不标注的话 hasMore 会被推断成 true，后面重新赋值就会报类型错。
    let hasMore: boolean = seedHasMore;
    try {
      while (hasMore && currentMessages.length < MESSAGE_HYDRATE_TARGET) {
        const detail = await fetchJSON<SessionDetail>(
            `/api/sessions/info?session_id=${encodeURIComponent(targetSessionId)}&page=${page + 1}&limit=${MESSAGE_PAGE_LIMIT}`,
        );
        if (token !== sessionLoadTokenRef.current) {
          return;
        }
        const merged = mergeOlderMessages(currentMessages, detail.list.map(messageRecordToChatMessage));
        if (merged.length === currentMessages.length) {
          // 没有新内容说明已经到头，别空转。
          break;
        }
        if (!shouldStickToBottomRef.current) {
          rememberScrollAnchor();
        }
        currentMessages = merged;
        page = detail.page || page + 1;
        hasMore = resolveHasMore(detail, merged.length);
        setMessages(merged);
        setMessagePage(page);
        setHasMoreMessages(hasMore);
        cacheSessionDetail(targetSessionId, merged, page, hasMore);
      }
    } catch {
      // 静默失败。
    } finally {
      if (hydratingSessionIdRef.current === targetSessionId) {
        hydratingSessionIdRef.current = '';
      }
    }
  }

  function cacheSessionDetail(targetSessionId: string, nextMessages: ChatMessage[], page: number, hasMore: boolean) {
    const cache = sessionCacheRef.current;
    // 重新写入时先删除，保证 Map 的插入顺序就是"最近使用"顺序。
    cache.delete(targetSessionId);
    cache.set(targetSessionId, { messages: nextMessages, page, hasMore });
    while (cache.size > SESSION_CACHE_MAX) {
      const oldest = cache.keys().next();
      if (oldest.done) {
        break;
      }
      cache.delete(oldest.value);
    }
  }

  function rememberScrollAnchor() {
    const element = conversationScrollRef.current;
    if (!element) {
      return;
    }
    scrollAnchorRef.current = { scrollHeight: element.scrollHeight, scrollTop: element.scrollTop };
  }

  // 本轮问答落库后本地快照就过期了，下次切回该会话要重新拉取。
  function invalidateActiveSessionCache() {
    const activeSessionId = sessionIdRef.current;
    if (activeSessionId) {
      sessionCacheRef.current.delete(activeSessionId);
    }
  }

  async function loadOlderMessages() {
    if (isStreaming || isLoadingOlderMessagesRef.current || !hasMoreMessages || !sessionIdRef.current) {
      return;
    }
    // 后台补齐已经在按页拉取更早消息，手动加载先让位，避免同一页拉两次。
    if (hydratingSessionIdRef.current === sessionIdRef.current) {
      return;
    }
    shouldStickToBottomRef.current = false;
    isLoadingOlderMessagesRef.current = true;
    setIsLoadingOlderMessages(true);
    const loadedBefore = messagesRef.current.length;
    try {
      const nextPage = messagePage + 1;
      const detail = await fetchJSON<SessionDetail>(`/api/sessions/info?session_id=${encodeURIComponent(sessionIdRef.current)}&page=${nextPage}&limit=${MESSAGE_PAGE_LIMIT}`);
      const olderMessages = detail.list.map(messageRecordToChatMessage);
      rememberScrollAnchor();
      setMessages((current) => mergeOlderMessages(current, olderMessages));
      setMessagePage(detail.page || nextPage);
      setHasMoreMessages(resolveHasMore(detail, loadedBefore + olderMessages.length));
      setPanelError('');
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
      sessionCacheRef.current.delete(targetSessionId);
      setSessions((items) => items.filter((item) => item.session_id !== targetSessionId));
      if (targetSessionId === sessionId) {
        sessionLoadTokenRef.current += 1;
        setSessionId('');
        setMessages([]);
        setMessagePage(1);
        setHasMoreMessages(false);
        setIsLoadingSessionDetail(false);
        setLastTraceId('');
      }
      setPanelError('');
    } catch (error) {
      setPanelError(error instanceof Error ? error.message : '会话删除失败');
    }
  }

  async function shareSession(targetSessionId: string) {
    if (isStreaming) {
      return;
    }
    try {
      const result = await createShareLink(targetSessionId);
      const link = window.location.origin + result.share_path;
      let copied = false;
      try {
        await navigator.clipboard.writeText(link);
        copied = true;
      } catch {
        copied = false;
      }
      if (copied) {
        setShareNotice('已复制分享链接');
      } else {
        // 剪贴板不可用（非安全上下文、无权限等）时至少把链接交给用户。
        window.alert(link);
      }
      setPanelError('');
    } catch (error) {
      setPanelError(error instanceof Error ? error.message : '创建分享失败');
    }
  }

  function startNewSession() {
    if (isStreaming) {
      return;
    }
    // 令牌自增：丢掉可能还在路上的旧会话请求，避免它把内容灌进新聊天。
    sessionLoadTokenRef.current += 1;
    setIsLoadingSessionDetail(false);
    setSessionId('');
    setMessages([]);
    setMessagePage(1);
    setHasMoreMessages(false);
    setLastTraceId('');
  }

  // ── 项目分析导出 ──────────────────────────────────────────────
  // 只有项目分析（repo_analyzer）的回答可导出，判定见 export/exportDocument.ts。
  //
  // 回调会被 memo 过的 MessageItem 持有，引用必须稳定，否则每次流式更新
  // 都会让整列消息重新渲染。
  const handleExportMessage = useCallback((messageId: string, format: ExportFormat) => {
    const current = messagesRef.current;
    const index = current.findIndex((item) => item.id === messageId);
    const entry = index >= 0 ? buildEntryAt(current, index) : null;
    if (!entry) {
      setPanelError('这条回答不可导出：只有项目分析的回答支持导出');
      return;
    }
    setPanelError('');
    const meta = { sessionId: sessionIdRef.current };
    if (format === 'md') {
      downloadTextFile(buildExportFilename(meta, 'md'), buildMarkdownDocument([entry], meta));
      return;
    }
    // PDF 交给浏览器打印（见 printEntries 对应的副作用）。
    setPrintEntries([entry]);
  }, []);

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
      const response = await fetchAuthorized('/api/chat/stream', {
        method: 'POST',
        headers: {
          'Content-Type': 'application/json; charset=utf-8',
        },
        body: JSON.stringify({
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
      invalidateActiveSessionCache();
      void checkHealth();
      void refreshMetrics();
      void refreshSessions();
    }
  }, [profile]);

  function handleStreamEvent(assistantId: string, eventName: string, payload: StreamPayload) {
    // 后端历史版本把工具结果也发成 tool_call，这里统一归一化后再分发。
    switch (resolveStreamEventType(eventName, payload)) {
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
      const response = await fetchAuthorized('/api/chat/resume', {
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
      invalidateActiveSessionCache();
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
              onShareSession={shareSession}
              onOpenProfile={() => setIsProfileModalOpen(true)}
              shareNotice={shareNotice}
          />
        </aside>

        <main className="chat-stage">
          <div
              ref={conversationScrollRef}
              className={`conversation-scroll${shouldShowElevatorNav(messages) ? ' has-elevator' : ''}`}
              onScroll={handleConversationScroll}
          >
            <section className="workspace-meta-bar">
              <span className="workspace-meta-chip">账号: {authSession.user_id}（{authSession.plan}）</span>
              {sessionId && <span className="workspace-meta-chip">Session: {sessionId}</span>}
              {lastTraceId && <span className="workspace-meta-chip">Trace: {lastTraceId}</span>}
              <button className="workspace-meta-chip trace-entry-button" type="button" onClick={handleLogout}>
                退出登录
              </button>
            </section>

            {isLoadingSessionDetail && messages.length === 0 ? (
                <ChatSkeleton />
            ) : (
                <MessageList
                    messages={messages}
                    bottomRef={bottomRef}
                    isLoadingOlderMessages={isLoadingOlderMessages}
                    hasMoreMessages={hasMoreMessages}
                    onExportMessage={handleExportMessage}
                />
            )}
          </div>

          <ElevatorNav
              messages={messages}
              containerRef={conversationScrollRef}
          />

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
        {printEntries && (
            <div
                className="print-wait-overlay"
                role="dialog"
                aria-modal="true"
                aria-busy="true"
                aria-label="正在准备打印稿"
            >
              <div className="print-wait-card">
                <span className="print-wait-spinner" aria-hidden="true" />
                <p className="print-wait-title">正在准备打印稿…</p>
                <p className="print-wait-hint">正在等待图表渲染完成，随后会弹出系统打印对话框。</p>
              </div>
            </div>
        )}
        {printEntries && <ExportPrintRoot entries={printEntries} meta={{ sessionId }} />}
      </div>
  );
}
