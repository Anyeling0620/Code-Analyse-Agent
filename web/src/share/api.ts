import { fetchJSON } from '../api/client';
import type { ShareCreateResult, SharedSessionDetail } from '../types/chat';

const JSON_HEADERS = { 'Content-Type': 'application/json; charset=utf-8' };

// createShareLink 为当前登录用户自己的会话生成一条只读分享链接。
export function createShareLink(sessionId: string) {
  return fetchJSON<ShareCreateResult>('/api/sessions/share', {
    method: 'POST',
    headers: JSON_HEADERS,
    body: JSON.stringify({ session_id: sessionId }),
  });
}

// fetchSharedSession 按令牌读取分享快照。该接口在鉴权白名单里，未登录也能调用。
export function fetchSharedSession(token: string) {
  return fetchJSON<SharedSessionDetail>(`/api/sessions/shared/info?share_token=${encodeURIComponent(token)}`);
}

// revokeShare 撤销当前用户某个会话下所有未失效的分享。
export function revokeShare(sessionId: string) {
  return fetchJSON<null>(`/api/sessions/share?session_id=${encodeURIComponent(sessionId)}`, { method: 'DELETE' });
}
