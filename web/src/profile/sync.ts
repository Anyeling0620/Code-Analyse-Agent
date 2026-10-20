import type { ProfileForm } from '../types/chat';

// ProfileSyncStatus 描述本次登录后“服务端画像是否已成功回读”。
// pending/failed 都不能把本地值当成用户画像提交：服务端对请求里的 profile 是全量覆盖，
// 本地默认值一旦发出去，会把用户真正保存过的画像改写掉。
export type ProfileSyncStatus = 'pending' | 'ready' | 'failed';

// profileForChat 只在服务端画像成功同步后，才允许随对话提交画像。
// 其它状态一律发 null，让服务端按 user_id 使用库中当前值。
export function profileForChat(profile: ProfileForm, status: ProfileSyncStatus): ProfileForm | null {
  return status === 'ready' ? profile : null;
}

// normalizeProfileUpdatedAt 把 Go time.Time 的零值（0001-01-01T00:00:00Z）
// 视为“还没有保存过”，避免界面显示 0001/01/01。
export function normalizeProfileUpdatedAt(value: string | null | undefined): string {
  if (!value) {
    return '';
  }
  const date = new Date(value);
  if (Number.isNaN(date.getTime()) || date.getUTCFullYear() <= 1) {
    return '';
  }
  return value;
}
