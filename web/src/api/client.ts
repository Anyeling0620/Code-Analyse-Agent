import { clearSession, getToken } from '../auth/session';
import { getDeviceFingerprint } from '../auth/fingerprint';
import type { APIResponse } from '../types/chat';

// 后端 business code：200 表示成功，其余为业务/系统错误（与 common.OK.Code 对齐）
const OK_CODE = 200;

type FetchJSONOptions = {
  // 登录接口的 401 表示账号密码错误，而不是登录过期，需要透传服务端提示而非清除本地登录态。
  handleUnauthorized?: boolean;
};

// withAuth 给请求补上登录令牌。SSE 等直接使用 fetch 的调用点也应复用它。
export function withAuth(init?: RequestInit): RequestInit {
  const token = getToken();
  const headers = new Headers(init?.headers);
  if (token) {
    headers.set('Authorization', `Bearer ${token}`);
  }
  // 游客身份由「IP + 浏览器标识」派生，登录前后都要带上，未登录时后端才能识别同一个身份。
  const fingerprint = getDeviceFingerprint();
  if (fingerprint) {
    headers.set('X-Device-Fingerprint', fingerprint);
  }
  return { ...init, headers };
}

// LOGIN_EXPIRED_MESSAGE 是令牌失效时抛出的错误文案，登录页会据此回到未登录态。
export const LOGIN_EXPIRED_MESSAGE = '登录已失效，请重新登录';

// fetchAuthorized 发起带登录令牌的请求，并在 401 时清除本地登录态。
// 所有需要登录的请求都必须走这里——包括 SSE 这类直接用 fetch 的场景。
// 否则令牌失效后界面只会把错误显示在消息气泡里，永远不会退回登录页。
export async function fetchAuthorized(url: string, init?: RequestInit, options?: FetchJSONOptions): Promise<Response> {
  const response = await fetch(url, withAuth(init));
  if (response.status === 401 && (options?.handleUnauthorized ?? true)) {
    clearSession();
    throw new Error(LOGIN_EXPIRED_MESSAGE);
  }
  return response;
}

export async function fetchJSON<T>(url: string, init?: RequestInit, options?: FetchJSONOptions): Promise<T> {
  const response = await fetchAuthorized(url, init, options);
  if (!response.ok) {
    throw new Error((await readErrorMessage(response)) || `${url} failed: ${response.status}`);
  }
  const payload = (await response.json()) as APIResponse<T>;
  if (payload.code !== OK_CODE) {
    throw new Error(payload.message || `${url} failed`);
  }
  return payload.data;
}

// readErrorMessage 兼容两种错误体：api 层返回 message，中间件返回 msg。
async function readErrorMessage(response: Response): Promise<string> {
  try {
    const payload = (await response.json()) as { message?: string; msg?: string };
    return payload.message || payload.msg || '';
  } catch {
    return '';
  }
}

export function updateQuotaFromHeaders(response: Response) {
  const limit = response.headers.get('X-Quota-Daily-Limit');
  const used = response.headers.get('X-Quota-Daily-Used');
  if (!limit || !used) {
    return;
  }
}
