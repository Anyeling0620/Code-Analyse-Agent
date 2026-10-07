import { initialProfile } from '../constants/profile';
import type { ProfileForm, ServerProfile } from '../types/chat';

// 画像按 user_id 分键缓存在本地：同一台机器换账号登录不会互相串档，刷新页面也不会
// 退回默认值后被再次提交（默认值覆盖服务端画像）。
const STORAGE_PREFIX = 'analyse.profile.v1.';

function storageKey(userId: string): string {
  return `${STORAGE_PREFIX}${userId}`;
}

// readString 只接受字符串，顺带丢掉旧版本残留的字段（例如 purchased_courses 数组）。
function readString(value: unknown, fallback: string): string {
  return typeof value === 'string' ? value : fallback;
}

export function normalizeProfile(profile: ProfileForm): ProfileForm {
  return {
    user_type: profile.user_type.trim(),
    skill_level: profile.skill_level.trim(),
    goal_type: profile.goal_type.trim(),
    description: profile.description.trim(),
    current_topic: profile.current_topic.trim(),
    current_stage: profile.current_stage.trim(),
  };
}

export function loadProfile(userId: string): ProfileForm {
  if (!userId) {
    return initialProfile;
  }
  try {
    const raw = window.localStorage.getItem(storageKey(userId));
    if (!raw) {
      return initialProfile;
    }
    const parsed = JSON.parse(raw) as Partial<ProfileForm>;
    return normalizeProfile({
      user_type: readString(parsed.user_type, initialProfile.user_type),
      skill_level: readString(parsed.skill_level, initialProfile.skill_level),
      goal_type: readString(parsed.goal_type, initialProfile.goal_type),
      description: readString(parsed.description, initialProfile.description),
      current_topic: readString(parsed.current_topic, initialProfile.current_topic),
      current_stage: readString(parsed.current_stage, initialProfile.current_stage),
    });
  } catch {
    // 解析失败按没有缓存处理：画像可以重填，不能因此打断登录。
    return initialProfile;
  }
}

export function saveProfile(userId: string, profile: ProfileForm): void {
  if (!userId) {
    return;
  }
  try {
    window.localStorage.setItem(storageKey(userId), JSON.stringify(normalizeProfile(profile)));
  } catch {
    // 隐私模式或配额满：只影响下次刷新时的回填，不阻塞使用。
  }
}

// profileFromServer 用服务端返回的画像覆盖本地值。服务端是权威来源（它按 user_id 存库），
// 服务端为空的字段保留本地已有值，避免一次空响应把用户填的内容抹掉。
export function profileFromServer(current: ProfileForm, server: ServerProfile | null | undefined): ProfileForm {
  if (!server) {
    return current;
  }
  return normalizeProfile({
    user_type: server.user_type || current.user_type,
    skill_level: server.skill_level || current.skill_level,
    goal_type: server.goal_type || current.goal_type,
    description: server.description || current.description,
    current_topic: server.current_topic || current.current_topic,
    current_stage: server.current_stage || current.current_stage,
  });
}
