import { memo, useCallback, useEffect, useState } from 'react';
import { AssistantTimeline } from './AssistantTimeline';
import { TraceDialog } from './TraceDialog';
import { getMessageCopyText } from './messageSegments';
import { ExportMenu } from '../export/ExportMenu';
import { isExportableMessage, type ExportFormat } from '../export/exportDocument';
import type { ChatMessage } from '../types/chat';

export const MessageItem = memo(function MessageItem({
  message,
  onExportMessage,
}: {
  message: ChatMessage;
  onExportMessage: (messageId: string, format: ExportFormat) => void;
}) {
  const [copied, setCopied] = useState(false);
  const [menuAnchor, setMenuAnchor] = useState<{ top: number; left: number } | null>(null);
  const copyText = getMessageCopyText(message);
  const exportable = isExportableMessage(message);

  // 菜单跟随场景关闭：点击别处、按 Esc、或页面开始滚动。
  // 刚打开的一小段时间内忽略滚动，避免点击本身带出的滚动把菜单立刻关掉。
  useEffect(() => {
    if (!menuAnchor) {
      return;
    }
    const openedAt = Date.now();
    const close = () => setMenuAnchor(null);
    const closeOnScroll = () => {
      if (Date.now() - openedAt > 400) {
        setMenuAnchor(null);
      }
    };
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.key === 'Escape') {
        setMenuAnchor(null);
      }
    };
    document.addEventListener('click', close);
    document.addEventListener('keydown', onKeyDown);
    // capture：会话滚动条不在本组件里，只能从 document 捕获阶段监听。
    document.addEventListener('scroll', closeOnScroll, { capture: true, passive: true });
    return () => {
      document.removeEventListener('click', close);
      document.removeEventListener('keydown', onKeyDown);
      document.removeEventListener('scroll', closeOnScroll, { capture: true });
    };
  }, [menuAnchor]);

  const toggleExportMenu = useCallback((trigger: HTMLElement) => {
    setMenuAnchor((current) => {
      if (current) {
        return null;
      }
      const rect = trigger.getBoundingClientRect();
      return { top: rect.bottom + 6, left: rect.right };
    });
  }, []);

  async function copyMessage() {
    if (!copyText) {
      return;
    }
    await navigator.clipboard.writeText(copyText);
    setCopied(true);
    window.setTimeout(() => setCopied(false), 1200);
  }

  return (
      <article
          className={`message message-${message.role}${exportable ? ' message-exportable' : ''}`}
          data-message-id={message.id}
      >
        {exportable && (
            <button
                type="button"
                className="message-export-button"
                onClick={(event) => {
                  event.stopPropagation();
                  toggleExportMenu(event.currentTarget);
                }}
                title="导出这条项目分析"
                aria-label="导出这条项目分析"
                aria-haspopup="menu"
            >
              导出
              <span className="message-export-caret" aria-hidden="true">▾</span>
            </button>
        )}
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

        {menuAnchor && (
            <ExportMenu
                top={menuAnchor.top}
                left={menuAnchor.left}
                onPick={(format) => {
                  setMenuAnchor(null);
                  onExportMessage(message.id, format);
                }}
            />
        )}
      </article>
  );
});
