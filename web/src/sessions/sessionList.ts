import type { SessionListItem } from '../types/chat';

// 本模块是会话列表的**纯逻辑层**：只做列表的插入/替换/合并，不依赖 React 或 DOM，
// 因此可以被 node --experimental-strip-types 直接单测。
//
// 它存在的理由是"首问即建标签"这个需求：新会话在服务端还没有 session_id，
// 但侧栏必须立刻能看到它，否则用户在整轮生成过程中找不到自己刚问的会话。
// 占位项用 PENDING_SESSION_PREFIX 开头，等拿到真实 id 后再替换掉。

/** PENDING_SESSION_PREFIX 是"本地待定会话"的 session_id 前缀。 */
export const PENDING_SESSION_PREFIX = 'pending:';

/** isPendingSessionId 判断某个 session_id 是否为本地的待定占位。 */
export function isPendingSessionId(sessionId: string): boolean {
  return sessionId.startsWith(PENDING_SESSION_PREFIX);
}

/**
 * insertPendingSession 往列表最前面插一条本地占位会话。
 *
 * 插到最前面是因为"新会话"的语义就是最新的那条。已存在同 id 时不重复插入，
 * 保证幂等（同一轮里可能被调用多次）。
 */
export function insertPendingSession(
  current: SessionListItem[],
  pendingKey: string,
  firstMessage: string,
  userId: string,
  now: string,
): SessionListItem[] {
  if (!pendingKey) {
    return current;
  }
  if (current.some((item) => item.session_id === pendingKey)) {
    return current;
  }
  const placeholder: SessionListItem = {
    session_id: pendingKey,
    user_id: userId,
    title: '',
    summary: '',
    last_user_message: firstMessage,
    last_assistant_msg: '',
    update_at: now,
  };
  return [placeholder, ...current];
}

/**
 * replacePendingSession 把占位键换成服务端真实 id。
 *
 * 三种情况都要处理：
 *   - 列表里没有该占位项 → 原样返回（可能它本来就不存在，或被提前清掉了）；
 *   - 真实 id 已在列表里   → 丢弃占位，避免同一条会话出现两次；
 *   - 正常情况             → 原地替换，保持它在列表中的位置（最前面）。
 */
export function replacePendingSession(
  current: SessionListItem[],
  pendingKey: string,
  serverSessionId: string,
): SessionListItem[] {
  if (!pendingKey || !serverSessionId || pendingKey === serverSessionId) {
    return current;
  }
  const placeholder = current.find((item) => item.session_id === pendingKey);
  if (!placeholder) {
    return current;
  }
  const others = current.filter((item) => item.session_id !== pendingKey);
  if (others.some((item) => item.session_id === serverSessionId)) {
    return others;
  }
  return [{ ...placeholder, session_id: serverSessionId }, ...others];
}

/**
 * removePendingSession 摘掉一条本地占位会话。
 *
 * 用于两种场景：首问彻底失败（始终没拿到真实 id），或用户点了一条已失去句柄的
 * 陈旧占位——留着它只会让侧栏出现"点了没反应"的空会话。
 */
export function removePendingSession(
  current: SessionListItem[],
  pendingKey: string,
): SessionListItem[] {
  if (!pendingKey) {
    return current;
  }
  const next = current.filter((item) => item.session_id !== pendingKey);
  return next.length === current.length ? current : next;
}

/**
 * mergeRemoteWithPending 用服务端列表刷新，但保留尚未落到服务端的本地占位项。
 *
 * 这一步是必需的：refreshSessions 若直接 `setSessions(remote)`，任何一次后台刷新
 * （健康检查、成本指标、其它会话结束）都会把"正在发起首问的那个会话"从侧栏冲掉，
 * 用户会看到自己刚问的会话凭空消失。
 */
export function mergeRemoteWithPending(
  current: SessionListItem[],
  remote: SessionListItem[],
): SessionListItem[] {
  const pendingLocals = current.filter((item) => isPendingSessionId(item.session_id));
  if (pendingLocals.length === 0) {
    return remote;
  }
  const remoteIds = new Set(remote.map((item) => item.session_id));
  const keptPending = pendingLocals.filter((item) => !remoteIds.has(item.session_id));
  if (keptPending.length === 0) {
    return remote;
  }
  // 占位项放最前面：它们代表"最新发起、服务端尚未返回"的会话。
  return [...keptPending, ...remote];
}
