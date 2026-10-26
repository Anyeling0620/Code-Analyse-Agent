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
  /**
   * 服务端说"该会话已有正在生成的回答"（HTTP 409）。
   *
   * 这不算失败：本连接没有建立 SSE、也没有产生任何事件，说明这个会话本来
   * 就有一条 run 在跑。调用方应当提示用户稍候，并按 runId/lastSeq 挂载上去
   * 继续接收，而不是把它当成错误。
   */
  busy?: boolean;
  /** busy 时服务端返回的可直接展示的中文提示。 */
  busyMessage?: string;
};

/**
 * SessionBusyError 表示服务端以 409 拒绝了"再起一条 run"。
 *
 * 只在既拿不到 run_id、也回退查不到活跃 run 时才会抛给调用方；
 * 正常情况下 pumpRun 会直接转成"挂载已有 run 继续接收"。
 */
export class SessionBusyError extends Error {
  /** 服务端给的可读提示（中文），可直接展示。 */
  readonly userMessage: string;
  /** HTTP 状态码，固定 409。 */
  readonly code = 409;

  constructor(userMessage: string) {
    super(userMessage);
    this.name = 'SessionBusyError';
    this.userMessage = userMessage;
  }
}

/** 会话忙时的兜底提示：服务端没给 message 时用它。 */
export const SESSION_BUSY_MESSAGE = '该会话正在生成中，请稍候或先停止';

/**
 * 服务端明确拒绝、且重试不会有不同结果的 HTTP 错误。
 *
 * 与普通 Error 的区别在于它**跳过 pumpRun 的恢复与退避重试**：
 * 这类响应不是"连接断了"，而是"请求被拒"，重试只会把同一个错误再抛 5 次，
 * 还会顺带触发 discoverActiveRun 去查一条不该查的 run。
 */
class NonRetryableHttpError extends Error {
  readonly status: number;
  constructor(status: number) {
    super(`stream request failed: ${status}`);
    this.name = 'NonRetryableHttpError';
    this.status = status;
  }
}

type RunStreamHandlers = {
  onEvent: SSEEventHandler;
  onPhase?: (phase: RunStreamPhase) => void;
  onRunIds?: (info: RunRunIds) => void;
};

/** onRunIds 回调里带出的标识：让调用方能尽早按会话登记句柄。 */
export type RunRunIds = {
  /** 本轮 session_id（服务端回写后可能与发起时不同，例如新建会话）。 */
  sessionId: string;
  /** 本轮 run_id。 */
  runId: string;
  /**
   * true 表示这条 run **不是本次提交创建的**，而是 409 之后接管的那条已有 run。
   *
   * 调用方据此必须废弃自己刚建的占位消息（user 气泡 + assistant 占位），
   * 改把事件写进该 run 原本就有的那条 assistant 消息里——否则旧 run 的
   * 历史事件会灌进一个"这次才发现的新消息"，造成重复渲染与内容错位。
   */
  busy?: boolean;
};

type RunStreamState = {
  runId: string;
  sessionId: string;
  /**
   * 会话键锁定：sessionStorage 里活跃 run 标记用哪个键。
   * 事件里的 session_id 会改写 state.sessionId，但绝不能用来决定清哪个键，
   * 否则并行时一个会话的收尾会把另一个会话的标记清掉。
   */
  sessionKey: string;
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
  onRunIds?: (info: RunRunIds) => void;
}): Promise<RunStreamOutcome> {
  const state: RunStreamState = {
    runId: '',
    sessionId: params.sessionId,
    sessionKey: params.sessionId,
    lastSeq: 0,
    terminal: false,
  };
  const handlers: RunStreamHandlers = {
    onEvent: params.onEvent,
    onPhase: params.onPhase,
    onRunIds: params.onRunIds,
  };
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
  return pumpRun(state, handlers, firstRequest, params.signal, 'stream');
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
  onRunIds?: (info: RunRunIds) => void;
}): Promise<RunStreamOutcome> {
  const state: RunStreamState = {
    runId: '',
    sessionId: params.sessionId,
    sessionKey: params.sessionId,
    lastSeq: 0,
    terminal: false,
  };
  const handlers: RunStreamHandlers = {
    onEvent: params.onEvent,
    onPhase: params.onPhase,
    onRunIds: params.onRunIds,
  };
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
  return pumpRun(state, handlers, firstRequest, params.signal, 'resume');
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
  onRunIds?: (info: RunRunIds) => void;
}): Promise<RunStreamOutcome> {
  const lockedSessionId = params.sessionId ?? '';
  const state: RunStreamState = {
    runId: params.runId,
    sessionId: lockedSessionId,
    // 显式续传时 sessionId 已知，锁死它；缺省时退回事件里的 session_id。
    sessionKey: lockedSessionId,
    lastSeq: params.afterSeq ?? 0,
    terminal: false,
  };
  const handlers: RunStreamHandlers = {
    onEvent: params.onEvent,
    onPhase: params.onPhase,
    onRunIds: params.onRunIds,
  };
  const firstRequest = () => fetchRunEvents(state.runId, state.lastSeq, params.signal);
  return pumpRun(state, handlers, firstRequest, params.signal, 'replay');
}

// pumpRun 是唯一的接收循环：请求 → 消费事件 → 未收尾则退避重连。
// pumpRun 的入口类型。
// stream/resume 会**新建执行流**，可能被同会话串行闸门以 409 挡下；
// replay（attachRun）只是挂到一条已知 run 上，409 不该出现在这里，
// 也就不该把它的响应解读成"会话忙"。
type RunStreamEntry = 'stream' | 'resume' | 'replay';

async function pumpRun(
  state: RunStreamState,
  handlers: RunStreamHandlers,
  firstRequest: () => Promise<Response>,
  signal?: AbortSignal,
  entry: RunStreamEntry = 'replay',
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
      // 409：该会话已有正在生成的回答（后端 SessionBusy）。
      //
      // 这时**本连接没有建立 SSE**，也就没有任何事件流过：不能重试（重试还是 409），
      // 也不能把 sessionStorage 里的游标清零。正确做法是拿到那条在跑的 run，
      // 转成"挂载继续接收"。
      //
      // 只在 stream/resume 入口才这么解读：replay 不会新建 run，
      // 它的 409 与"会话忙"无关，按普通错误处理。
      const mayBeSessionBusy = entry !== 'replay' && (await isSessionBusyResponse(response));
      if (mayBeSessionBusy) {
        const busy = await resolveBusyRun(response, state);
        handlers.onPhase?.('closed');
        if (busy.runId) {
          return attachBusyRun(state, busy, handlers, signal);
        }
        // 连哪条 run 都查不到：交给调用方展示可读提示（不抛技术错误串）。
        throw new SessionBusyError(busy.message);
      }
      if (response.status === 409) {
        // 走到这里说明 409 但不像会话忙（同族业务错误，例如邮箱已注册）。
        // 这是"请求被拒"而非"连接断了"，重试无意义，也不能去查活跃 run。
        throw new NonRetryableHttpError(response.status);
      }
      if (!response.ok || !response.body) {
        throw new Error(`stream request failed: ${response.status}`);
      }

      await consumeSSE(response.body, (eventName, payload, meta) => {
        applyEvent(state, eventName, payload, meta.id, handlers);
      });

      if (state.terminal) {
        clearActiveRun(activeRunKey(state));
        handlers.onPhase?.('closed');
        return outcomeOf(state, true);
      }
    } catch (error) {
      if (isAbortError(error)) {
        // 用户主动停止：不重连，交由调用方处理中止态。
        throw error;
      }
      if (error instanceof NonRetryableHttpError || error instanceof SessionBusyError) {
        // 请求被拒：重试与"找回活跃 run"都不适用，直接交给调用方。
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
    // 锁定的键只在还没有值时补上（attachRun 缺省 sessionId 的场景）；
    // 已有值就保持不变，避免事件把"该清哪个会话的标记"改写掉。
    if (!state.sessionKey) {
      state.sessionKey = payload.session_id;
    }
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
      clearActiveRun(activeRunKey(state));
    } else {
      writeActiveRun(activeRunKey(state), { runId: state.runId, lastSeq: state.lastSeq });
    }
  }

  // 拿到 run_id 就尽早告知调用方：并行调度需要按会话登记句柄。
  if (state.runId && state.sessionId) {
    handlers.onRunIds?.({ sessionId: state.sessionId, runId: state.runId });
  }

  // 先落游标再抛事件：调用方即便在回调里抛错，重连起点也是对的。
  handlers.onEvent(eventName, payload, metaId ? { id: metaId } : {});
}

// ── "会话忙"的判定口径（刻意收紧）────────────────────────────────
//
// 409 并不是 SessionBusy 独占的：邮箱已注册（EmailRegistered）等同族业务错误
// 也用 409。若只看状态码，登录/注册路径的 409 会被误当成"该会话正在生成"，
// 从而去查活跃 run、甚至挂载到一条根本不存在的 run 上。
//
// 因此判定收敛成两条同时成立：
//   ① 本次调用必须是 chat stream / resume 入口（只有这两个入口会起 run）；
//   ② 状态码是 409，或 body.code===409 —— 且（能读到 message 时）文案命中会话忙语义。
//
// ②里的文案匹配是**与后端的耦合点**：后端文案在 common/errno.go（SessionBusy），
// 形如「该会话已有正在生成的回答，请等待完成或先停止」。这里用"包含匹配"而不是
// 全等，是为了让后端微调措辞（补标点、改语气）时前端不至于立刻失效。
// 若将来后端能在 409 body 里给出稳定的机器可读标记（如 data.reason='session_busy'），
// 应当优先认那个标记，并把这个文案匹配降级为兜底。
const SESSION_BUSY_MESSAGE_HINTS = ['正在生成', '已有正在生成的回答'];

// isSessionBusyResponse 判断一个响应是不是"会话忙"。
//
// 两个条件都要过：状态码层面是 409（HTTP 状态或 body.code），
// 且——只要能读到 message——文案命中会话忙语义。
// 这样 EmailRegistered 之类的同族 409 会被排除在外。
async function isSessionBusyResponse(response: Response): Promise<boolean> {
  const looksBusy = response.status === 409;
  if (!response.headers.get('content-type')?.includes('json')) {
    // 非 JSON 但状态是 409：没有 message 可核对，只能按状态码判。
    return looksBusy;
  }
  try {
    const payload = (await response.clone().json()) as { code?: number; message?: string };
    const codeBusy = looksBusy || payload?.code === 409;
    if (!codeBusy) {
      return false;
    }
    const text = typeof payload.message === 'string' ? payload.message : '';
    if (!text) {
      // 后端没给 message：退回到状态码判断。
      return true;
    }
    return SESSION_BUSY_MESSAGE_HINTS.some((hint) => text.includes(hint));
  } catch {
    return looksBusy;
  }
}

type BusyResolution = { runId: string; lastSeq: number; message: string };

// resolveBusyRun 在收到 409 后定位"该会话正在跑的那条 run"。
//
// 优先用响应 body 里带的 run_id（后端若给了就不必再问一次）；
// 拿不到就回退查 GET /api/chat/run/active，最后再看 sessionStorage 里的本地标记。
async function resolveBusyRun(response: Response, state: RunStreamState): Promise<BusyResolution> {
  let message = SESSION_BUSY_MESSAGE;
  let bodyRunId = '';
  let bodyLastSeq = 0;
  try {
    const payload = (await response.clone().json()) as {
      message?: string;
      data?: { run?: ChatRunInfo | null; run_id?: string; last_seq?: number };
    };
    if (payload?.message?.trim()) {
      message = payload.message.trim();
    }
    // 两种形状都读：嵌套 data.run.run_id 与扁平 data.run_id。
    // 后端 409 body 目前把 run_id **平铺**在 data.run_id（t12 已落地），
    // 嵌套形状是早期设想，保留读取以兼容两种形态。
    // **这段读取必须保留**：命中就省掉一次 GET /api/chat/run/active 往返。
    bodyRunId = payload?.data?.run?.run_id ?? payload?.data?.run_id ?? '';
    // body 通常**不**提供 last_seq，这是后端的刻意设计而不是缺口：
    // 409 发生在"建 run 之前"，此刻占用该会话的是**另一条** run，
    // 而本连接从未订阅过它，它的游标对这条连接而言没有意义（应为 0）。
    //
    // 所以这里的 0 不是"读失败"，别为了让它有值去别处推断 —— 那会造出一个
    // 错误的续接点，导致漏事件或重复回放。真实起点由下面的
    // `Math.max(bodyLastSeq, localSeq)` 决定：本地同 runId 的记录更可信。
    const rawLastSeq = payload?.data?.run?.last_seq ?? payload?.data?.last_seq;
    if (typeof rawLastSeq === 'number' && Number.isFinite(rawLastSeq) && rawLastSeq > 0) {
      bodyLastSeq = rawLastSeq;
    }
  } catch {
    // body 不是 JSON：用兜底提示即可。
  }

  if (bodyRunId) {
    // 游标取"body 与本地"的较大值：
    //   · body 若不提供 last_seq（当前常态）→ bodyLastSeq 为 0，最终取本地值；
    //   · 本地没有同 runId 的记录时 localSeq 为 0，从 0 起回放（不漏事件）。
    // 两者都只有 0 时才从头回放，这是安全侧的选择（重复可去重，漏了补不回来）。
    const local = readActiveRun(state.sessionId || state.sessionKey);
    const localSeq = local && local.runId === bodyRunId ? local.lastSeq : 0;
    return { runId: bodyRunId, lastSeq: Math.max(bodyLastSeq, localSeq), message };
  }

  // 回退：问服务端这个会话有没有正在跑的 run。
  const discovered = await discoverActiveRun(state.sessionId || state.sessionKey);
  if (discovered) {
    return { runId: discovered.runId, lastSeq: discovered.lastSeq, message };
  }

  // 最后退回本地标记（跨标签页/刷新后仍然有效）。
  const cached = readActiveRun(state.sessionId || state.sessionKey);
  if (cached) {
    return { runId: cached.runId, lastSeq: cached.lastSeq, message };
  }
  return { runId: '', lastSeq: 0, message };
}

// attachBusyRun 把"被 409 拒绝"的一次提交转成"挂到已有 run 上继续接收"。
//
// 关键：409 时没有任何事件到达过，state.lastSeq 仍是本地已知的最大游标。
// 从它之后继续订阅，服务端会回放这期间的事件，因此不会重复也不会漏。
async function attachBusyRun(
  state: RunStreamState,
  busy: BusyResolution,
  handlers: RunStreamHandlers,
  signal?: AbortSignal,
): Promise<RunStreamOutcome> {
  state.runId = busy.runId;
  state.lastSeq = Math.max(state.lastSeq, busy.lastSeq);
  state.terminal = false;
  // 通知调用方：这条 run 是"接管已有"而非"本次新建"，必须据此丢弃占位消息。
  if (state.sessionId) {
    handlers.onRunIds?.({ sessionId: state.sessionId, runId: state.runId, busy: true });
  }
  handlers.onPhase?.('reconnected');
  // 复用同一条接收循环：从 lastSeq 之后回放并跟随，重试策略与正常路径完全一致。
  const outcome = await pumpRun(state, handlers, () => fetchRunEvents(state.runId, state.lastSeq, signal), signal);
  // 标记"这次提交其实是被 409 挡下后挂上去的"，供调用方做轻提示。
  return { ...outcome, busy: true, busyMessage: busy.message };
}

// activeRunKey 决定这份活跃 run 标记记在哪个会话键上。
// 优先级：锁定的会话键 → 事件回写的 session_id。锁定键一旦有值就不再改变，
// 这样并行时 A 会话的收尾不会去清 B 会话的标记。
function activeRunKey(state: RunStreamState): string {
  return state.sessionKey || state.sessionId;
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
