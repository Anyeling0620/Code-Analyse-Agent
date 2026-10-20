import { useEffect, useMemo, useState } from 'react';
import { fetchJSON } from '../api/client';
import type { CostBreakdownItem, CostOverview } from '../types/chat';
import { CloseIcon } from '../ui/icons';

type RangeKey = '7d' | '30d' | '90d' | 'month';
type TabKey = 'tool' | 'session' | 'model' | 'trend';

const ranges: { key: RangeKey; label: string }[] = [
  { key: '7d', label: '近 7 天' },
  { key: '30d', label: '近 30 天' },
  { key: '90d', label: '近 90 天' },
  { key: 'month', label: '本月' },
];

const tabs: { key: TabKey; label: string }[] = [
  { key: 'tool', label: '工具' },
  { key: 'session', label: '会话' },
  { key: 'model', label: '模型' },
  { key: 'trend', label: '趋势' },
];

export function CostModal({ onClose }: { onClose: () => void }) {
  const [range, setRange] = useState<RangeKey>('30d');
  const [tab, setTab] = useState<TabKey>('tool');
  const [overview, setOverview] = useState<CostOverview | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');

  // ESC 关闭：与 ProfileModal 一致，不抢焦点、不做 focus trap。
  useEffect(() => {
    function handleKeyDown(event: KeyboardEvent) {
      if (event.key === 'Escape') {
        onClose();
      }
    }
    window.addEventListener('keydown', handleKeyDown);
    return () => window.removeEventListener('keydown', handleKeyDown);
  }, [onClose]);

  // 区间变化就重新请求一次；cancelled 用来丢弃过期响应，避免快速切换时旧数据覆盖新数据。
  useEffect(() => {
    let cancelled = false;
    const { from, to } = rangeDates(range);
    setLoading(true);
    setError('');
    setOverview(null);
    fetchJSON<CostOverview>(`/api/cost/overview?from=${from}&to=${to}`)
      .then((data) => {
        if (!cancelled) {
          setOverview(data);
        }
      })
      .catch((caught: unknown) => {
        if (!cancelled) {
          setError(caught instanceof Error && caught.message ? normalizeError(caught.message) : '消耗数据加载失败，请重试');
        }
      })
      .finally(() => {
        if (!cancelled) {
          setLoading(false);
        }
      });
    return () => {
      cancelled = true;
    };
  }, [range]);

  const rangeText = useMemo(() => {
    if (overview) {
      return `统计区间 ${overview.from} 至 ${overview.to} · 金额单位 CNY`;
    }
    const { from, to } = rangeDates(range);
    return `统计区间 ${from} 至 ${to} · 金额单位 CNY`;
  }, [overview, range]);

  return (
    <div className="modal-backdrop" role="presentation" onClick={onClose}>
      <section
        className="modal-card cost-modal"
        role="dialog"
        aria-modal="true"
        aria-label="消耗明细"
        onClick={(event) => event.stopPropagation()}
      >
        <span className="modal-tab" aria-hidden="true">COST</span>

        <header className="modal-header">
          <div>
            <h2>消耗明细</h2>
            <p className="cost-subtitle">{rangeText}</p>
          </div>
          <button className="icon-button" type="button" onClick={onClose} aria-label="关闭消耗弹窗">
            <CloseIcon size={17} />
          </button>
        </header>

        <div className="cost-range" role="group" aria-label="统计区间">
          {ranges.map((item) => (
            <button
              key={item.key}
              type="button"
              className={range === item.key ? 'cost-range-btn is-active' : 'cost-range-btn'}
              aria-pressed={range === item.key}
              onClick={() => setRange(item.key)}
            >
              {item.label}
            </button>
          ))}
        </div>

        {loading ? (
          <p className="cost-status">加载中…</p>
        ) : error ? (
          <p className="cost-status cost-error" role="alert">{error}</p>
        ) : !overview ? (
          <p className="cost-status">这段时间还没有产生消耗</p>
        ) : overview.total.calls === 0 && overview.total.cny === 0 ? (
          <p className="cost-status">这段时间还没有产生消耗</p>
        ) : (
          <>
            <div className="cost-summary">
              <CostStat label="总花费" value={`¥${formatCny(overview.total.cny)}`} />
              <CostStat label="Tokens" value={formatCount(overview.total.tokens)} />
              <CostStat label="调用次数" value={formatCount(overview.total.calls)} />
              <CostStat label="会话数" value={formatCount(overview.total.sessions)} />
              <CostStat label="缓存命中率" value={formatCacheRate(overview.total.cache_hit_tokens, overview.total.prompt_tokens)} />
            </div>

            <div className="cost-tabs" role="tablist" aria-label="消耗维度">
              {tabs.map((item) => (
                <button
                  key={item.key}
                  type="button"
                  role="tab"
                  aria-selected={tab === item.key}
                  className={tab === item.key ? 'cost-tab is-active' : 'cost-tab'}
                  onClick={() => setTab(item.key)}
                >
                  {item.label}
                </button>
              ))}
            </div>

            {tab === 'trend' ? (
              <TrendView daily={overview.daily} />
            ) : (
              <BreakdownList items={pickBreakdown(overview, tab)} kind={tab} />
            )}
          </>
        )}
      </section>
    </div>
  );
}

function CostStat({ label, value }: { label: string; value: string }) {
  return (
    <div className="cost-stat">
      <span className="cost-stat-value">{value}</span>
      <span className="cost-stat-label">{label}</span>
    </div>
  );
}

function pickBreakdown(overview: CostOverview, tab: Exclude<TabKey, 'trend'>): CostBreakdownItem[] {
  if (tab === 'session') {
    return overview.by_session;
  }
  return tab === 'model' ? overview.by_model : overview.by_tool;
}

function BreakdownList({ items, kind }: { items: CostBreakdownItem[]; kind: Exclude<TabKey, 'trend'> }) {
  if (items.length === 0) {
    return <p className="cost-status">这段时间还没有产生消耗</p>;
  }
  return (
    <div className="cost-list">
      {items.map((item) => {
        const width = `${Math.min(Math.max(item.share, 0), 1) * 100}%`;
        return (
          <div className="cost-row" key={`${kind}-${item.key}`}>
            <div className="cost-row-head">
              <div>
                <span className="cost-row-label" title={item.key}>{rowLabel(item, kind)}</span>
                <span className="cost-row-meta">{formatCount(item.calls)} 次调用 · {formatCount(item.tokens)} tokens</span>
              </div>
              <div>
                <span className="cost-row-cny">¥{formatCny(item.cny)}</span>
                <span className="cost-row-share">{formatPercent(item.share)}</span>
              </div>
            </div>
            <div className="cost-bar" aria-hidden="true">
              <span className="cost-bar-fill" style={{ width }} />
            </div>
          </div>
        );
      })}
    </div>
  );
}

function TrendView({ daily }: { daily: CostOverview['daily'] }) {
  if (daily.length === 0) {
    return <p className="cost-status">这段时间还没有产生消耗</p>;
  }
  const peak = daily.reduce((max, point) => Math.max(max, point.cny), 0);
  const middle = daily.length > 2 ? daily[Math.floor(daily.length / 2)] : null;
  return (
    <>
      <div className="cost-trend">
        {daily.map((point) => {
          // 有花费的日子至少留 2% 高度，否则零头会被压成看不见的一条线。
          const height = peak > 0 && point.cny > 0 ? `${Math.max((point.cny / peak) * 100, 2)}%` : '0%';
          return (
            <div
              className="cost-trend-col"
              key={point.date}
              title={`${point.date} · ¥${formatCny(point.cny)} · ${formatCount(point.calls)} 次调用`}
            >
              <span className="cost-trend-bar" style={{ height }} />
            </div>
          );
        })}
      </div>
      <div className="cost-trend-axis" aria-hidden="true">
        <span>{shortDate(daily[0].date)}</span>
        <span>{middle ? shortDate(middle.date) : ''}</span>
        <span>{shortDate(daily[daily.length - 1].date)}</span>
      </div>
    </>
  );
}

// rowLabel 会话没有标题时用截断的 session_id 占位，避免整行空白。
function rowLabel(item: CostBreakdownItem, kind: Exclude<TabKey, 'trend'>): string {
  const label = item.label?.trim();
  if (label) {
    return label;
  }
  if (kind === 'session') {
    return item.key.length > 12 ? `${item.key.slice(0, 12)}…` : item.key;
  }
  return item.key || '(direct)';
}

// formatCny 统一金额口径：不足 0.01 保留 4 位，否则 2 位，避免小额被四舍五入成 0.00。
function formatCny(value: number): string {
  if (!Number.isFinite(value) || value <= 0) {
    return '0.00';
  }
  return value < 0.01 ? value.toFixed(4) : value.toFixed(2);
}

// normalizeError 去掉后端 Errno 拼出的 "ParamError:" 之类前缀，只给用户看中文原因。
// 日期解析失败会带 time.Parse 的英文原文，那类统一换成人话。
function normalizeError(message: string): string {
  const raw = message.trim();
  const withoutCode = /^[A-Za-z_][A-Za-z0-9_]*:/.test(raw) ? raw.slice(raw.indexOf(':') + 1).trim() : raw;
  if (!withoutCode || withoutCode.includes('parsing time') || withoutCode.includes('time.Parse')) {
    return '日期格式不正确，应为 YYYY-MM-DD';
  }
  return withoutCode;
}

function formatPercent(share: number): string {
  const value = Number.isFinite(share) ? share * 100 : 0;
  if (value > 0 && value < 0.1) {
    return '<0.1%';
  }
  return `${value.toFixed(1)}%`;
}

function formatCacheRate(hit: number, prompt: number): string {
  if (!prompt || prompt <= 0) {
    return '—';
  }
  return `${((hit / prompt) * 100).toFixed(1)}%`;
}

function formatCount(value: number): string {
  if (!Number.isFinite(value)) {
    return '0';
  }
  return Math.round(value).toLocaleString('zh-CN');
}

function shortDate(value: string): string {
  return value.length >= 10 ? value.slice(5) : value;
}

function pad2(value: number): string {
  return value < 10 ? `0${value}` : String(value);
}

// formatDateLocal 用本地时区拼日期；toISOString 会按 UTC 算，东八区凌晨会偏到前一天。
function formatDateLocal(date: Date): string {
  return `${date.getFullYear()}-${pad2(date.getMonth() + 1)}-${pad2(date.getDate())}`;
}

function rangeDates(key: RangeKey): { from: string; to: string } {
  const today = new Date();
  const to = formatDateLocal(today);
  if (key === 'month') {
    return { from: formatDateLocal(new Date(today.getFullYear(), today.getMonth(), 1)), to };
  }
  const days = key === '7d' ? 7 : key === '30d' ? 30 : 90;
  return { from: formatDateLocal(new Date(today.getFullYear(), today.getMonth(), today.getDate() - (days - 1))), to };
}
