import { FormEvent, KeyboardEvent, memo, useRef, useState } from 'react';
import { formatCacheStats, formatCost, formatQuota } from '../chat/formatters';
import type { CostDailyTotal, QuotaToday } from '../types/chat';
import { ArrowDownIcon, ArrowUpIcon, SendIcon, StopIcon } from '../ui/icons';

export const Composer = memo(function Composer({
  isStreaming,
  isExpanded,
  costDaily,
  quota,
  onSubmit,
  onStop,
  onScrollTop,
  onScrollBottom,
}: {
  isStreaming: boolean;
  isExpanded: boolean;
  costDaily: CostDailyTotal | null;
  quota: QuotaToday | null;
  onSubmit: (message: string) => void;
  onStop: () => void;
  onScrollTop: () => void;
  onScrollBottom: () => void;
}) {
  const textareaRef = useRef<HTMLTextAreaElement | null>(null);
  const [hasDraft, setHasDraft] = useState(false);

  function submitDraft(event?: FormEvent) {
    event?.preventDefault();
    const textarea = textareaRef.current;
    const message = textarea?.value.trim() ?? '';
    // isStreaming 只表示"当前会话"在跑。同会话串行由上层强制（重复提交会被拒
    // 并给出提示），这里不再用全局忙碌挡掉其他会话的输入。
    if (!message) {
      return;
    }
    if (textarea) {
      textarea.value = '';
    }
    setHasDraft(false);
    onSubmit(message);
  }

  function handleInput(event: FormEvent<HTMLTextAreaElement>) {
    const nextHasDraft = event.currentTarget.value.trim() !== '';
    setHasDraft((current) => (current === nextHasDraft ? current : nextHasDraft));
  }

  function handleKeyDown(event: KeyboardEvent<HTMLTextAreaElement>) {
    if (event.key !== 'Enter' || event.shiftKey || event.nativeEvent.isComposing) {
      return;
    }
    event.preventDefault();
    submitDraft();
  }

  return (
      <form className={`composer composer-flat ${isExpanded ? 'composer-expanded' : 'composer-compact'}`} onSubmit={submitDraft}>
        <div className="composer-shell">
          <div className="composer-topline">
            <span className="composer-label">指令</span>
            <span className="composer-hint">
              {isStreaming ? '该会话正在返回，可随时停止本轮。' : 'Enter 发送 · Shift+Enter 换行'}
            </span>
          </div>
          <textarea
              ref={textareaRef}
              onInput={handleInput}
              onKeyDown={handleKeyDown}
              placeholder="描述要分析的项目，或继续追问当前代码…"
              aria-label="输入分析指令"
              rows={3}
          />
          <div className="composer-footer">
            <div className="composer-metrics">
              <span className="cache-metrics" title="今日累计：命中缓存的输入 token 与金额 / 未命中缓存的输入 token 与金额">
                {formatCacheStats(costDaily)}
              </span>
              <span className="inline-metrics">成本 {formatCost(costDaily)} · 额度 {formatQuota(quota)}</span>
            </div>
            <div className="actions-right">
              <button type="button" className="icon-button scroll-button" onClick={onScrollTop} aria-label="置顶" title="置顶">
                <ArrowUpIcon size={17} />
              </button>
              <button type="button" className="icon-button scroll-button" onClick={onScrollBottom} aria-label="置底" title="置底">
                <ArrowDownIcon size={17} />
              </button>
              <button
                  type={isStreaming ? 'button' : 'submit'}
                  className={`icon-button ${isStreaming ? 'stop-button' : 'send-button'}`}
                  onClick={isStreaming ? onStop : undefined}
                  disabled={!isStreaming && !hasDraft}
                  aria-label={isStreaming ? '停止本轮' : '发送问题'}
                  title={isStreaming ? '停止本轮' : '发送问题'}
              >
                {isStreaming ? <StopIcon size={18} /> : <SendIcon size={18} />}
              </button>
            </div>
          </div>
        </div>
      </form>
  );
});
