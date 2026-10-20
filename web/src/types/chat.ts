import type { InterruptEvent } from '../InterruptModal';

// 以下三个联合类型只作为输入建议（datalist）的候选值保留，
// 画像字段本身允许用户自由填写，因此 ProfileForm 里统一是 string。
export type SkillLevel = '零基础' | '入门' | '熟悉' | '熟练' | '专家';
export type GoalType = '补基础' | '做项目' | '学Agent' | '求职面试' | '技术调研' | '其他';
export type CurrentStage = '学习中' | '开发中' | '联调收尾' | '复盘中' | '已上线维护' | '规划中';

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
  updated_at?: string;
};

// ProfileRevision 是 /api/profile/history 返回的一条画像快照。
export type ProfileRevision = ProfileForm & {
  id: number;
  user_id: string;
  source: 'api' | 'chat' | 'legacy' | string;
  updated_at: string;
};

export type ProfileHistory = {
  items: ProfileRevision[];
  limit: number;
  offset: number;
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
  /** 后端 chat_messages.id；轻量历史消息按条懒加载调用事件时使用。 */
  recordId?: number;
  /** 该消息在服务端是否存有调用事件（轻量历史接口返回）。 */
  hasRenderEvents?: boolean;
  /** 调用事件是否已在本地回放完成；没有事件时也视为已加载。 */
  eventsLoaded?: boolean;
  /** 调用事件正在请求中。 */
  eventsLoading?: boolean;
  /** 轻量探测：这条回答调用过 repo_analyzer，未加载事件也能显示导出。 */
  hasProjectAnalysis?: boolean;
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
  // AI 根据首轮问答生成的标题；老会话没有该字段或为空时回退到用户消息/摘要。
  title?: string;
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
  /** 轻量历史接口：库里是否存有调用事件，需要时再按条请求完整事件。 */
  has_render_events?: boolean;
  /** 轻量探测：调用过 repo_analyzer，用于导出按钮判定。 */
  has_project_analysis?: boolean;
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
  /** 轻量历史接口不再做 COUNT(*)，因此可能省略；分页以 has_more 为准。 */
  total?: number;
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
// 这里带出的 email_register / password_reset 决定登录页展示哪些入口。
export type VersionInfo = {
  app_name: string;
  version: string;
  go_version: string;
  email_register: boolean;
  password_reset: boolean;
};

// SendCodeResult 是 /api/auth/email/code 的返回：验证码有效期与重发间隔（秒）。
export type SendCodeResult = {
  expires_in: number;
  resend_after: number;
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

// CostTotal 是消耗总览的汇总行，金额单位 CNY、token 为区间内累加。
export type CostTotal = {
  cny: number;
  calls: number;
  sessions: number;
  tools: number;
  models: number;
  tokens: number;
  prompt_tokens: number;
  completion_tokens: number;
  cache_hit_tokens: number;
  cache_miss_tokens: number;
  cache_hit_cny: number;
  cache_miss_cny: number;
};

// CostBreakdownItem 是工具/模型/会话三个维度的通用聚合行，key 为对应主键。
export type CostBreakdownItem = {
  key: string;
  label: string;
  calls: number;
  tokens: number;
  cny: number;
  share: number;
  last_at: string;
};

// CostDailyPoint 是区间内单日的消耗，服务端会把没有数据的日子补 0。
export type CostDailyPoint = {
  date: string;
  calls: number;
  tokens: number;
  cny: number;
};

// CostOverview 是 /api/cost/overview 的返回，只描述当前登录用户的数据。
export type CostOverview = {
  from: string;
  to: string;
  total: CostTotal;
  by_tool: CostBreakdownItem[];
  by_model: CostBreakdownItem[];
  by_session: CostBreakdownItem[];
  daily: CostDailyPoint[];
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
