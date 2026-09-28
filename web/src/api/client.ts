import { clearSession, getToken } from '../auth/session';
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
  return { ...init, headers };
}

export async function fetchJSON<T>(url: string, init?: RequestInit, options?: FetchJSONOptions): Promise<T> {
  const handleUnauthorized = options?.handleUnauthorized ?? true;
  const response = await fetch(url, withAuth(init));
  if (response.status === 401 && handleUnauthorized) {
    clearSession();
    throw new Error('登录已失效，请重新登录');
  }
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
