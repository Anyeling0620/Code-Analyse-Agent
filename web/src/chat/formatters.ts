import type { CostDailyTotal, QuotaToday, TraceEvent } from '../types/chat';

export function formatCost(costDaily: CostDailyTotal | null) {
  return costDaily ? `¥${costDaily.cny.toFixed(6)}` : '暂无';
}

export function formatQuota(quota: QuotaToday | null) {
  return quota ? `${quota.used}/${quota.limit}` : '暂无';
}

export function formatTime(value: string) {
  if (!value) {
    return '';
  }
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) {
    return value;
  }
  return date.toLocaleString('zh-CN', { month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit' });
}

export function formatTraceDetail(event: TraceEvent) {
  if (event.detail) {
    return event.detail;
  }
  if (event.stage.startsWith('tool_call:')) {
    return '已发起调用';
  }
  if (event.stage.startsWith('tool_result:')) {
    return '已返回结果';
  }
  return '处理中';
}
