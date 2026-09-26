import type { APIResponse } from '../types/chat';

// 后端 business code：200 表示成功，其余为业务/系统错误（与 common.OK.Code 对齐）
const OK_CODE = 200;

export async function fetchJSON<T>(url: string, init?: RequestInit): Promise<T> {
  const response = await fetch(url, init);
  if (!response.ok) {
    throw new Error(`${url} failed: ${response.status}`);
  }
  const payload = (await response.json()) as APIResponse<T>;
  if (payload.code !== OK_CODE) {
    throw new Error(payload.message || `${url} failed`);
  }
  return payload.data;
}

export function updateQuotaFromHeaders(response: Response) {
  const limit = response.headers.get('X-Quota-Daily-Limit');
  const used = response.headers.get('X-Quota-Daily-Used');
  if (!limit || !used) {
    return;
  }
}
