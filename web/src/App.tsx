import { useCallback, useEffect, useLayoutEffect, useRef, useState, useSyncExternalStore } from 'react';
import { InterruptModal } from './InterruptModal';
import { fetchJSON } from './api/client';
import { attachRun, readActiveRun, resumeChatRun, streamChatRun, SessionBusyError, type RunStreamPhase } from './api/runStream';
import { RunRegistry, type RunHandle } from './api/runRegistry';
import { LoginModal } from './auth/LoginModal';
import { clearSession, getSession, saveSession, subscribeSession } from './auth/session';
import type { AuthSession } from './auth/session';
import { MessageList } from './chat/MessageList';
import { ChatSkeleton } from './chat/ChatSkeleton';
import { EmptyState } from './chat/EmptyState';
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
import { applyBufferUpdates, mergeLiveMessages, mergeRunUpdateBuffers, projectRunUpdates, resolveTargetMessageId, shouldAcceptRunEvent } from './chat/sessionStreamBuffer';
import type { RunUpdateBuffer } from './chat/sessionStreamBuffer';
import {
  PENDING_SESSION_PREFIX,
  insertPendingSession,
  mergeRemoteWithPending,
  removePendingSession,
  replacePendingSession,
} from './sessions/sessionList';
import { Composer } from './composer/Composer';
import { loadProfile, profileFromServer, saveProfile as cacheProfile } from './profile/storage';
import { fetchProfile, saveProfileToServer } from './profile/api';
import { profileForChat, normalizeProfileUpdatedAt } from './profile/sync';
import type { ProfileSyncStatus } from './profile/sync';
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
import { CostModal } from './cost/CostModal';
import { SessionPanel } from './sessions/SessionPanel';
import { createShareLink } from './share/api';
import { SharedSessionView } from './share/SharedSessionView';
import { AppearanceProvider } from './theme/AppearanceContext';
import { AppearanceSwitcher } from './theme/AppearanceSwitcher';
import { MenuIcon } from './ui/icons';
import { sectionHueAt, sectionLabel, shortId } from './ui/palette';
import type { ChatMessage, ChatMessageRecord, ChatRunActiveResp, CostDailyTotal, LoginResult, PaginatedSessionList, PendingInterruptEvent, ProfileForm, QuotaToday, SendCodeResult, SessionDetail, SessionListItem, StreamPayload, TraceEvent, VersionInfo } from './types/chat';

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
// 会话忙（409）且拿不到服务端提示时的兜底文案。
const SESSION_BUSY_FALLBACK_MESSAGE = '该会话正在生成中，请稍候或先停止';
// 已成功接管到那条已有 run 时的提示（区别于"没能接管、只给提示"）。
const SESSION_BUSY_ATTACHED_MESSAGE = '该会话正在生成，已为你接着显示此前的内容';

export default function App() {
  const [authSession, setAuthSession] = useState(() => getSession());
  // 邮箱注册需要服务端同时开启注册开关并配置好 SMTP；未开启时登录页保持原样。
  const [emailRegisterEnabled, setEmailRegisterEnabled] = useState(false);
  // 密码重设只依赖 SMTP，注册关闭时仍可对老用户开放。
  const [passwordResetEnabled, setPasswordResetEnabled] = useState(false);
  // 只有「本标签页刚刚完成登录」才弹画像强引导；从 localStorage 恢复登录态
  // （刷新页面、新标签页直接打开）不弹，避免把已登录用户当成新用户反复拦一次。
  const [promptProfileOnLogin, setPromptProfileOnLogin] = useState(false);

  useEffect(() => subscribeSession(setAuthSession), []);

  // 登录前只有 /api/version 可访问，用它确认服务端是否开放邮箱注册与密码重设：
  // 没开就不显示入口，避免出现"点了才报错"的按钮。
  useEffect(() => {
    if (authSession) {
      return;
    }
    let cancelled = false;
    fetchJSON<VersionInfo>('/api/version', undefined, { handleUnauthorized: false })
      .then((version) => {
        if (!cancelled) {
          setEmailRegisterEnabled(Boolean(version.email_register));
          setPasswordResetEnabled(Boolean(version.password_reset));
        }
      })
      .catch(() => {
        // 探测失败按未开放处理，账号登录不受影响。
      });
    return () => {
      cancelled = true;
    };
  }, [authSession]);

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
    // 先记下「本次是主动登录」，再写入会话触发工作区挂载：
    // 这样即使两次状态更新没有批处理，ChatWorkspace 首次渲染也能拿到 true。
    setPromptProfileOnLogin(true);
    saveSession({ token: result.token, user_id: result.user_id, plan: result.plan });
  }

  // 邮箱注册成功时后端已签发令牌，因此和普通登录一样直接进入工作台。
  async function handleRegister(email: string, code: string, password: string) {
    const result = await fetchJSON<LoginResult>(
        '/api/auth/register',
        {
          method: 'POST',
          headers: { 'Content-Type': 'application/json; charset=utf-8' },
          body: JSON.stringify({ email, code, password }),
        },
        { handleUnauthorized: false },
    );
    setPromptProfileOnLogin(true);
    saveSession({ token: result.token, user_id: result.user_id, plan: result.plan });
  }

  // 发送邮箱验证码。返回服务端的重发间隔，登录弹窗据此启动倒计时。
  async function handleSendCode(email: string, purpose: 'register' | 'reset'): Promise<number> {
    const result = await fetchJSON<SendCodeResult>(
        '/api/auth/email/code',
        {
          method: 'POST',
          headers: { 'Content-Type': 'application/json; charset=utf-8' },
          body: JSON.stringify({ email, purpose }),
        },
        { handleUnauthorized: false },
    );
    return result.resend_after;
  }

  // 密码重设成功后旧令牌会被后端拒绝，因此不保存登录态，回到登录页重新登录。
  async function handleResetPassword(email: string, code: string, password: string) {
    await fetchJSON<null>(
        '/api/auth/password/reset',
        {
          method: 'POST',
          headers: { 'Content-Type': 'application/json; charset=utf-8' },
          body: JSON.stringify({ email, code, password }),
        },
        { handleUnauthorized: false },
    );
  }


  // 登出（或令牌失效）后清掉标记：之后即使别的标签页登录同步过来，
  // 本标签页也只是恢复登录态，不会误弹画像强引导。
  useEffect(() => {
    if (!authSession) {
      setPromptProfileOnLogin(false);
    }
  }, [authSession]);

  // 分享链接是公开只读页：命中 ?share=<token> 时直接渲染快照，
  // 既不弹登录框，也不进入需要登录的工作区。
  const shareToken = readShareToken();
  if (shareToken) {
    return <SharedSessionView token={shareToken} />;
  }

  if (!authSession) {
    return (
        <LoginModal
            onSubmit={handleLogin}
            emailRegisterEnabled={emailRegisterEnabled}
            passwordResetEnabled={passwordResetEnabled}
            onRegister={handleRegister}
            onResetPassword={handleResetPassword}
            onSendCode={handleSendCode}
        />
    );
  }
  // 用 user_id 作 key：换账号时整棵工作区重新挂载，
  // 画像、会话、消息等本地状态不会残留到下一个账号。
  // 外观 Provider 只包住已登录工作区：登录页与只读分享页保持经典样式，
  // 不会被持久化的新设计色板“半套”污染。
  return (
    <AppearanceProvider>
      <ChatWorkspace key={authSession.user_id} authSession={authSession} promptProfileOnLogin={promptProfileOnLogin} />
    </AppearanceProvider>
  );
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
type ChatWorkspaceProps = {
  authSession: AuthSession;
  // 本标签页刚刚登录成功时为 true：此时弹画像强引导。
  // 恢复已有登录态（刷新/新标签页）为 false：直接进入工作区，不再弹。
  promptProfileOnLogin: boolean;
};

function ChatWorkspace({ authSession, promptProfileOnLogin }: ChatWorkspaceProps) {
  // 画像优先取本机缓存（按 user_id 分键）：刷新页面不会退回默认值，也不会把默认值
  // 当成用户画像再提交一次。缓存为空时用 initialProfile 兜底。
  const [profile, setProfile] = useState<ProfileForm>(() => loadProfile(authSession.user_id));
  // 服务端最后一次实际修改画像的时间。只用于弹窗的“已保存”反馈，不参与表单编辑。
  const [profileUpdatedAt, setProfileUpdatedAt] = useState('');
  const [sessionId, setSessionId] = useState('');
  const [interruptEvent, setInterruptEvent] = useState<PendingInterruptEvent | null>(null);
  const [messages, setMessages] = useState<ChatMessage[]>([]);
  const [sessions, setSessions] = useState<SessionListItem[]>([]);
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
  const [isSidebarOpen, setIsSidebarOpen] = useState(false);
  // 消耗明细弹窗：由左侧栏底部的账号卡片打开，只读展示，不影响对话流。
  const [showCost, setShowCost] = useState(false);
  // 右侧页缘标签轨当前指向的消息；用来给顶部图幅标签染上同一块分区色。
  const [activeMessageId, setActiveMessageId] = useState('');
  // 刚登录时先弹画像：挂载即打开，并且是「强引导」——不填完保存进不去工作区。
  // 恢复登录态（刷新、新标签页）时保持关闭；手动打开（会话面板的「基础画像」按钮）
  // 走可关闭的编辑模式。
  const [isProfileModalOpen, setIsProfileModalOpen] = useState(promptProfileOnLogin);
  const [isProfileGate, setIsProfileGate] = useState(promptProfileOnLogin);
  // 画像从服务端回读的状态。只有 ready 才允许随对话提交画像；
  // pending（仍在请求）和 failed（请求失败）都必须发送 null，避免默认值覆盖服务端。
  const [profileSyncStatus, setProfileSyncStatus] = useState<ProfileSyncStatus>('pending');
  // 弹窗要等回读“有结果”才渲染：否则表单会先拿本地默认值渲染，服务端值回来后
  // 草稿状态不会跟着变。成功/失败都算有结果；失败时允许用户手动填写并显式保存。
  const isProfileSyncSettled = profileSyncStatus !== 'pending';
  const [shareNotice, setShareNotice] = useState('');
  const [printEntries, setPrintEntries] = useState<ExportEntry[] | null>(null);
  const sessionIdRef = useRef('');
  // 导出入口在消息卡片上，回调需要保持引用稳定（见 handleExportMessage），
  // 所以最新的消息列表通过 ref 读取。
  const messagesRef = useRef<ChatMessage[]>([]);
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
  // 正在懒加载调用详情的消息 id，避免重复点击发多次请求。
  const loadingMessageDetailsRef = useRef<Set<string>>(new Set());
  // 已回放完事件的历史消息。后台补齐/上滑分页会用服务端轻量数据重建列表，
  // 这里保存最新版本，重建时覆盖回去，避免刚加载的详情被旧数据冲掉。
  const enrichedMessageDetailsRef = useRef<Map<string, ChatMessage>>(new Map());
  // 正在后台补齐历史的会话 id（空串表示没有）。
  const hydratingSessionIdRef = useRef('');
  // 插入更早消息后需要恢复的滚动位置，避免视口跳动。
  const scrollAnchorRef = useRef<{ scrollHeight: number; scrollTop: number } | null>(null);
  // 批量更新队列：run 句柄 → (消息 id → 待应用的更新函数)。
  // 按 run 分桶是为了并行时一方的增量不会挤进另一方的投影。
  const assistantUpdateQueueRef = useRef<Map<RunHandle, RunUpdateBuffer>>(new Map());
  const assistantUpdateTimerRef = useRef<number | null>(null);
  // 已经尝试过"重新挂载在跑的 run"的会话集合，避免同一会话重复挂载。
  const reattachedSessionsRef = useRef<Set<string>>(new Set());

  // ── 会话级流式调度 ──────────────────────────────────────────────
  // 并行输出的状态键是"会话"，不是"页面"：每条 run 一个句柄（runId/lastSeq/
  // assistantId/AbortController），切走会话只是 detach（保活后台接收），
  // 只有用户主动点停止才 abort。同会话串行由 isBusy 判定。
  const registryRef = useRef<RunRegistry | null>(null);
  if (registryRef.current === null) {
    registryRef.current = new RunRegistry();
  }
  const registry = registryRef.current;
  // 订阅句柄表变化：侧栏"生成中"指示、顶栏状态章、输入框发送/停止切换都靠它刷新。
  useSyncExternalStore(
      useCallback((listener: () => void) => registry.subscribe(listener), [registry]),
      useCallback(() => registry.getVersion(), [registry]),
      useCallback(() => registry.getVersion(), [registry]),
  );

  // liveRunsRef 保存"每条 run 正在生成的那一轮"的本地消息数组，键是句柄本身。
  //
  // 它是进行中这一轮的**唯一真身**：后端要等整轮跑完才写会话，所以尚未落库的
  // user / assistant 消息只存在这里。patchAssistant 直接改它，投影到视口时
  // 再把这一轮按消息 id 合并进去。
  //
  // 键用句柄（对象）而不是会话 id：新建会话在服务端给回 session_id 之前是空串，
  // 之后又会被迁移成真正的 id，用字符串做键会在迁移瞬间丢数据。
  const liveRunsRef = useRef<Map<RunHandle, ChatMessage[]>>(new Map());

  // 尚未落库的新会话：本地 sessionId 还是空串，但流式句柄需要一个稳定的键，
  // 否则同一新会话里连点两次会被当成两个不同会话（同会话串行就失效了）。
  // 这个键只在本地用，绝不作为 session_id 发给服务端。
  const pendingSessionKeyRef = useRef('');

  // currentOwnerKey 返回"当前视图对应的调度键"：已有会话就是 session_id，
  // 新会话则用本地待定键（首次提交时生成）。
  function currentOwnerKey(): string {
    const activeSessionId = sessionIdRef.current;
    if (activeSessionId) {
      return activeSessionId;
    }
    return pendingSessionKeyRef.current;
  }

  function ensurePendingSessionKey(): string {
    if (!pendingSessionKeyRef.current) {
      pendingSessionKeyRef.current = `${PENDING_SESSION_PREFIX}${crypto.randomUUID()}`;
    }
    return pendingSessionKeyRef.current;
  }

  // liveMessagesFor 取某个会话正在生成的那一轮（没有则 undefined）。
  function liveMessagesFor(targetSessionId: string): ChatMessage[] | undefined {
    const handle = registry.get(targetSessionId);
    return handle ? liveRunsRef.current.get(handle) : undefined;
  }

  useEffect(() => {
    void checkHealth();
    void refreshMetrics();
    void refreshSessions();
  }, []);

  // 登录（或换账号）后从服务端读一次画像：服务端是按 user_id 存的权威来源，
  // 本地 localStorage 只是"秒开"用的缓存。读失败就用缓存兜底，不阻塞强引导。
  useEffect(() => {
    let cancelled = false;
    fetchProfile()
      .then((server) => {
        if (cancelled) {
          return;
        }
        setProfile((current) => {
          const merged = profileFromServer(current, server);
          cacheProfile(authSession.user_id, merged);
          return merged;
        });
        setProfileUpdatedAt(normalizeProfileUpdatedAt(server.updated_at));
        // 只有真正拿到服务端画像才算同步完成；失败必须保持 failed，
        // 否则下一轮对话会把本地默认画像全量写回服务端。
        setProfileSyncStatus('ready');
      })
      .catch(() => {
        if (!cancelled) {
          setProfileSyncStatus('failed');
        }
      });
    return () => {
      cancelled = true;
    };
  }, [authSession.user_id]);

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

  // 进入会话（含刷新后重新点开会话）时，如果服务端还有正在跑的 run，
  // 就按 run_id 重新挂载，把断连期间错过的事件补齐。
  // 放在历史消息加载完成之后，避免与 loadSession 的 setMessages([]) 抢 state。
  useEffect(() => {
    if (!sessionId || isLoadingSessionDetail) {
      return;
    }
    // 该会话已经在我们手里跟着（正在生成），不需要再挂一次。
    if (registry.isBusy(sessionId)) {
      return;
    }
    if (reattachedSessionsRef.current.has(sessionId)) {
      return;
    }
    reattachedSessionsRef.current.add(sessionId);
    void reattachActiveRun(sessionId);
  }, [sessionId, isLoadingSessionDetail, registry]);

  useEffect(() => {
    // 空工作区的封面从顶部开始读，不要因为初始化就滚到底部把标题藏起来。
    if (messages.length === 0 || !shouldStickToBottomRef.current) {
      return;
    }
    const frame = window.requestAnimationFrame(() => {
      scrollConversationToBottom('auto');
      window.requestAnimationFrame(() => scrollConversationToBottom('auto'));
    });
    return () => window.cancelAnimationFrame(frame);
  }, [messages]);

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
      // 直接覆盖会把本地占位会话冲掉——新会话刚发出首问、服务端还没返回列表时，
      // 任何一次后台刷新（健康检查、成本指标、其它会话结束）都会让侧栏那条
      // "正在生成的会话"凭空消失。所以把服务端列表与本地占位合并：
      // 服务端已经能查到的用服务端数据，查不到的占位继续留着。
      setSessions((current) => mergeRemoteWithPending(current, page.list));
      setSessionPage(page.page);
      setHasMoreSessions(page.has_more);
      setPanelError('');
    } catch (error) {
      setPanelError(error instanceof Error ? error.message : '会话记录加载失败');
    } finally {
      setIsLoadingSessions(false);
    }
  }

  // mergeRemoteWithPending 用服务端列表刷新，但保留尚未落到服务端的本地占位项。
  // 具体合并规则在 sessions/sessionList.ts（纯函数，可单测）。
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
  //
  // 跨会话并行之后这里**不再有闸门**：切走 = detach（后台 run 保活继续接收，
  // 只是不再更新视口），切回 = 把那一轮的本地消息合并回视口继续接收。
  async function loadSession(nextSessionId: string) {
    // 点中的是"本地待定会话"：它服务端还不存在，不能去请求它的详情。
    // 这种会话只可能有一个（=当前正在发起首问的那个），此时用户其实已经停在那儿了，
    // 直接忽略即可；如果待定键已经变了（说明它已被替换或作废），也无需处理。
    if (nextSessionId.startsWith(PENDING_SESSION_PREFIX)) {
      if (nextSessionId === pendingSessionKeyRef.current) {
        return;
      }
      // 陈旧占位（对应的句柄已不存在）：直接把它从列表里摘掉，避免点了没反应。
      const staleHandle = registry.get(nextSessionId);
      if (!staleHandle) {
        setSessions((current) => current.filter((item) => item.session_id !== nextSessionId));
        return;
      }
    }
    const previousOwnerKey = currentOwnerKey();
    if (previousOwnerKey && previousOwnerKey !== nextSessionId) {
      // 离开前先把当前这一轮（可能正在生成）落进缓存，切回来才有东西可渲染。
      holdCurrentView(previousOwnerKey);
      // detach 而不是 abort：只停止更新视口，绝不中断后台执行。
      const previousHandle = registry.get(previousOwnerKey);
      if (previousHandle) {
        registry.markDetached(previousHandle);
      }
      // 离开"还没落库的新会话"后，待定键就作废了（它可能还在后台跑）。
      // 但若目标本身就是那个待定会话，则不能清——否则切回来就找不到了。
      if (!nextSessionId.startsWith(PENDING_SESSION_PREFIX)) {
        pendingSessionKeyRef.current = '';
      }
    }
    const token = sessionLoadTokenRef.current + 1;
    sessionLoadTokenRef.current = token;
    shouldStickToBottomRef.current = true;
    // ① 立刻切到点中的标签页。
    setSessionId(nextSessionId);
    setLastTraceId('');
    setActiveMessageId('');
    setPanelError('');

    // 该会话可能正在后台生成：重新接管视口，从这一刻起继续接收增量。
    const liveHandle = registry.get(nextSessionId);
    if (liveHandle) {
      registry.markAttached(liveHandle);
      // 关键：丢弃它在后台期间攒下的投影队列。
      // 那些增量已经写进 live 数组，下面的 mergeLiveMessages 会把它们并进视口；
      // 若留着队列，下一次 flush 会把同样的 delta 再应用一遍（内容翻倍）。
      assistantUpdateQueueRef.current.delete(liveHandle);
    }
    const liveMessages = liveMessagesFor(nextSessionId) ?? [];

    const cachedDetail = sessionCacheRef.current.get(nextSessionId);
    const cached = cachedDetail ?? (liveMessages.length > 0
        ? { messages: [] as ChatMessage[], page: 1, hasMore: false }
        : undefined);
    if (cached) {
      // 命中缓存直接出内容，连骨架都不闪。
      // 正在生成的会话用本地这一轮覆盖缓存里的旧快照（不重复拉历史、不重复渲染）。
      const restored = mergeLiveMessages(cached.messages, liveMessages);
      setIsLoadingSessionDetail(false);
      setMessages(restored);
      setMessagePage(cached.page);
      setHasMoreMessages(cached.hasMore);
      window.requestAnimationFrame(() => {
        if (restored.length === 0) {
          scrollConversationToTop();
        } else {
          scrollConversationToBottom('auto');
        }
      });
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
      const loadedMessages = applyEnrichedDetails(detail.list.map(messageRecordToChatMessage));
      const page = detail.page || 1;
      const hasMore = resolveHasMore(detail, loadedMessages.length);
      setIsLoadingSessionDetail(false);
      // 这一轮可能正在生成：历史里还没有它的 user/assistant 消息，
      // 用本地 live 记录补上，避免"刚切回来就只剩骨架"。
      const liveForSession = liveMessagesFor(nextSessionId) ?? [];
      const restoredMessages = mergeLiveMessages(loadedMessages, liveForSession);
      setMessages(restoredMessages);
      setMessagePage(page);
      setHasMoreMessages(hasMore);
      window.requestAnimationFrame(() => {
        if (restoredMessages.length === 0) {
          scrollConversationToTop();
        } else {
          scrollConversationToBottom('auto');
        }
      });
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
        const olderMessages = detail.list.map(messageRecordToChatMessage);
        const merged = applyEnrichedDetails(mergeOlderMessages(currentMessages, olderMessages));
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
        // 用函数式合并当前 state：期间用户可能刚展开过详情，
        // 直接 setMessages(merged) 会把它覆盖回轻量版本。
        setMessages((current) => applyEnrichedDetails(mergeOlderMessages(current, olderMessages)));
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

  // holdCurrentView 把当前视口的消息快照保存下来，供切走再切回时复用。
  // 正在生成的会话必须走这一步：否则切回来时缓存里还是"生成前"的旧快照，
  // 或者干脆命中不了缓存而只能重新拉历史。
  function holdCurrentView(targetSessionId: string) {
    // 只有"正在生成"的会话需要保留进行中的快照：它的最新内容还没落库，
    // 缓存里那份是生成前的旧快照。没有在跑的 run 就让它自然失效，
    // 切回时重新拉服务端已落库的版本（带 recordId，懒加载详情/导出都依赖它）。
    if (!registry.isBusy(targetSessionId)) {
      return;
    }
    const live = liveMessagesFor(targetSessionId);
    if (!live || live.length === 0) {
      return;
    }
    const existing = sessionCacheRef.current.get(targetSessionId);
    const page = existing?.page ?? messagePage;
    const hasMore = existing?.hasMore ?? hasMoreMessages;
    cacheSessionDetail(targetSessionId, live, page, hasMore);
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

  // loadMessageDetails 按需拉取单条消息的完整 render_events，并原位替换消息。
  // 历史首屏只出正文，用户点“加载调用详情”时才付事件解析成本。
  const loadMessageDetails = useCallback(async (messageId: string) => {
    const activeSessionId = sessionIdRef.current;
    const target = messagesRef.current.find((item) => item.id === messageId);
    if (isStreamingFor(activeSessionId)) {
      // 该会话正在跑的 run 会持续往同一条消息里追加事件，此时回放历史事件会覆盖新内容。
      return;
    }
    if (!activeSessionId || !target || target.recordId === undefined || target.eventsLoaded) {
      return;
    }
    if (loadingMessageDetailsRef.current.has(messageId)) {
      return;
    }
    loadingMessageDetailsRef.current.add(messageId);

    setMessages((current) => current.map((item) => (item.id === messageId ? { ...item, eventsLoading: true } : item)));
    try {
      const record = await fetchJSON<ChatMessageRecord>(
          `/api/sessions/message?session_id=${encodeURIComponent(activeSessionId)}&message_id=${target.recordId}`,
      );
      if (sessionIdRef.current !== activeSessionId) {
        return;
      }
      // 先让“加载中”状态渲染出来，再同步回放事件（事件多的消息解析要几十到几百毫秒）。
      await new Promise<void>((resolve) => window.requestAnimationFrame(() => resolve()));
      const enriched = messageRecordToChatMessage(record);
      const enrichedMessage: ChatMessage = {
        ...enriched,
        id: messageId,
        createdAt: target.createdAt ?? enriched.createdAt,
        eventsLoading: false,
      };
      enrichedMessageDetailsRef.current.set(messageId, enrichedMessage);
      const applyEnriched = (item: ChatMessage): ChatMessage => (item.id === messageId ? enrichedMessage : item);
      setMessages((current) => current.map(applyEnriched));
      const cached = sessionCacheRef.current.get(activeSessionId);
      if (cached) {
        sessionCacheRef.current.set(activeSessionId, { ...cached, messages: cached.messages.map(applyEnriched) });
      }
    } catch (error) {
      setMessages((current) => current.map((item) => (item.id === messageId ? { ...item, eventsLoading: false } : item)));
      setPanelError(error instanceof Error ? error.message : '调用详情加载失败');
    } finally {
      loadingMessageDetailsRef.current.delete(messageId);
    }
  }, []);

  function rememberScrollAnchor() {
    const element = conversationScrollRef.current;
    if (!element) {
      return;
    }
    scrollAnchorRef.current = { scrollHeight: element.scrollHeight, scrollTop: element.scrollTop };
  }

  // 本轮问答落库后本地快照就过期了，下次切回该会话要重新拉取。
  // 必须显式传会话 id：并行时后台 run 收尾不能清掉用户正在看的会话的缓存。
  function invalidateActiveSessionCache(targetSessionId: string) {
    if (targetSessionId) {
      sessionCacheRef.current.delete(targetSessionId);
    }
  }

  async function loadOlderMessages() {
    if (isStreamingFor(sessionIdRef.current) || isLoadingOlderMessagesRef.current || !hasMoreMessages || !sessionIdRef.current) {
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
      setMessages((current) => applyEnrichedDetails(mergeOlderMessages(current, olderMessages)));
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

  // 分页/后台补齐可能用服务端轻量记录重建同一条历史消息；
  // 已经懒加载过详情的版本优先，避免刚展开的工具卡片被覆盖。
  function applyEnrichedDetails(items: ChatMessage[]) {
    const enriched = enrichedMessageDetailsRef.current;
    if (enriched.size === 0) {
      return items;
    }
    return items.map((item) => enriched.get(item.id) ?? item);
  }

  async function deleteSession(targetSessionId: string) {
    // 确认框必须排在任何副作用之前：用户点"取消"时不能留下"连接已断、
    // live 记录已删、句柄已释放"的半坏状态（会话还在，却再也收不到输出）。
    if (!window.confirm('确定删除这条会话记录吗？删除后不可恢复。')) {
      return;
    }
    if (isStreamingFor(targetSessionId)) {
      // 已经确认要删：这里才动传输层，避免事件继续写进已删除的会话缓存。
      // 只 abort，不在这里 finish —— 让该 run 的 finally 走正常收尾，
      // 否则 live 记录与句柄状态会不一致（finally 里还会再删一次 live）。
      const handle = registry.get(targetSessionId);
      if (handle) {
        registry.abort(handle);
      }
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
        setActiveMessageId('');
        window.requestAnimationFrame(() => scrollConversationToTop());
      }
      setPanelError('');
    } catch (error) {
      setPanelError(error instanceof Error ? error.message : '会话删除失败');
    }
  }

  async function shareSession(targetSessionId: string) {
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
    // 新建会话与其他会话在跑什么无关：只在发起会话间做 detach。
    const previousSessionId = sessionIdRef.current;
    if (previousSessionId) {
      holdCurrentView(previousSessionId);
      const previousHandle = registry.get(previousSessionId);
      if (previousHandle) {
        registry.markDetached(previousHandle);
      }
    }
    // 无论当前停在哪儿，都换一个全新的待定键：旧的新会话（可能还在后台跑）
    // 只是 detach，不会阻塞这次新建，也不会被误判成"当前会话正在生成"。
    if (pendingSessionKeyRef.current) {
      const pendingHandle = registry.get(pendingSessionKeyRef.current);
      if (pendingHandle) {
        registry.markDetached(pendingHandle);
      }
      pendingSessionKeyRef.current = '';
    }
    // 令牌自增：丢掉可能还在路上的旧会话请求，避免它把内容灌进新聊天。
    sessionLoadTokenRef.current += 1;
    setIsLoadingSessionDetail(false);
    setSessionId('');
    setMessages([]);
    setMessagePage(1);
    setHasMoreMessages(false);
    setLastTraceId('');
    setActiveMessageId('');
    window.requestAnimationFrame(() => scrollConversationToTop());
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

  // ── 流式调度核心 ────────────────────────────────────────────────
  //
  // 每个会话一条 run。事件回调绑定"发起时的会话 id + 消息 id"，并且：
  //   · 该会话就是当前展示会话 → 同步更新视口（保证流式平滑）
  //   · 不是当前会话 → 只更新缓存与 live 记录，不碰视口（跨会话不串台）
  // 批量更新本身也按消息 id 过滤，所以即便投影时机有偏差，
  // 也不会把 A 会话的增量写到 B 会话的消息上。

  // findAssistantForRun 在既有消息里找"某条 run 原本对应的 assistant 消息"。
  //
  // 409 接管时绝不能新建消息：那条 run 的正文早就在会话里了（或正在往那条上写），
  // 新建一条只会让旧 run 的事件灌进新气泡，出现重复渲染与内容错位。
  // 优先用 recordId（服务端消息 id）匹配 run 的归属，退回到最后一条 assistant。
  function findAssistantForRun(targetSessionId: string, runMessages: ChatMessage[]): ChatMessage | null {
    // 候选源**只用调用方传进来的这一份**（该 run 的 live 快照）。
    //
    // 之前这里会在"目标会话恰好是当前视图"时改读 messagesRef.current，
    // 形成两个不同源的候选：调用方按 live 传参、函数却悄悄换成视口快照，
    // 结论随"用户此刻在看哪个会话"而变，属于难复现的隐式耦合。
    // live 快照是超集（视口内容都是从它投影出来的），只读它就够了。
    void targetSessionId;
    const candidates = runMessages;
    for (let index = candidates.length - 1; index >= 0; index -= 1) {
      const item = candidates[index];
      if (item.role !== 'assistant') {
        continue;
      }
      // 还带着 recordId 的说明是已落库的历史消息，正是我们要复用的那条。
      if (item.recordId !== undefined) {
        return item;
      }
    }
    // 退回：会话里最后一条 assistant（可能是本标签页此前生成、尚未落库的那条）。
    for (let index = candidates.length - 1; index >= 0; index -= 1) {
      if (candidates[index].role === 'assistant') {
        return candidates[index];
      }
    }
    return null;
  }

  // adoptBusyRun 处理"本次提交被 409 挡下，改挂到该会话已有 run 上"。
  //
  // 三件事必须同时做到：
  //   ① 废弃本次刚加的 user 气泡 + assistant 占位（它们的 run 从未存在过）；
  //   ② 把句柄的消息归属改成该 run 原本那条 assistant 消息（绝不能新建）；
  //   ③ 提示用户"已为你接着显示"，而不是报错。
  //
  // 返回 true 表示接管成功，调用方后续不得再触碰本次的 assistantId。
  function adoptBusyRun(
      ownerSessionId: string,
      handle: RunHandle,
      placeholderAssistantId: string,
      placeholderUserId: string,
  ): boolean {
    // ① 先解析落点，**再**决定删什么。
    //
    //    顺序很关键：如果先删占位再发现"找不到归属消息"，handle.assistantId
    //    会残留一个已被删除的 id，随后的事件全部写到不存在的消息上 ——
    //    静默丢弃，正是本轮要修的那类缺陷。所以先找、后删。
    //
    //    候选快照取"该会话自己的 live"，而不是当前视口的 messages：
    //    409 发生时用户可能正看着别的会话，按视口找会抓到错误会话的最后一条
    //    assistant。liveRunsRef 里存的才是这个 handle 这一轮的真身。
    const runMessages = liveRunsRef.current.get(handle) ?? [];
    const existing = findAssistantForRun(ownerSessionId, runMessages);

    const stripIds = new Set<string>([placeholderUserId]);
    if (existing) {
      // 有归属消息：本次的两个占位都废弃，写入权交给历史消息。
      stripIds.add(placeholderAssistantId);
    }
    // 没有归属消息时**保留 assistant 占位**作为可写落点：宁可显示在一个新气泡里，
    // 也不能把旧 run 的后续输出丢掉（删除它会让后续事件无处可写）。

    const strip = (items: ChatMessage[]) => items.filter((item) => !stripIds.has(item.id));
    setMessages((current) => {
      const next = strip(current);
      return next.length === current.length ? current : next;
    });
    const live = liveRunsRef.current.get(handle);
    if (live) {
      liveRunsRef.current.set(handle, strip(live));
    }
    const cached = sessionCacheRef.current.get(ownerSessionId);
    if (cached) {
      sessionCacheRef.current.set(ownerSessionId, { ...cached, messages: strip(cached.messages) });
    }

    // ② 落点：优先历史消息，否则沿用（已被保留的）占位消息。
    //    两种情况都必须是一个**真实存在、可写**的消息 id。
    const targetId = existing ? existing.id : placeholderAssistantId;
    handle.assistantId = targetId;
    // 让它继续显示"生成中"，随后的 delta 会接着往这条上写。
    patchAssistant(handle, targetId, (item) => ({ ...item, status: 'streaming' }));
    // ③ 只做状态交接，不在这里写提示文案：文案统一由调用方在 outcome.busy
    //    分支输出一次，避免两条 setPanelError 互相覆盖。
    return true;
  }

  // beginHandle 登记句柄并把 live 记录接到当前会话上（发起前调用）。
  function beginHandle(ownerSessionId: string, assistantId: string, seedMessages: ChatMessage[]) {
    const handle = registry.begin({
      sessionId: ownerSessionId,
      assistantId,
      status: 'streaming',
    });
    liveRunsRef.current.set(handle, seedMessages);
    return handle;
  }

  // insertPendingSessionItem 在首问时先往侧栏插一条本地占位会话。
  //
  // 为什么需要它：新会话在服务端还没有 session_id（要等首次请求建出来），而
  // 会话列表只在整轮流式结束后才 refreshSessions()——如果什么都不做，用户在
  // 整个生成过程中看不到"我刚问的这个会话"，切走之后就找不回来。
  //
  // 具体列表操作在 sessions/sessionList.ts 里，是纯函数、可单测。
  function insertPendingSessionItem(pendingKey: string, firstMessage: string) {
    setSessions((current) =>
      insertPendingSession(current, pendingKey, firstMessage, authSession.user_id, new Date().toISOString()),
    );
  }

  // removePendingSessionItem 摘掉一条本地占位会话（首问失败、或陈旧占位被点中）。
  function removePendingSessionItem(pendingKey: string) {
    setSessions((current) => removePendingSession(current, pendingKey));
  }

  // replacePendingSessionItem 把占位键换成服务端真实 id。
  function replacePendingSessionItem(pendingKey: string, serverSessionId: string) {
    setSessions((current) => replacePendingSession(current, pendingKey, serverSessionId));
  }

  // adoptServerSessionId 处理新建会话：服务端给出了 session_id 之后，
  // 把句柄与 live 记录一起搬到真正的键上。只有用户仍停在这个（尚未命名的）
  // 会话上时才把视图切过去，避免后台会话把视图拽走。
  function adoptServerSessionId(handle: RunHandle, serverSessionId: string) {
    if (!serverSessionId || serverSessionId === handle.sessionId) {
      return;
    }
    const previousKey = handle.sessionId;
    const wasPending = previousKey !== '' && previousKey === pendingSessionKeyRef.current;
    // 侧栏占位项换成真实 id：必须在 wasPending 分支之外也做，
    // 因为用户可能已经切走（视图不跟），但标签仍然该换成真实会话。
    if (previousKey) {
      replacePendingSessionItem(previousKey, serverSessionId);
    }
    registry.rekey(previousKey, serverSessionId);
    if (wasPending) {
      pendingSessionKeyRef.current = '';
      // 只有用户仍停在这个还没命名的新会话上时才跟着改视图；
      // 后台会话把它自己的 id 迁移过来，绝不能把用户正在看的会话拽走。
      if (sessionIdRef.current === '') {
        // 同步改 ref：state 要到下一次渲染才生效，而这期间事件还在流入，
        // 若只改 state 会出现一小段"事件找不到视口"的空窗。
        sessionIdRef.current = serverSessionId;
        setSessionId(serverSessionId);
      }
    }
  }

  const handleSubmit = useCallback(async (messageText: string) => {
    const message = messageText.trim();
    if (!message) {
      return;
    }
    // requestSessionId 是发给服务端的 session_id（新会话为空串，由服务端建）；
    // ownerSessionId 是本地调度键（新会话用待定键），两者不能混用。
    const requestSessionId = sessionIdRef.current;
    const ownerSessionId = requestSessionId || ensurePendingSessionKey();
    // 同会话串行：该会话已有活跃 run 时拒绝再发一条。
    // 注意判定的是"这一个会话"，其他会话在跑不影响这里。
    if (registry.isBusy(ownerSessionId)) {
      setPanelError('该会话正在生成回复，等它结束后再发下一条（其他会话不受影响）。');
      return;
    }

    // 首问即建标签：新会话在服务端还没有 session_id，但如果等"整轮回复结束才刷新列表"，
    // 用户会在整个生成过程中看不到自己刚问的这个会话——切回来也找不到入口。
    // 所以先在侧栏插一条本地占位项（session_id 用待定键），拿到服务端 id 后再替换。
    if (!requestSessionId) {
      insertPendingSessionItem(ownerSessionId, message);
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

    setPanelError('');
    shouldStickToBottomRef.current = true;
    setIsComposerExpanded(true);
    const seedMessages = [...messagesRef.current, userMessage, assistantPlaceholder];
    const handle = beginHandle(ownerSessionId, assistantId, seedMessages);
    setMessages(seedMessages);
    let sawDone = false;
    // 非空表示"本次提交被 409 挡下，改挂到已有 run 上"，此时本次的占位消息已废弃。
    let adoptedBusyRun = false;

    try {
      // streamChatRun 内部负责断线自动续传：连接被掐断时会带着
      // Last-Event-ID 重新挂上这条 run，把漏掉的事件补齐，UI 无需感知。
      const outcome = await streamChatRun({
        sessionId: requestSessionId,
        message,
        // 服务端画像回读完成前不要提交本地缓存/默认值：后端会把请求里的 profile
        // 全量写库，拿默认值先发一轮会把用户真正保存的画像覆盖掉。
        profile: profileForChat(profile, profileSyncStatus),
        signal: handle.controller.signal,
        onPhase: (phase) => notifyRunPhase(handle, assistantId, phase),
        onRunIds: (info) => {
          if (info.busy && !adoptedBusyRun) {
            // 409 接管：这条 run 不是本次提交创建的。本次刚加的 user 气泡 +
            // assistant 占位必须废弃，把写入权交还给该 run 原本的那条消息。
            adoptedBusyRun = adoptBusyRun(ownerSessionId, handle, assistantId, userMessage.id);
            return;
          }
          if (adoptedBusyRun) {
            return;
          }
          registry.setRunId(handle, info.runId);
          adoptServerSessionId(handle, info.sessionId);
        },
        onEvent: (eventName, payload) => {
          if (eventName === 'done') {
            sawDone = true;
          }
          handleStreamEvent(handle, assistantId, eventName, payload);
        },
      });
      if (outcome.done) {
        sawDone = true;
      }
      // 409 接管的唯一提示出口：无论走 onRunIds 提前接管，还是靠回退挂上，
      // 都在这里输出一次，避免两条文案竞争覆盖。
      if (outcome.busy) {
        setPanelError(SESSION_BUSY_ATTACHED_MESSAGE);
      }

      // 收尾也从句柄取落点：409 接管后 handle.assistantId 已指向既有消息，
      // 仍用闭包里的 assistantId 会把收尾状态写进一个已删除的占位。
      patchAssistant(handle, resolveTargetMessageId(handle, assistantId), (item) => ({
        ...item,
        toolEvents: finalizeToolEvents(item.toolEvents),
        segments: finalizeSegments(item.segments),
        status: sawDone || hasMeaningfulAssistantState(item) ? 'done' : item.status,
      }));
    } catch (error) {
      const isAbort = error instanceof Error && error.name === 'AbortError';
      // 409 会话忙：服务端说这个会话已经有一条 run 在跑。runStream 已经尽力
      // 挂上那条 run；走到这里说明连 run_id 都查不到，那就只给可读提示，
      // 绝不把 "stream request failed: 409" 这类技术服务串甩给用户。
      const isBusy = error instanceof SessionBusyError;
      const busyMessage = isBusy && error instanceof SessionBusyError
          ? error.userMessage
          : SESSION_BUSY_FALLBACK_MESSAGE;
      const messageText = isAbort
          ? '本轮已停止。'
          : isBusy
              ? busyMessage
              : error instanceof Error
                  ? error.message
                  : '请求失败，请检查后端是否已经启动。';

      if (isBusy) {
        setPanelError(busyMessage);
      }

      // catch 收尾同样必须从句柄取落点：409 接管成功后占位已被 strip 掉、
      // handle.assistantId 已指向该 run 原来的历史消息，这里若仍用闭包常量，
      // 收尾状态（done/error）会写到一条已被删除的消息上并被静默丢弃。
      patchAssistant(handle, resolveTargetMessageId(handle, assistantId), (item) => {
        const meaningful = hasMeaningfulAssistantState(item);
        // 会话忙不是这条回复失败：用 done 渲染成一条可读提示，避免红色错误态误导。
        const doneLike = meaningful || isAbort || isBusy;
        return {
          ...item,
          content: item.content || messageText,
          toolEvents: finalizeToolEvents(item.toolEvents),
          segments: finalizeSegments(item.segments),
          status: doneLike ? 'done' : 'error',
        };
      });
    } finally {
      // 只释放自己的句柄；其他会话的 run 不受影响。
      const finalSessionId = handle.sessionId;
      if (handle.superseded) {
        // 已被接管：句柄归接管者所有，这里不能 flush（会把接管后的增量冲掉）、
        // 也不能 finish（会删掉新句柄）。
        return;
      }
      flushAssistantUpdates(handle);
      assistantUpdateQueueRef.current.delete(handle);
      registry.finish(handle);
      liveRunsRef.current.delete(handle);
      invalidateActiveSessionCache(finalSessionId);
      // 首问就失败、始终没拿到服务端 session_id：占位会话留着没有意义
      // （点它只会落进"陈旧占位"分支），直接摘掉，避免侧栏堆一条打不开的空会话。
      // 注意只在它仍是待定键时才清：一旦拿到真实 id，说明会话已真实存在，
      // 该保留（哪怕这一轮因为报错提前结束）。
      if (finalSessionId.startsWith(PENDING_SESSION_PREFIX)) {
        removePendingSessionItem(finalSessionId);
      }
      void checkHealth();
      void refreshMetrics();
      void refreshSessions();
    }
  }, [profile, profileSyncStatus]);

  // notifyRunPhase 把"连接中断，正在重连"这类传输层状态显示出来。
  // 关键点：重连中不能把消息标成 error——执行其实还在服务端继续跑。
  function notifyRunPhase(handle: RunHandle, fallbackAssistantId: string, phase: RunStreamPhase) {
    const assistantId = resolveTargetMessageId(handle, fallbackAssistantId);
    if (phase === 'reconnecting') {
      patchAssistant(handle, assistantId, (item) => ({
        ...item,
        traceEvents: [...item.traceEvents, buildTraceEvent({
          type: 'observe',
          stage: 'connection',
          detail: '连接中断，正在重连并补齐事件…',
          timestamp: new Date().toISOString(),
        })],
      }));
      return;
    }
    if (phase === 'reconnected') {
      patchAssistant(handle, assistantId, (item) => ({
        ...item,
        traceEvents: [...item.traceEvents, buildTraceEvent({
          type: 'observe',
          stage: 'connection',
          detail: '已重新连接，继续接收结果',
          timestamp: new Date().toISOString(),
        })],
      }));
    }
  }

  // reattachActiveRun 处理"刷新页面 / 切回会话时这一轮还在跑"：
  // 按 run_id 重新挂载，服务端会回放断连期间错过的事件。
  async function reattachActiveRun(targetSessionId: string) {
    if (!targetSessionId) {
      return;
    }
    // 该会话已经有句柄（正在我们手里跟着）就不再重复挂载。
    if (registry.isBusy(targetSessionId)) {
      return;
    }

    let runId = '';
    let status = 'running';
    let lastSeq = 0;
    try {
      const resp = await fetchJSON<ChatRunActiveResp>(
          `/api/chat/run/active?session_id=${encodeURIComponent(targetSessionId)}`,
      );
      if (resp?.run?.run_id) {
        runId = resp.run.run_id;
        status = resp.run.status || 'running';
        lastSeq = typeof resp.run.last_seq === 'number' ? resp.run.last_seq : 0;
      }
    } catch {
      // 接口不可用（例如后端版本较旧）时退回本地标记，不打断页面。
    }
    if (!runId) {
      const cached = readActiveRun(targetSessionId);
      if (!cached) {
        return;
      }
      runId = cached.runId;
      lastSeq = cached.lastSeq;
    }
    if (registry.isBusy(targetSessionId)) {
      return;
    }

    // 审批中断的 run：历史消息里已经存下了中断前的正文与 render_events，
    // 这时整段回放会重复渲染，所以只把 interrupt/done/error 重新投给 UI
    // （让审批弹窗恢复出来），正文沿用列表里已有的那条 assistant 消息。
    const interrupted = status === 'interrupted';
    let targetMessageId = interrupted ? findLastAssistantMessageId() : '';
    if (targetMessageId) {
      // 这条消息马上会被续跑事件追加，不能再被历史懒加载的旧快照覆盖。
      enrichedMessageDetailsRef.current.delete(targetMessageId);
    }
    const addedPlaceholder: ChatMessage | null = targetMessageId ? null : {
      id: crypto.randomUUID(),
      role: 'assistant',
      content: '',
      tools: [],
      toolEvents: [],
      segments: [],
      traceEvents: [],
      status: 'streaming',
    };
    if (addedPlaceholder) {
      targetMessageId = addedPlaceholder.id;
    }

    // 只在这个会话就是当前视图会话时往视口里加占位消息；
    // 否则交给缓存，用户切回时自然看到。
    const seedMessages = addedPlaceholder
        ? [...messagesRef.current, addedPlaceholder]
        : messagesRef.current;
    if (addedPlaceholder && targetSessionId === sessionIdRef.current) {
      setMessages(seedMessages);
    }

    shouldStickToBottomRef.current = true;
    const handle = registry.begin({
      sessionId: targetSessionId,
      runId,
      assistantId: targetMessageId,
      lastSeq: interrupted ? 0 : lastSeq,
      status: interrupted ? 'interrupted' : 'running',
    });
    if (seedMessages.length > 0) {
      liveRunsRef.current.set(handle, seedMessages);
    }
    let sawDone = false;
    try {
      const outcome = await attachRun({
        runId,
        sessionId: targetSessionId,
        afterSeq: interrupted ? 0 : lastSeq,
        signal: handle.controller.signal,
        onPhase: (phase) => notifyRunPhase(handle, targetMessageId, phase),
        onEvent: (eventName, payload) => {
          const resolved = resolveStreamEventType(eventName, payload);
          if (interrupted && resolved !== 'interrupt' && resolved !== 'done' && resolved !== 'error') {
            return;
          }
          if (resolved === 'done') {
            sawDone = true;
          }
          handleStreamEvent(handle, targetMessageId, eventName, payload);
        },
      });
      if (outcome.done) {
        sawDone = true;
      }
    } catch (error) {
      // 重新挂载失败（例如连接仍不可用）不弹错误气泡：用户继续追问即可，
      // 或者下次进入该会话时再挂一次。
      if (!(error instanceof Error && error.name === 'AbortError')) {
        console.warn('reattach run failed', error);
      }
    } finally {
      if (handle.superseded) {
        return;
      }
      // 收尾顺序：先把这一轮的增量连同收尾状态冲进视口，再释放句柄与 live 记录。
      patchAssistant(handle, targetMessageId, (item) => ({
        ...item,
        toolEvents: finalizeToolEvents(item.toolEvents),
        segments: finalizeSegments(item.segments),
        status: sawDone || hasMeaningfulAssistantState(item) ? 'done' : item.status,
      }));
      flushAssistantUpdates(handle);
      registry.finish(handle);
      liveRunsRef.current.delete(handle);
      invalidateActiveSessionCache(targetSessionId);
      void checkHealth();
      void refreshMetrics();
      void refreshSessions();
    }
  }

  function findLastAssistantMessageId(): string {
    const list = messagesRef.current;
    for (let index = list.length - 1; index >= 0; index -= 1) {
      if (list[index].role === 'assistant') {
        return list[index].id;
      }
    }
    return '';
  }

  function buildTraceEvent(payload: StreamPayload): TraceEvent {
    return {
      id: crypto.randomUUID(),
      stage: payload.stage ?? payload.type,
      detail: payload.detail ?? '',
      elapsedMs: payload.elapsed_ms,
      timestamp: payload.timestamp,
    };
  }

  // handleStreamEvent 处理一条流事件。
  // ownerSessionId 是"发起这条 run 的会话"，assistantId 是"这条 run 对应的消息"；
  // 两者都锁定在发起时刻，事件内容本身不得改写它们（否则后台会话会串台/串消息）。
  function handleStreamEvent(handle: RunHandle, fallbackAssistantId: string, eventName: string, payload: StreamPayload) {
    // run 一致性守卫：一条句柄只接受它自己那条 run 的事件。
    // 句柄 runId 为空时采纳首个帧；一旦有值，run_id 不同的帧一律丢弃
    // （那是别的 run 串进来的尾巴，写进来就是跨会话/跨 run 污染）。
    const verdict = shouldAcceptRunEvent(handle.runId, payload.run_id);
    if (!verdict.accept) {
      return;
    }
    if (verdict.adoptRunId) {
      handle.runId = verdict.adoptRunId;
    }
    // 落点**每次从 handle 读**：adoptBusyRun 会把 handle.assistantId 改指
    // 该 run 原本那条历史消息，闭包常量此时已经指向一个被移除的占位消息。
    const assistantId = resolveTargetMessageId(handle, fallbackAssistantId);
    // 后端历史版本把工具结果也发成 tool_call，这里统一归一化后再分发。
    switch (resolveStreamEventType(eventName, payload)) {
      case 'ready':
        updateTrace(payload);
        break;
      case 'session':
        if (payload.session_id) {
          adoptServerSessionId(handle, payload.session_id);
        }
        updateTrace(payload);
        break;
      case 'observe':
        updateTrace(payload);
        appendTraceEvent(handle, assistantId, payload);
        break;
      case 'heartbeat':
        updateTrace(payload);
        break;
      case 'tool_call':
        updateTrace(payload);
        if (!hasToolName(payload)) {
          break;
        }
        appendTraceEvent(handle, assistantId, { ...payload, stage: `tool_call:${payload.tool_name}`, detail: payload.tool_arguments });
        patchAssistant(handle, assistantId, (item) => addToolCallWithSegment(item, payload));
        break;
      case 'tool_result':
        updateTrace(payload);
        if (!hasToolName(payload)) {
          break;
        }
        appendTraceEvent(handle, assistantId, { ...payload, stage: `tool_result:${payload.tool_name}`, detail: payload.tool_result });
        patchAssistant(handle, assistantId, (item) => applyToolResultWithSegment(item, payload));
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
        patchAssistant(handle, assistantId, (item) => appendTextBlockSegment(item, payload.delta ?? payload.message ?? payload.detail ?? '', payload, false, 'progress'));
        break;
      case 'delta':
        updateTrace(payload);
        patchAssistant(handle, assistantId, (item) => appendStreamingDeltaSegment(item, payload.delta ?? '', payload));
        break;
      case 'done':
        updateTrace(payload);
        // 服务端是画像的权威来源（按 user_id 存库）：把最终值回写本地状态与缓存，
        // 保证界面显示和后续请求提交的是同一份画像。
        if (payload.result?.profile) {
          const nextProfile = profileFromServer(profile, payload.result.profile);
          setProfile(nextProfile);
          cacheProfile(authSession.user_id, nextProfile);
          setProfileUpdatedAt(normalizeProfileUpdatedAt(payload.result.profile.updated_at));
          // 这一轮能拿到服务端画像，说明画像已经是权威值，后续对话可以正常携带。
          setProfileSyncStatus('ready');
        }
        patchAssistant(handle, assistantId, (item) => ({
          ...item,
          content: normalizeMarkdown(item.content),
          tools: payload.result?.used_tools ?? item.tools,
          toolEvents: finalizeToolEvents(item.toolEvents),
          segments: finalizeSegments(item.segments),
          status: 'done',
        }));
        break;
      case 'error':
        patchAssistant(handle, assistantId, (item) => {
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
    const targetSessionId = ev.session_id ?? sessionIdRef.current;
    shouldStickToBottomRef.current = true;
    // 审批恢复同属"同会话串行"：续跑的是同一条 run、同一条 assistant 消息。
    // 但传输层是新建的，而旧句柄（等审批时挂着的）可能还没走完它的 finally，
    // 所以用 takeover 强制换新句柄，并把旧句柄标记 superseded ——
    // 旧句柄随后的 finally 不会再 flush 或 finish 掉新句柄。
    const inheritedLive = liveMessagesFor(targetSessionId);
    const handle = registry.begin({
      sessionId: targetSessionId,
      assistantId: ev.assistant_message_id,
      status: 'streaming',
      takeover: true,
    });
    // 续跑接手这个会话正在生成的那一轮（没有就沿用当前视口）。
    if (!inheritedLive || inheritedLive.length === 0) {
      liveRunsRef.current.set(handle, messagesRef.current);
    } else {
      liveRunsRef.current.set(handle, inheritedLive);
    }
    patchAssistant(handle, ev.assistant_message_id, (item) => ({ ...item, status: 'streaming' }));
    try {
      // 与普通对话一致：审批恢复同样支持断线续传，续传起点由服务端按
      // 这条 run 的当前游标决定，不会把中断前的事件重复回放一遍。
      await resumeChatRun({
        sessionId: targetSessionId,
        pendingId: ev.pending_approval_id,
        approved,
        signal: handle.controller.signal,
        onPhase: (phase) => notifyRunPhase(handle, ev.assistant_message_id, phase),
        onRunIds: (info) => registry.setRunId(handle, info.runId),
        onEvent: (eventName, payload) => {
          handleStreamEvent(handle, ev.assistant_message_id, eventName, payload);
        },
      });
      patchAssistant(handle, ev.assistant_message_id, (item) => ({
        ...item,
        toolEvents: finalizeToolEvents(item.toolEvents),
        segments: finalizeSegments(item.segments),
        status: hasMeaningfulAssistantState(item) ? 'done' : item.status,
      }));
    } catch (error) {
      // 审批恢复也可能被 409 挡下（同一会话并发两次恢复）：给可读提示。
      const busyMessage = error instanceof SessionBusyError
          ? error.userMessage
          : SESSION_BUSY_FALLBACK_MESSAGE;
      const isBusy = error instanceof SessionBusyError;
      if (isBusy) {
        setPanelError(busyMessage);
      }
      const messageText = isBusy
          ? busyMessage
          : error instanceof Error
              ? error.message
              : '审批恢复请求失败，请稍后再试。';
      patchAssistant(handle, ev.assistant_message_id, (item) => {
        const meaningful = hasMeaningfulAssistantState(item);
        return {
          ...item,
          content: meaningful ? item.content : messageText,
          toolEvents: finalizeToolEvents(item.toolEvents),
          segments: finalizeSegments(item.segments),
          status: meaningful || isBusy ? 'done' : 'error',
        };
      });
    } finally {
      if (handle.superseded) {
        return;
      }
      flushAssistantUpdates(handle);
      registry.finish(handle);
      liveRunsRef.current.delete(handle);
      invalidateActiveSessionCache(targetSessionId);
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

  function appendTraceEvent(handle: RunHandle, messageId: string, payload: StreamPayload) {
    if (!payload.stage && !payload.detail) {
      return;
    }
    patchAssistant(handle, messageId, (item) => ({
      ...item,
      traceEvents: [...item.traceEvents, buildTraceEvent(payload)],
    }));
  }

  // patchAssistant 是流式增量的唯一入口，按"会话 + 消息"记账。
  //
  // 落点分两种：
  //   · 该会话就是当前展示会话 → 40ms 批量并进视口（流式要平滑）
  //   · 后台会话 → 只改它自己那一轮的 live 数组（用户切回时合并出来）
  // 批量应用时再按消息 id 过滤，因此即便投影时机有偏差，
  // 也绝不会把 A 会话的增量写到 B 会话的消息上。
  function patchAssistant(handle: RunHandle, id: string, updater: (item: ChatMessage) => ChatMessage) {
    if (handle.superseded) {
      // 这一轮已被接管（审批恢复重新登记了句柄），旧句柄的尾巴不能再写消息。
      return;
    }
    // 用"当前调度键"判定而不是裸 sessionId：新会话在服务端给回 session_id
    // 之前，句柄的 sessionId 是本地待定键，直接比 sessionIdRef 会永远不相等，
    // 结果就是新会话的第一轮回复完全不出字。
    const isViewing = Boolean(handle.sessionId) && handle.sessionId === currentOwnerKey();

    // live 真身始终同步更新：它代表这一轮的真实内容，与用户在看哪个会话无关。
    const live = liveRunsRef.current.get(handle);
    if (live) {
      const index = live.findIndex((item) => item.id === id);
      if (index >= 0) {
        const next = live.slice();
        next[index] = updater(next[index]);
        liveRunsRef.current.set(handle, next);
      }
    }

    // 只有"用户正在看这个会话"才记账到投影队列。
    //
    // 后台会话**不入队**：它的真身已经在上面写进 live 数组了，切回时由
    // mergeLiveMessages 一次性合并到视口。若后台也入队，切回时就会出现
    // "mergeLive 已经应用过一次 + flush 又应用一次"的二次应用（delta 翻倍）。
    if (!isViewing) {
      return;
    }

    let sessionBuffer = assistantUpdateQueueRef.current.get(handle);
    if (!sessionBuffer) {
      sessionBuffer = new Map<string, Array<(item: ChatMessage) => ChatMessage>>();
      assistantUpdateQueueRef.current.set(handle, sessionBuffer);
    }
    const queuedUpdates = sessionBuffer.get(id);
    if (queuedUpdates) {
      queuedUpdates.push(updater);
    } else {
      sessionBuffer.set(id, [updater]);
    }
    scheduleAssistantUpdateFlush();
  }


  function scheduleAssistantUpdateFlush() {
    if (assistantUpdateTimerRef.current !== null) {
      return;
    }
    assistantUpdateTimerRef.current = window.setTimeout(flushAssistantUpdates, 40);
  }

  // flushAssistantUpdates 把队列里的增量投影到视口。
  //
  // 只应用"消息确实在当前视口里"的那部分（projectRunUpdates 按消息 id 过滤）：
  // 这是跨会话并行不串台的最后一道闸门。传入句柄时只冲刷这一路（该轮收尾），
  // 否则冲刷全部（定时器到期）。
  function flushAssistantUpdates(ownerHandle?: RunHandle) {
    const source = ownerHandle
        ? (assistantUpdateQueueRef.current.has(ownerHandle)
            ? ([[ownerHandle, assistantUpdateQueueRef.current.get(ownerHandle)!]] as Array<[RunHandle, RunUpdateBuffer]>)
            : [])
        : Array.from(assistantUpdateQueueRef.current.entries());

    if (source.length === 0) {
      if (ownerHandle === undefined) {
        assistantUpdateTimerRef.current = null;
      }
      return;
    }
    for (const [bufferedHandle] of source) {
      assistantUpdateQueueRef.current.delete(bufferedHandle);
    }
    if (ownerHandle === undefined) {
      assistantUpdateTimerRef.current = null;
    }

    // 后台会话的增量在这里被整段丢弃投影：它们的真身在 live 数组里已经更新，
    // 用户切回时由 mergeLiveMessages 带出来。
    const projected = source.map(([, buffer]) => projectRunUpdates(messagesRef.current, buffer));
    const merged = mergeRunUpdateBuffers(projected);
    if (merged.size === 0) {
      return;
    }
    setMessages((current) => applyBufferUpdates(current, merged));
  }

  function handleConversationScroll() {
    const scrollElement = conversationScrollRef.current;
    if (!scrollElement) {
      return;
    }
    const distanceToBottom = scrollElement.scrollHeight - scrollElement.scrollTop - scrollElement.clientHeight;
    const isAtBottom = distanceToBottom < 96;
    if (scrollElement.scrollTop < 96 && hasMoreMessages && !isLoadingOlderMessages && !isStreamingFor(sessionIdRef.current)) {
      void loadOlderMessages();
    }
    shouldStickToBottomRef.current = isAtBottom;
    setIsComposerExpanded((current) => (current === isAtBottom ? current : isAtBottom));
  }

  // 停止只作用于**当前会话**的 run，绝不误停其他会话。
  const stopStreaming = useCallback(() => {
    const handle = registry.get(currentOwnerKey());
    if (!handle) {
      return;
    }
    registry.abort(handle);
  }, [registry]);

  // isStreamingFor 判定单个会话是否正在生成（送/停按钮、侧栏圆点、顶栏状态章都用它）。
  const isStreamingFor = useCallback((targetSessionId: string) => registry.isBusy(targetSessionId), [registry]);
  const busySessions = useCallback(() => registry.busySessions(), [registry]);
  const currentSessionStreaming = isStreamingFor(currentOwnerKey());

  function scrollConversationToBottom(behavior: ScrollBehavior) {
    const scrollElement = conversationScrollRef.current;
    if (!scrollElement) return;
    scrollElement.scrollTo({ top: scrollElement.scrollHeight, behavior });
  }

  function scrollConversationToTop(behavior: ScrollBehavior = 'auto') {
    conversationScrollRef.current?.scrollTo({ top: 0, behavior });
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

  const activeSession = sessions.find((item) => item.session_id === sessionId);
  const activeSessionTitle = activeSession?.title || activeSession?.last_user_message || activeSession?.summary
      || (sessionId ? '当前会话' : '新的分析');
  const activeMessageIndex = messages.findIndex((item) => item.id === activeMessageId);
  const activeHue = sectionHueAt(activeMessageIndex >= 0 ? activeMessageIndex : Math.max(messages.length - 1, 0));
  const healthText = health === 'online' ? '后端在线' : health === 'offline' ? '后端离线' : '检查中';

  return (
      <div className={`app-shell${isSidebarOpen ? ' sidebar-open' : ''}`}>
        <aside className="index-board" aria-label="会话索引">
          <SessionPanel
              health={health}
              panelError={panelError}
              sessions={sessions}
              sessionId={sessionId}
              isStreamingFor={isStreamingFor}
              busySessions={busySessions}
              isLoadingMoreSessions={isLoadingMoreSessions}
              hasMoreSessions={hasMoreSessions}
              userId={authSession.user_id}
              plan={authSession.plan}
              onStartNewSession={() => {
                setIsSidebarOpen(false);
                startNewSession();
              }}
              onLoadSession={(nextSessionId) => {
                setIsSidebarOpen(false);
                void loadSession(nextSessionId);
              }}
              onLoadMoreSessions={loadMoreSessions}
              onDeleteSession={deleteSession}
              onShareSession={shareSession}
              onOpenProfile={() => {
                // 手动打开是编辑，不是登录后的强引导：允许取消关闭。
                // 移动端从抽屉里打开时先收起抽屉，避免弹窗被索引板盖住。
                setIsSidebarOpen(false);
                setIsProfileGate(false);
                setIsProfileModalOpen(true);
              }}
              onOpenCost={() => {
                // 移动端同样先收起抽屉，否则抽屉会压在弹窗上面一截。
                setIsSidebarOpen(false);
                setShowCost(true);
              }}
              onLogout={() => void handleLogout()}
              onClose={() => setIsSidebarOpen(false)}
              shareNotice={shareNotice}
          />
        </aside>
        <button
            type="button"
            className="drawer-scrim"
            aria-label="关闭会话索引"
            onClick={() => setIsSidebarOpen(false)}
        />

        <main className="chat-stage">
          <header className="stage-bar">
            <button
                className="mobile-menu-button"
                type="button"
                onClick={() => setIsSidebarOpen(true)}
                aria-label="打开会话索引"
            >
              <MenuIcon size={20} />
            </button>
            <div className="stage-identity">
              <div className="stage-title-row">
                {activeMessageIndex >= 0 && (
                    <span
                        className="stage-section-tab"
                        style={{ background: activeHue.value, color: activeHue.on }}
                        aria-hidden="true"
                    >
                      {sectionLabel(activeMessageIndex)}
                    </span>
                )}
                <h1 className="stage-title" title={activeSessionTitle}>{activeSessionTitle}</h1>
              </div>
              <div className="stage-subline">
                <span className={`stage-health stage-health-${health}`}>
                  <span className="status-dot" aria-hidden="true" />
                  {healthText}
                </span>
                <span className="stage-meta-item">
                  SESSION {sessionId ? shortId(sessionId, 12) : 'NOT SAVED'}
                </span>
                {lastTraceId && <span className="stage-meta-item">TRACE {shortId(lastTraceId, 12)}</span>}
              </div>
            </div>
            <div className="stage-stamps">
              <span className={`run-stamp ${currentSessionStreaming ? 'run-stamp-live' : 'run-stamp-ready'}`}>
                {currentSessionStreaming ? 'RUNNING' : 'READY'}
              </span>
            </div>
            <AppearanceSwitcher />
          </header>

          <div
              ref={conversationScrollRef}
              className={`conversation-scroll${shouldShowElevatorNav(messages) ? ' has-elevator' : ''}`}
              onScroll={handleConversationScroll}
          >
            {isLoadingSessionDetail && messages.length === 0 ? (
                <ChatSkeleton />
            ) : messages.length === 0 ? (
                <EmptyState />
            ) : (
                <MessageList
                    messages={messages}
                    bottomRef={bottomRef}
                    isLoadingOlderMessages={isLoadingOlderMessages}
                    hasMoreMessages={hasMoreMessages}
                    onExportMessage={handleExportMessage}
                    onLoadDetails={loadMessageDetails}
                />
            )}
          </div>

          <ElevatorNav
              messages={messages}
              containerRef={conversationScrollRef}
              onActiveChange={setActiveMessageId}
          />

          <Composer
              isStreaming={currentSessionStreaming}
              isExpanded={isComposerExpanded}
              costDaily={costDaily}
              quota={quota}
              onSubmit={handleSubmit}
              onStop={stopStreaming}
              onScrollTop={scrollToTop}
              onScrollBottom={scrollToBottom}
          />
        </main>
        {isProfileModalOpen && isProfileSyncSettled && (
            <ProfileModal
                profile={profile}
                updatedAt={profileUpdatedAt}
                gate={isProfileGate}
                onSave={async (nextProfile) => {
                  // 先落库再放行：强引导的语义是「保存成功才算填完」，服务端失败就留在弹窗里重试。
                  const saved = await saveProfileToServer(nextProfile);
                  const merged = profileFromServer(nextProfile, saved);
                  setProfile(merged);
                  cacheProfile(authSession.user_id, merged);
                  setProfileUpdatedAt(normalizeProfileUpdatedAt(saved.updated_at));
                  // 显式保存成功后，本地值已确认写入服务端，可以恢复正常携带。
                  setProfileSyncStatus('ready');
                  setIsProfileModalOpen(false);
                  // 保存过一次之后就是普通编辑：再打开时可以取消关闭。
                  setIsProfileGate(false);
                }}
                onClose={() => setIsProfileModalOpen(false)}
            />
        )}
        {showCost && (
            <CostModal onClose={() => setShowCost(false)} />
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
