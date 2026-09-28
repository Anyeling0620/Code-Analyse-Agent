import type { CostDailyTotal, QuotaToday, TraceEvent } from '../types/chat';

export function formatCost(costDaily: CostDailyTotal | null) {
  return costDaily ? `¥${costDaily.cny.toFixed(6)}` : '暂无';
}

function formatTokens(value: number | undefined) {
  return (value ?? 0).toLocaleString('zh-CN');
}

function formatCNY(value: number | undefined) {
  return `¥${(value ?? 0).toFixed(6)}`;
}

/** 缓存命中 / 未命中分别的 token 与金额。后端未返回这些字段时按 0 显示，避免出现 undefined。 */
export function formatCacheStats(costDaily: CostDailyTotal | null) {
  if (!costDaily) {
    return '缓存命中 — token (—) · 未命中 — token (—)';
  }
  return (
    `缓存命中 ${formatTokens(costDaily.cache_hit_tokens)} token (${formatCNY(costDaily.cache_hit_cny)})` +
    ` · 未命中 ${formatTokens(costDaily.cache_miss_tokens)} token (${formatCNY(costDaily.cache_miss_cny)})`
  );
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
