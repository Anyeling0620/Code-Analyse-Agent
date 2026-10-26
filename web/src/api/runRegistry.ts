// RunRegistry 是"会话维度的流式句柄表"。
//
// 背景：跨会话并行之前，页面用单一 isStreaming 布尔 + 单一 AbortController
// 表达"整页在流式"。那套状态无法区分会话，导致"生成中不能切会话"，
// 而且先结束的那条流会把整页状态清空，误判另一条仍在跑的会话。
//
// 这里把状态键从"页面"换成"会话"：每个会话独立持有 runId / lastSeq /
// assistantId / AbortController。切走会话只是 detach（保活后台接收），
// 只有用户主动点「停止」才 abort。
//
// 本文件不依赖 DOM 与 React，可被 node --experimental-strip-types 直接单测。

/** run 在服务端的生命周期状态。 */
export type RunLifecycle = 'running' | 'interrupted';

/** 句柄状态：服务端生命周期 + 前端本地的收尾态。 */
export type RunHandleStatus = RunLifecycle | 'streaming' | 'done' | 'error';

export type RunHandle = {
  /** 归属会话。首个事件带回服务端 session_id 后，这里可能被 rekey 更新。 */
  sessionId: string;
  /** run_id：拿到 ready 事件后才有值；空串表示还没握上手。 */
  runId: string;
  /** 已渲染到的事件游标，续传起点。 */
  lastSeq: number;
  /** 该会话当前这条 assistant 消息的本地 id。 */
  assistantId: string;
  status: RunHandleStatus;
  controller: AbortController;
  /** 传输层是否仍在跟随（SSE 连接存活）。detach/abort 会把它置 false，执行不受影响。 */
  live: boolean;
  /** 用户是否已经离开该会话视图（离开即只更新缓存、不更新视口）。 */
  detached: boolean;
  /** 最近一次活动时间，用于 LRU 淘汰。 */
  updatedAt: number;
  /**
   * 已被新的一轮接管（审批恢复沿用同一条 run 时会重新登记句柄）。
   * 一旦置位，这个旧句柄的后续增量一律丢弃：它等待的那条流已经收尾，
   * 消息归属权交给了接管者。
   */
  superseded: boolean;
};

/** 同时保活的会话句柄上限。浏览器同域 SSE 上限是 6，这里留一点余量。 */
export const MAX_ACTIVE_RUNS = 8;

export type BeginHandleParams = {
  sessionId: string;
  runId?: string;
  assistantId: string;
  status?: RunHandleStatus;
  controller?: AbortController;
  lastSeq?: number;
  /**
   * 接管：即便该会话已有句柄也强制换成新句柄（审批恢复走这条路）。
   * 旧句柄被标记 superseded 并断开传输层，它的 finally 不会再删掉新句柄。
   */
  takeover?: boolean;
};

/**
 * RunRegistry 只做句柄记账，不碰网络与 React 状态。
 *
 * 变更通知走 subscribe/notify：App 用一个 version 计数订阅它，
 * 侧栏与顶栏据此刷新"哪些会话正在生成"。
 */
export class RunRegistry {
  private handles = new Map<string, RunHandle>();
  private listeners = new Set<() => void>();
  private version = 0;

  // ── 订阅 ─────────────────────────────────────────────────────
  subscribe(listener: () => void): () => void {
    this.listeners.add(listener);
    return () => {
      this.listeners.delete(listener);
    };
  }

  notify(): void {
    this.version += 1;
    for (const listener of Array.from(this.listeners)) {
      listener();
    }
  }

  /** 单调递增的版本号。React 侧用它作 useSyncExternalStore 的快照值。 */
  getVersion(): number {
    return this.version;
  }

  // ── 查询 ─────────────────────────────────────────────────────
  get(sessionId: string): RunHandle | undefined {
    return sessionId ? this.handles.get(sessionId) : undefined;
  }

  /** 该会话是否有活跃 run（running / interrupted）。同会话串行的唯一判据。 */
  isBusy(sessionId: string): boolean {
    return this.get(sessionId) !== undefined;
  }

  size(): number {
    return this.handles.size;
  }

  /** 正在后台生成的会话 id 列表，供侧栏渲染运行态指示。 */
  busySessions(): string[] {
    return Array.from(this.handles.keys());
  }

  // ── 写入 ─────────────────────────────────────────────────────
  /**
   * 登记一条新 run。
   *
   * 同一个 runId 重复登记是幂等的（重新挂载时复用原句柄）；
   * 同一会话出现另一条 run 属于违反"同会话串行"，直接抛错（双保险，
   * 正常情况下 handleSubmit 已经用 isBusy 拦过一次）。
   */
  begin(params: BeginHandleParams): RunHandle {
    const { sessionId } = params;
    if (!sessionId) {
      throw new Error('runRegistry.begin: sessionId is required');
    }
    const existing = this.handles.get(sessionId);
    if (existing && !params.takeover) {
      if (params.runId && existing.runId === params.runId) {
        return existing;
      }
      throw new Error(`runRegistry.begin: 会话 ${sessionId} 已有正在生成的回复，不允许并发起第二条 run`);
    }
    if (existing) {
      // 接管：旧句柄的流已经收尾（审批中断），把它的尾巴按住，
      // 免得它的 finally 把新登记的句柄一起释放掉。
      existing.superseded = true;
      try {
        existing.controller.abort();
      } catch {
        // 已经 abort 过，忽略。
      }
    }

    const handle: RunHandle = {
      sessionId,
      runId: params.runId ?? '',
      lastSeq: params.lastSeq ?? 0,
      assistantId: params.assistantId,
      status: params.status ?? 'streaming',
      controller: params.controller ?? new AbortController(),
      live: true,
      detached: false,
      updatedAt: now(),
      superseded: false,
    };
    this.handles.set(sessionId, handle);
    this.enforceLimit();
    this.notify();
    return handle;
  }

  /**
   * 会话 id 迁移：发起时本地可能还没有 session_id（新建会话），
   * 服务端在首个 session 事件里给回来之后，把句柄搬到真正的键上。
   */
  rekey(fromSessionId: string, toSessionId: string): RunHandle | undefined {
    if (!fromSessionId || !toSessionId || fromSessionId === toSessionId) {
      return this.handles.get(fromSessionId || toSessionId);
    }
    const handle = this.handles.get(fromSessionId);
    if (!handle) {
      return this.handles.get(toSessionId);
    }
    if (this.handles.has(toSessionId)) {
      // 目标键已被别的 run 占用：保留原句柄，交给上层按 runId 去重。
      return handle;
    }
    this.handles.delete(fromSessionId);
    handle.sessionId = toSessionId;
    handle.updatedAt = now();
    this.handles.set(toSessionId, handle);
    this.notify();
    return handle;
  }

  /** 推进游标。只允许单调前进，重连回放落后事件时不会把起点拉回去。 */
  touch(handle: RunHandle, lastSeq: number): void {
    if (!Number.isFinite(lastSeq) || lastSeq <= handle.lastSeq) {
      return;
    }
    handle.lastSeq = lastSeq;
    handle.updatedAt = now();
    this.notify();
  }

  setRunId(handle: RunHandle, runId: string): void {
    if (!runId || handle.runId === runId) {
      return;
    }
    handle.runId = runId;
    handle.updatedAt = now();
    this.notify();
  }

  setStatus(handle: RunHandle, status: RunHandleStatus): void {
    if (handle.status === status) {
      return;
    }
    handle.status = status;
    handle.updatedAt = now();
    this.notify();
  }

  /** 主动断开这条 run 的传输层（用户点停止、登出、或 LRU 淘汰最久未看的会话）。 */
  abort(handle: RunHandle): void {
    handle.live = false;
    try {
      handle.controller.abort();
    } catch {
      // controller 可能已经被别处 abort 过，忽略。
    }
    this.notify();
  }

  /** 收尾：只释放自己的句柄。并行场景下不会误删其他会话的状态。 */
  finish(handle: RunHandle): void {
    if (this.handles.get(handle.sessionId) === handle) {
      this.handles.delete(handle.sessionId);
      this.notify();
    }
  }

  abortAll(): void {
    for (const handle of Array.from(this.handles.values())) {
      this.abort(handle);
    }
    this.handles.clear();
    this.notify();
  }

  /**
   * 切走会话 = detach：**不清句柄、不 abort、不断连接**。
   * 首版保留 SSE 连接（并发 1–3 条远低于浏览器 6 连接上限），
   * 切回来时不需要重连，也就没有"abort → 重连 → 重复事件"这一类风险。
   * detached 只表达"用户已离开该会话视图"：事件照收，但只更新缓存、不更新视口。
   */
  markDetached(handle: RunHandle): void {
    if (handle.detached) {
      return;
    }
    handle.detached = true;
    handle.updatedAt = now();
    this.notify();
  }

  /** 切成"用户正在看这个会话"：继续跟着事件更新视口。 */
  markAttached(handle: RunHandle): void {
    if (!handle.detached) {
      return;
    }
    handle.detached = false;
    handle.updatedAt = now();
    this.notify();
  }

  /**
   * 超出上限时淘汰最久没被查看的会话：先 abort 它的传输层，
   * 后端 run 照旧跑完并落库，用户切回时仍可凭 run_id + lastSeq 续上。
   */
  private enforceLimit(): void {
    while (this.handles.size > MAX_ACTIVE_RUNS) {
      const lru = this.leastRecentlyUsed();
      if (!lru) {
        return;
      }
      this.abort(lru);
      this.handles.delete(lru.sessionId);
    }
  }

  private leastRecentlyUsed(): RunHandle | undefined {
    let oldest: RunHandle | undefined;
    for (const handle of this.handles.values()) {
      if (!oldest || handle.updatedAt < oldest.updatedAt) {
        oldest = handle;
      }
    }
    return oldest;
  }
}

function now(): number {
  return Date.now();
}
