import { fetchJSON } from '../api/client';
import type { ProfileForm, ProfileHistory, ServerProfile } from '../types/chat';

// 画像的读写接口。归属由登录令牌决定，请求体不带 user_id。
export async function fetchProfile(): Promise<ServerProfile> {
  return fetchJSON<ServerProfile>('/api/profile');
}

// saveProfileToServer 全量保存画像，返回服务端落库后的最终值。
export async function saveProfileToServer(profile: ProfileForm): Promise<ServerProfile> {
  return fetchJSON<ServerProfile>('/api/profile', {
    method: 'PUT',
    headers: { 'Content-Type': 'application/json; charset=utf-8' },
    body: JSON.stringify(profile),
  });
}

// fetchProfileHistory 读取最近快照，供画像弹窗展示“已保存/更新时间”和存档列表。
export async function fetchProfileHistory(limit = 5): Promise<ProfileHistory> {
  const params = new URLSearchParams({ limit: String(limit), offset: '0' });
  return fetchJSON<ProfileHistory>(`/api/profile/history?${params.toString()}`);
}
