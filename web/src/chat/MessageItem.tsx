import { memo, useState } from 'react';
import { AssistantTimeline } from './AssistantTimeline';
import { TraceDialog } from './TraceDialog';
import { getMessageCopyText } from './messageSegments';
import type { ChatMessage } from '../types/chat';

export const MessageItem = memo(function MessageItem({ message }: { message: ChatMessage }) {
  const [copied, setCopied] = useState(false);
  const copyText = getMessageCopyText(message);

  async function copyMessage() {
    if (!copyText) {
      return;
    }
    await navigator.clipboard.writeText(copyText);
    setCopied(true);
    window.setTimeout(() => setCopied(false), 1200);
  }

  return (
      <article className={`message message-${message.role}`}>
        <button
            type="button"
            className="message-copy-button"
            onClick={() => void copyMessage()}
            disabled={!copyText}
            aria-label="复制消息"
            title={copied ? '已复制' : '复制消息'}
        >
          {copied ? '✓' : '⧉'}
        </button>
        <div className="message-meta">
          {message.status === 'streaming' && <span className="stream-dot">流式返回中</span>}
          {message.status === 'error' && <span className="stream-error">本轮异常</span>}
          {message.tools.map((tool) => (
              <span key={`${message.id}-${tool}`} className="tool-chip">
                {tool}
              </span>
          ))}
          {message.role === 'assistant' && message.traceEvents.length > 0 && <TraceDialog events={message.traceEvents} />}
        </div>

        {message.role === 'assistant' ? (
            <AssistantTimeline message={message} />
        ) : (
            <p className="plain-text">{message.content}</p>
        )}
      </article>
  );
});
