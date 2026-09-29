import { fetchAuthorized, updateQuotaFromHeaders } from './client';
import { consumeSSE, type SSEEventHandler } from './sse';
import { resolveStreamEventType } from '../chat/messageSegments';
import type { ChatRunInfo, ProfileForm, StreamPayload } from '../types/chat';

// RunStreamPhase 描述一次流式请求所处阶段，供 UI 提示"正在重连"。
// open       首次建立连接
// reconnecting 连接断了，正在退避重试
// reconnected  重试成功，继续接收事件
// closed       不再重试，流结束
export type RunStreamPhase = 'open' | 'reconnecting' | 'reconnected' | 'closed';

export type RunStreamOutcome = {
  /** 是否收到终止事件（done / error）。false 表示重试耗尽仍未收尾。 */
  done: boolean;
  /** 本轮 run_id：拿到后即可用于断继续传。 */
  runId?: string;
  /** 最后一条事件的游标，等价于最后一次上报的 Last-Event-ID。 */
  lastSeq: number;
};

type RunStreamHandlers = {
  onEvent: SSEEventHandler;
  onPhase?: (phase: RunStreamPhase) => void;
};

type RunStreamState = {
  runId: string;
  sessionId: string;
  lastSeq: number;
  terminal: boolean;
};

// 退避序列：500ms → 1s → 2s → 4s → 8s，共 5 次重试。
const RECONNECT_DELAYS_MS = [500, 1000, 2000, 4000, 8000];
const ACTIVE_RUN_KEY_PREFIX = 'run:active:';

export type ActiveRunRecord = { runId: string; lastSeq: number };

// readActiveRun / writeActiveRun / clearActiveRun 用 sessionStorage 记录"这个会话
// 正在跑的 run"。页面刷新后 UI 状态全没了，但这份标记还在，可以据此重新挂载补事件。
export function readActiveRun(sessionId: string): ActiveRunRecord | null {
  if (!sessionId || typeof sessionStorage === 'undefined') {
    return null;
  }
  try {
    const raw = sessionStorage.getItem(ACTIVE_RUN_KEY_PREFIX + sessionId);
    if (!raw) {
      return null;
    }
    const parsed = JSON.parse(raw) as Partial<ActiveRunRecord>;
    if (!parsed?.runId) {
      return null;
    }
    return { runId: parsed.runId, lastSeq: typeof parsed.lastSeq === 'number' ? parsed.lastSeq : 0 };
  } catch {
    return null;
  }
}

export function writeActiveRun(sessionId: string, record: ActiveRunRecord): void {
  if (!sessionId || typeof sessionStorage === 'undefined') {
    return;
  }
  try {
    sessionStorage.setItem(ACTIVE_RUN_KEY_PREFIX + sessionId, JSON.stringify(record));
  } catch {
    // 隐私模式等写失败场景：不影响本轮对话。
  }
}

export function clearActiveRun(sessionId: string): void {
  if (!sessionId || typeof sessionStorage === 'undefined') {
    return;
  }
  try {
    sessionStorage.removeItem(ACTIVE_RUN_KEY_PREFIX + sessionId);
  } catch {
    // 忽略写失败
  }
}

// streamChatRun 发起一轮新对话：POST 建 run，并在连接中断时自动续传。
export async function streamChatRun(params: {
  sessionId: string;
  message: string;
  profile: ProfileForm | null;
  signal?: AbortSignal;
  onEvent: SSEEventHandler;
  onPhase?: (phase: RunStreamPhase) => void;
}): Promise<RunStreamOutcome> {
  const state: RunStreamState = {
    runId: '',
    sessionId: params.sessionId,
    lastSeq: 0,
    terminal: false,
  };
  const handlers: RunStreamHandlers = { onEvent: params.onEvent, onPhase: params.onPhase };
  const firstRequest = async () => {
    const response = await fetchAuthorized('/api/chat/stream', {
      method: 'POST',
      headers: {
        'Content-Type': 'application/json; charset=utf-8',
      },
      body: JSON.stringify({
        session_id: params.sessionId,
        message: params.message,
        profile: params.profile,
      }),
      signal: params.signal,
    });
    updateQuotaFromHeaders(response);
    return response;
  };
  return pumpRun(state, handlers, firstRequest, params.signal);
}

// resumeChatRun 恢复一次审批中断：与 streamChatRun 共用同一套续传逻辑。
// 续传起点由服务端决定（它知道这条 run 已经产生到哪个 seq），因此这里不传 afterSeq。
export async function resumeChatRun(params: {
  sessionId: string;
  pendingId: string;
  approved: boolean;
  signal?: AbortSignal;
  onEvent: SSEEventHandler;
  onPhase?: (phase: RunStreamPhase) => void;
}): Promise<RunStreamOutcome> {
  const state: RunStreamState = {
    runId: '',
    sessionId: params.sessionId,
    lastSeq: 0,
    terminal: false,
  };
  const handlers: RunStreamHandlers = { onEvent: params.onEvent, onPhase: params.onPhase };
  const firstRequest = async () => {
    const response = await fetchAuthorized('/api/chat/resume', {
      method: 'POST',
      headers: {
        'Content-Type': 'application/json; charset=utf-8',
      },
      body: JSON.stringify({
        session_id: params.sessionId,
        pending_id: params.pendingId,
        approved: params.approved,
      }),
      signal: params.signal,
    });
    updateQuotaFromHeaders(response);
    return response;
  };
  return pumpRun(state, handlers, firstRequest, params.signal);
}

// attachRun 按 run_id 重新挂载一条进行中的 run：回放 afterSeq 之后的事件并继续跟随。
// 页面刷新、切换会话回来时用它补齐断连期间错过的事件。
export async function attachRun(params: {
  runId: string;
  sessionId?: string;
  afterSeq?: number;
  signal?: AbortSignal;
  onEvent: SSEEventHandler;
  onPhase?: (phase: RunStreamPhase) => void;
}): Promise<RunStreamOutcome> {
  const state: RunStreamState = {
    runId: params.runId,
    sessionId: params.sessionId ?? '',
    lastSeq: params.afterSeq ?? 0,
    terminal: false,
  };
  const handlers: RunStreamHandlers = { onEvent: params.onEvent, onPhase: params.onPhase };
  const firstRequest = () => fetchRunEvents(state.runId, state.lastSeq, params.signal);
  return pumpRun(state, handlers, firstRequest, params.signal);
}

// pumpRun 是唯一的接收循环：请求 → 消费事件 → 未收尾则退避重连。
async function pumpRun(
  state: RunStreamState,
  handlers: RunStreamHandlers,
  firstRequest: () => Promise<Response>,
  signal?: AbortSignal,
): Promise<RunStreamOutcome> {
  let request = firstRequest;
  let attempt = 0;

  for (;;) {
    try {
      handlers.onPhase?.(attempt === 0 ? 'open' : 'reconnected');
      const response = await request();

      // 服务端明确说这条 run 不可续传（不存在 / 不属于当前用户）：
      // 再重试也是同样的结果，直接收尾。
      if (response.status === 400 || response.status === 404) {
        handlers.onPhase?.('closed');
        return outcomeOf(state, false);
      }
      if (!response.ok || !response.body) {
        throw new Error(`stream request failed: ${response.status}`);
      }

      await consumeSSE(response.body, (eventName, payload, meta) => {
        applyEvent(state, eventName, payload, meta.id, handlers);
      });

      if (state.terminal) {
        clearActiveRun(state.sessionId);
        handlers.onPhase?.('closed');
        return outcomeOf(state, true);
      }
    } catch (error) {
      if (isAbortError(error)) {
        // 用户主动停止：不重连，交由调用方处理中止态。
        throw error;
      }
      if (!state.runId) {
        // 连接在拿到 run_id 之前就断了：先问服务端这个会话有没有正在跑的 run
        // （例如页面刷新后重来），问不到就只能把错误抛给调用方。
        const discovered = await discoverActiveRun(state.sessionId, signal);
        if (!discovered) {
          throw error;
        }
        state.runId = discovered.runId;
        state.lastSeq = Math.max(state.lastSeq, discovered.lastSeq);
      }
    }

    if (attempt >= RECONNECT_DELAYS_MS.length) {
      break;
    }
    handlers.onPhase?.('reconnecting');
    await delay(RECONNECT_DELAYS_MS[attempt], signal);
    attempt += 1;
    request = () => fetchRunEvents(state.runId, state.lastSeq, signal);
  }

  handlers.onPhase?.('closed');
  // 重试耗尽仍没收尾：保留 sessionStorage 里的活跃 run 记录，
  // 这样用户刷新页面或重新进入会话还能再挂一次。
  return outcomeOf(state, false);
}

function applyEvent(
  state: RunStreamState,
  eventName: string,
  payload: StreamPayload,
  metaId: string | undefined,
  handlers: RunStreamHandlers,
) {
  if (payload.run_id) {
    state.runId = payload.run_id;
  }
  if (payload.session_id) {
    state.sessionId = payload.session_id;
  }

  const seq = parseSeq(metaId ?? (typeof payload.seq === 'number' ? String(payload.seq) : ''));
  if (seq !== null && seq > state.lastSeq) {
    state.lastSeq = seq;
  }

  const resolvedType = resolveStreamEventType(eventName, payload);
  if (resolvedType === 'done' || resolvedType === 'error') {
    state.terminal = true;
  }

  if (state.runId && state.sessionId) {
    if (state.terminal) {
      clearActiveRun(state.sessionId);
    } else {
      writeActiveRun(state.sessionId, { runId: state.runId, lastSeq: state.lastSeq });
    }
  }

  // 先落游标再抛事件：调用方即便在回调里抛错，重连起点也是对的。
  handlers.onEvent(eventName, payload, metaId ? { id: metaId } : {});
}

function outcomeOf(state: RunStreamState, done: boolean): RunStreamOutcome {
  const outcome: RunStreamOutcome = { done, lastSeq: state.lastSeq };
  if (state.runId) {
    outcome.runId = state.runId;
  }
  return outcome;
}

function fetchRunEvents(runId: string, afterSeq: number, signal?: AbortSignal): Promise<Response> {
  return fetchAuthorized(
    `/api/chat/stream/run?run_id=${encodeURIComponent(runId)}&after=${afterSeq}`,
    {
      method: 'GET',
      // 两种游标都带上：后端优先读 Last-Event-ID，after 作为兼容兜底。
      headers: { 'Last-Event-ID': String(afterSeq) },
      signal,
    },
  );
}

async function discoverActiveRun(sessionId: string, signal?: AbortSignal): Promise<ActiveRunRecord | null> {
  if (!sessionId) {
    return null;
  }
  try {
    const response = await fetchAuthorized(
      `/api/chat/run/active?session_id=${encodeURIComponent(sessionId)}`,
      { signal },
    );
    if (!response.ok) {
      return null;
    }
    const payload = (await response.json()) as { data?: { run?: ChatRunInfo | null } };
    const run = payload?.data?.run;
    if (!run?.run_id) {
      return null;
    }
    return { runId: run.run_id, lastSeq: typeof run.last_seq === 'number' ? run.last_seq : 0 };
  } catch {
    return null;
  }
}

function parseSeq(raw: string): number | null {
  if (!raw) {
    return null;
  }
  const value = Number.parseInt(raw, 10);
  if (!Number.isFinite(value) || value < 0) {
    return null;
  }
  return value;
}

function isAbortError(error: unknown): boolean {
  return error instanceof Error && error.name === 'AbortError';
}

function abortError(): Error {
  const error = new Error('aborted');
  error.name = 'AbortError';
  return error;
}

function delay(ms: number, signal?: AbortSignal): Promise<void> {
  return new Promise((resolve, reject) => {
    if (signal?.aborted) {
      reject(abortError());
      return;
    }
    const timer = setTimeout(() => {
      cleanup();
      resolve();
    }, ms);
    const onAbort = () => {
      cleanup();
      reject(abortError());
    };
    const cleanup = () => {
      clearTimeout(timer);
      signal?.removeEventListener('abort', onAbort);
    };
    signal?.addEventListener('abort', onAbort, { once: true });
  });
}
