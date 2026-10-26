import type { ChatMessage } from '../types/chat';

// 本模块是跨会话并行流式的**纯逻辑层**：只做消息数组的合并与投影，
// 不依赖 React、DOM 或网络，因此可以被 node --experimental-strip-types 直接单测。
//
// 它承担两条结构性保证：
//   1. 后台会话的增量只在它自己那一轮的数组里累积（按 run 分桶）；
//   2. 投影到视口时按**消息 id** 过滤，所以即便调用方传错会话，
//      也不可能把 A 会话的增量写到 B 会话的消息上。

export type AssistantUpdate = (item: ChatMessage) => ChatMessage;
export type AssistantUpdates = Array<AssistantUpdate>;
/** 一条 run 的待应用增量：消息 id → 更新函数列表。 */
export type RunUpdateBuffer = Map<string, AssistantUpdates>;

/**
 * applyBufferUpdates 把一批增量同步应用到消息数组上。
 *
 * 只作用于 id 命中 buffer 的消息，其余原样返回（并保持引用不变，
 * 这样 React 侧不会因为无关消息而重渲染）。
 */
export function applyBufferUpdates(items: ChatMessage[], buffer: RunUpdateBuffer): ChatMessage[] {
  if (buffer.size === 0) {
    return items;
  }
  let touched = false;
  const next = items.map((item) => {
    const updates = buffer.get(item.id);
    if (!updates || updates.length === 0) {
      return item;
    }
    touched = true;
    return updates.reduce((acc, update) => update(acc), item);
  });
  return touched ? next : items;
}

/**
 * projectRunUpdates 计算"某条 run 的增量里，有多少能落到当前视口"。
 *
 * 不在视口里的消息被丢弃（它的真身在 live 数组里已经更新过），
 * 这是跨会话不串台的最后一道闸门。
 */
export function projectRunUpdates(items: ChatMessage[], buffer: RunUpdateBuffer): RunUpdateBuffer {
  if (buffer.size === 0) {
    return new Map();
  }
  const inView = new Set(items.map((item) => item.id));
  const projected: RunUpdateBuffer = new Map();
  for (const [messageId, updates] of buffer) {
    if (!inView.has(messageId) || updates.length === 0) {
      continue;
    }
    projected.set(messageId, updates);
  }
  return projected;
}

/** mergeRunUpdateBuffers 把多路增量合并成一份（同一消息按发生顺序串联）。 */
export function mergeRunUpdateBuffers(buffers: RunUpdateBuffer[]): RunUpdateBuffer {
  const merged: RunUpdateBuffer = new Map();
  for (const buffer of buffers) {
    for (const [messageId, updates] of buffer) {
      const existing = merged.get(messageId);
      merged.set(messageId, existing ? [...existing, ...updates] : [...updates]);
    }
  }
  return merged;
}

/**
 * appendUpdates 就地把一条增量推进 buffer（保证同一消息的更新有序）。
 */
export function appendUpdates(buffer: RunUpdateBuffer, messageId: string, update: AssistantUpdate): void {
  const existing = buffer.get(messageId);
  if (existing) {
    existing.push(update);
    return;
  }
  buffer.set(messageId, [update]);
}

/**
 * mergeLiveMessages 把 overlay 的内容覆盖到 base 上（同 id 取 overlay，多的追加）。
 *
 * 切回正在生成的会话时用它：base 是缓存/服务端历史，overlay 是这一轮的最新状态。
 * 同 id 覆盖而不是拼接，就是"不重复渲染已收到内容"的实现方式。
 */
export function mergeLiveMessages(base: ChatMessage[], overlay: ChatMessage[]): ChatMessage[] {
  if (overlay.length === 0) {
    return base;
  }
  const overlayById = new Map(overlay.map((item) => [item.id, item]));
  const seen = new Set<string>();
  const merged: ChatMessage[] = [];
  let changed = false;

  for (const item of base) {
    const fresh = overlayById.get(item.id);
    seen.add(item.id);
    if (fresh) {
      if (fresh !== item) {
        changed = true;
        merged.push(fresh);
      } else {
        merged.push(item);
      }
      continue;
    }
    merged.push(item);
  }
  for (const item of overlay) {
    if (!seen.has(item.id)) {
      changed = true;
      merged.push(item);
    }
  }
  return changed ? merged : base;
}

// ── 事件路由 ────────────────────────────────────────────────────────────
//
// 事件该写进哪条消息，必须**从句柄读**，不能用发起时捕获的闭包常量。
// 原因：409 接管（adoptBusyRun）会把 handle.assistantId 改指该 run 原本那条
// 历史消息；若调用点还拿着"本次新建的占位 id"，接管之后的事件会写进一个
// 已经被移除的消息，等于把整轮输出丢掉（且不报错，最难发现的一类缺陷）。

/** 解析一条流事件应当落到的消息 id：句柄优先，闭包常量只作兜底。 */
export function resolveTargetMessageId(handle: { assistantId?: string }, fallback = ''): string {
  return (handle.assistantId || fallback || '').trim();
}

/**
 * 判断一条事件是否属于该句柄的 run。
 *
 * - payload 没带 run_id：无法判断，放行（历史版本后端可能不发）。
 * - 句柄还没有 runId：接受并采纳，这是首个能确认归属的帧。
 * - 两者都有且不同：丢弃——那是别的 run 串进来的尾巴。
 */
export function shouldAcceptRunEvent(
    handleRunId: string,
    payloadRunId?: string,
): { accept: boolean; adoptRunId?: string } {
  if (!payloadRunId) {
    return { accept: true };
  }
  if (!handleRunId) {
    return { accept: true, adoptRunId: payloadRunId };
  }
  return { accept: payloadRunId === handleRunId };
}
