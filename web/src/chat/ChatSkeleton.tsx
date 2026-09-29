import { memo } from 'react';

/**
 * 切换会话时的占位骨架。
 *
 * 交互目标：点中左侧会话后立即切到该标签页并显示骨架，
 * 历史消息在后台拉取，拉完再换成真实内容——避免"点了没反应"的假死感。
 */
export const ChatSkeleton = memo(function ChatSkeleton({
  label = '正在加载历史消息…',
}: {
  label?: string;
}) {
  return (
      <section className="chat-skeleton" aria-busy="true" aria-live="polite">
        <span className="chat-skeleton-hint">
          <span className="chat-skeleton-spinner" aria-hidden="true" />
          {label}
        </span>
        {[0, 1, 2].map((index) => (
            <div key={index} className="chat-skeleton-card">
              <span className="chat-skeleton-line chat-skeleton-line-title" />
              <span className="chat-skeleton-line" />
              <span className="chat-skeleton-line chat-skeleton-line-short" />
              <span className="chat-skeleton-line" />
              <span className="chat-skeleton-line chat-skeleton-line-mid" />
            </div>
        ))}
      </section>
  );
});
