import type { InterruptEvent } from '../InterruptModal';

// 以下三个联合类型只作为输入建议（datalist）的候选值保留，
// 画像字段本身允许用户自由填写，因此 ProfileForm 里统一是 string。
export type SkillLevel = '零基础' | '入门' | '熟悉';
export type GoalType = '补基础' | '做项目' | '学Agent';
export type CurrentStage = '学习中' | '开发中' | '联调收尾' | '复盘中';

export type ProfileForm = {
  user_type: string;
  skill_level: string;
  goal_type: string;
  description: string;
  current_topic: string;
  current_stage: string;
};

// ServerProfile 是后端返回的画像（service/dto.Profile）。
// 比 ProfileForm 多 user_id/updated_at 等服务端字段，回填时只取前端认识的几个。
export type ServerProfile = {
  user_id?: string;
  user_type?: string;
  skill_level?: string;
  goal_type?: string;
  description?: string;
  current_topic?: string;
  current_stage?: string;
};

export type ToolTrace = {
  id: string;
  callId: string;
  name: string;
  arguments: string;
  result: string;
  status: 'calling' | 'done';
};

export type MessageSegment =
    | { type: 'text'; id: string; content: string; source?: 'progress' | 'delta' }
    | { type: 'tool'; id: string; tool: ToolTrace }
    | { type: 'error'; id: string; content: string };

export type ChatMessage = {
  id: string;
  role: 'user' | 'assistant';
  content: string;
  tools: string[];
  toolEvents: ToolTrace[];
  segments: MessageSegment[];
  traceEvents: TraceEvent[];
  status: 'idle' | 'streaming' | 'error' | 'done';
  /** 历史消息的落库时间；实时流新增的消息没有该字段。 */
  createdAt?: string;
};

export type ChatResult = {
  answer: string;
  session_id: string;
  used_tools: string[];
  // 后端在 done 事件里回带服务端最终画像，前端据此回写本地状态（含描述）。
  profile?: ServerProfile;
};

export type StreamPayload = {
  type: string;
  // run_id / seq 用于断连续传：seq 对应后端 chat_run_events.id，
  // 重连时按 Last-Event-ID 只补 seq 之后的事件。
  run_id?: string;
  seq?: number;
  trace_id?: string;
  session_id?: string;
  tool_name?: string;
  tool_call_id?: string;
  tool_arguments?: string;
  tool_result?: string;
  delta?: string;
  message?: string;
  stage?: string;
  detail?: string;
  elapsed_ms?: number;
  timestamp?: string;
  content_kind?: string;
  render_mode?: string;
  visibility?: string;
  result?: ChatResult;
  pending_approval_id?: string;
  pending_command?: string;
  pending_risk_reason?: string;
  pending_risk_level?: string;
  pending_workdir?: string;
  pending_timeout_sec?: number;
};

export type SessionListItem = {
  session_id: string;
  user_id: string;
  summary: string;
  last_user_message: string;
  last_assistant_msg: string;
  update_at: string;
  // 后端 SessionContext 会带上项目根/项目名；列表接口不保证返回，故为可选。
  current_project_root?: string;
  current_project_name?: string;
};

export type ChatMessageRecord = {
  id: number;
  session_id: string;
  user_id: string;
  role: 'user' | 'assistant';
  content: string;
  render_events?: StreamPayload[];
  created_at: string;
};

// ChatRunInfo / ChatRunActiveResp 对应后端 run 状态机查询接口。
export type ChatRunInfo = {
  run_id: string;
  session_id: string;
  status: 'running' | 'interrupted' | 'done' | 'failed' | string;
  degraded: boolean;
  question: string;
  pending_approval_id: string;
  project_root: string;
  project_name: string;
  last_seq: number;
  started_at: string;
  ended_at: string;
};

export type ChatRunActiveResp = {
  run: ChatRunInfo | null;
};

export type PendingInterruptEvent = InterruptEvent & { assistant_message_id: string };

export type PaginatedSessionList = {
  list: SessionListItem[];
  total: number;
  page: number;
  limit: number;
  has_more: boolean;
};

export type SessionDetail = {
  session: SessionListItem;
  list: ChatMessageRecord[];
  total: number;
  page: number;
  limit: number;
  has_more: boolean;
};

export type TraceEvent = {
  id: string;
  stage: string;
  detail: string;
  elapsedMs?: number;
  timestamp?: string;
};

export type APIResponse<T> = {
  code: number;
  message: string;
  data: T;
  trace_id?: string;
};

export type LoginResult = {
  token: string;
  user_id: string;
  plan: string;
};

// VersionInfo 是 /api/version 的返回。登录前唯一可访问的接口，
// 这里带出的 guest_login 决定登录页是否展示游客入口。
export type VersionInfo = {
  app_name: string;
  version: string;
  go_version: string;
  guest_login: boolean;
};

export type CostDailyTotal = {
  date: string;
  cny: number;
  prompt_tokens: number;
  completion_tokens: number;
  cache_hit_tokens: number;
  cache_miss_tokens: number;
  cache_hit_cny: number;
  cache_miss_cny: number;
};

export type QuotaToday = {
  user_id: string;
  plan: string;
  date: string;
  used: number;
  limit: number;
};

// ShareCreateResult 是创建只读分享后后端返回的分享凭据。
export type ShareCreateResult = {
  share_token: string;
  share_path: string;
  created_at: string;
  expires_at: string | null;
};

// SharedSessionDetail 是只读分享页拿到的快照内容（按令牌读取，无需登录）。
export type SharedSessionDetail = SessionDetail & {
  shared_at: string;
  expires_at: string | null;
};
