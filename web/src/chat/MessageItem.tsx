import { memo, useCallback, useEffect, useState, type CSSProperties } from 'react';
import { AssistantTimeline } from './AssistantTimeline';
import { TraceDialog } from './TraceDialog';
import { getMessageCopyText } from './messageSegments';
import { ExportMenu } from '../export/ExportMenu';
import { isExportableMessage, type ExportFormat } from '../export/exportDocument';
import type { ChatMessage } from '../types/chat';
import { ActivityIcon, CheckIcon, ChevronDownIcon, CopyIcon } from '../ui/icons';
import { sectionHueAt, sectionLabel } from '../ui/palette';

export const MessageItem = memo(function MessageItem({
  message,
  index = 0,
  onExportMessage,
  onLoadDetails,
}: {
  message: ChatMessage;
  index?: number;
  // 可选：未传时视为该场景不提供导出（如只读分享页）。
  onExportMessage?: (messageId: string, format: ExportFormat) => void;
  // 可选：历史消息的调用事件懒加载入口；分享页也复用同一套 UI。
  onLoadDetails?: (messageId: string) => void;
}) {
  const [copied, setCopied] = useState(false);
  const [menuAnchor, setMenuAnchor] = useState<{ top: number; left: number } | null>(null);
  const copyText = getMessageCopyText(message);
  const exportable = !!onExportMessage && isExportableMessage(message);
  const hue = sectionHueAt(index);

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
          style={{ '--section-hue': hue.value } as CSSProperties}
      >
        {message.role === 'assistant' && (
            <>
              <span
                  className="message-section-tab"
                  style={{ background: hue.value, color: hue.on }}
                  aria-hidden="true"
              >
                {sectionLabel(index)}
              </span>
              <span className="punch-hole punch-hole-top" aria-hidden="true" />
              <span className="punch-hole punch-hole-bottom" aria-hidden="true" />
            </>
        )}

        <div className="message-meta">
          <span className={`message-role-stamp message-role-${message.role}`}>
            {message.role === 'assistant' ? 'ANALYSIS' : 'INSTRUCTION'}
          </span>
          {message.status === 'streaming' && (
              <span className="stream-stamp stream-stamp-live">
                <span className="live-square" aria-hidden="true" />
                流式返回中
              </span>
          )}
          {message.status === 'error' && <span className="stream-stamp stream-stamp-error">本轮异常</span>}
          {message.tools.map((tool) => (
              <span key={`${message.id}-${tool}`} className="tool-chip">
                {tool}
              </span>
          ))}
          {message.role === 'assistant' && message.hasRenderEvents && !message.eventsLoaded && onLoadDetails && (
              <button
                  type="button"
                  className="tool-chip trace-entry-button detail-load-button"
                  disabled={message.eventsLoading}
                  onClick={() => onLoadDetails(message.id)}
                  title="按需加载这条回答的工具调用详情"
              >
                <ActivityIcon size={13} />
                {message.eventsLoading ? '调用详情加载中…' : '加载调用详情'}
              </button>
          )}
          {message.role === 'assistant' && message.traceEvents.length > 0 && <TraceDialog events={message.traceEvents} />}
        </div>

        <div className="message-content">
          {message.role === 'assistant' ? (
              <AssistantTimeline message={message} />
          ) : (
              <p className="plain-text">{message.content}</p>
          )}
        </div>

        <div className="message-actions">
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
                  <ChevronDownIcon size={12} />
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
            {copied ? <CheckIcon size={14} /> : <CopyIcon size={14} />}
          </button>
        </div>

        {menuAnchor && (
            <ExportMenu
                top={menuAnchor.top}
                left={menuAnchor.left}
                onPick={(format) => {
                  setMenuAnchor(null);
                  onExportMessage?.(message.id, format);
                }}
            />
        )}
      </article>
  );
});
