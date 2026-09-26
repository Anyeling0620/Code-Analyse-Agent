import type { InterruptEvent } from '../InterruptModal';

export type SkillLevel = '零基础' | '入门' | '熟悉';
export type GoalType = '补基础' | '做项目' | '学Agent';
export type CurrentStage = '学习中' | '开发中' | '联调收尾' | '复盘中';

export type ProfileForm = {
  user_type: string;
  skill_level: SkillLevel;
  goal_type: GoalType;
  purchased_courses: string[];
  current_topic: string;
  current_stage: CurrentStage;
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
};

export type ChatResult = {
  answer: string;
  session_id: string;
  used_tools: string[];
};

export type StreamPayload = {
  type: string;
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

export type CostDailyTotal = {
  date: string;
  cny: number;
};

export type QuotaToday = {
  user_id: string;
  plan: string;
  date: string;
  used: number;
  limit: number;
};
