import { FormEvent, KeyboardEvent, memo, useRef, useState } from 'react';
import { formatCost, formatQuota } from '../chat/formatters';
import type { CostDailyTotal, QuotaToday } from '../types/chat';

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
    if (!message || isStreaming) {
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
          <textarea
              ref={textareaRef}
              onInput={handleInput}
              onKeyDown={handleKeyDown}
              placeholder="直接提真实问题。Enter 发送，Shift+Enter 换行。"
              rows={3}
          />
          <div className="composer-footer">
            <span className="composer-meta">{isStreaming ? '正在返回，可随时停止本轮。' : 'Enter 发送，Shift+Enter 换行。'}</span>
            <div className="actions-right">
              <span className="inline-metrics">成本 {formatCost(costDaily)} · 额度 {formatQuota(quota)}</span>
              <button type="button" className="icon-button scroll-button" onClick={onScrollTop} aria-label="置顶" title="置顶">
                <svg viewBox="0 0 24 24" aria-hidden="true">
                  <path d="M12 19V5m0 0-6 6m6-6 6 6" />
                </svg>
              </button>
              <button type="button" className="icon-button scroll-button" onClick={onScrollBottom} aria-label="置底" title="置底">
                <svg viewBox="0 0 24 24" aria-hidden="true">
                  <path d="M12 5v14m0 0-6-6m6 6 6-6" />
                </svg>
              </button>
              <button
                  type={isStreaming ? 'button' : 'submit'}
                  className={`icon-button ${isStreaming ? 'stop-button' : 'send-button'}`}
                  onClick={isStreaming ? onStop : undefined}
                  disabled={!isStreaming && !hasDraft}
                  aria-label={isStreaming ? '停止本轮' : '发送问题'}
                  title={isStreaming ? '停止本轮' : '发送问题'}
              >
                <svg viewBox="0 0 24 24" aria-hidden="true">
                  {isStreaming ? (
                      <rect x="7" y="7" width="10" height="10" rx="2" />
                  ) : (
                      <>
                        <path d="M4 19.5 20 12 4 4.5 6.8 12 4 19.5Z" />
                        <path d="M6.8 12H20" />
                      </>
                  )}
                </svg>
              </button>
            </div>
          </div>
        </div>
      </form>
  );
});
