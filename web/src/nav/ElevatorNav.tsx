import { memo, useCallback, useEffect, useMemo, useRef, useState, type CSSProperties, type RefObject } from 'react';
import { isExportableMessage } from '../export/exportDocument';
import type { ChatMessage } from '../types/chat';
import { sectionHueAt } from '../ui/palette';

/** 少于两条消息时没有导航价值，与 App 里预留右侧空间的条件保持一致。 */
export function shouldShowElevatorNav(messages: ChatMessage[]) {
  return messages.length >= 2;
}

/** 胶囊 tooltip 用的单行摘要：折叠空白、去掉 Markdown 结构符，避免 tooltip 串行。 */
function previewOf(message: ChatMessage) {
  const singleLine = message.content
      .replace(/```[\s\S]*?```/g, ' ')
      .replace(/\s+/g, ' ')
      .replace(/^[#>\-*\s]+/, '')
      .trim();
  if (!singleLine) {
    return message.role === 'user' ? '（空消息）' : '（等待回答）';
  }
  return singleLine.length > 60 ? `${singleLine.slice(0, 60)}…` : singleLine;
}

/**
 * 右侧页缘标签轨：一条消息对应一枚页签。
 *
 * 形态来自手册装订边的阶梯索引：页签高度按消息篇幅变化，当前阅读位置把
 * 页签向外抽出；可导出的项目分析回答额外带一枚铬黄缺口标记。
 */
export const ElevatorNav = memo(function ElevatorNav({
  messages,
  containerRef,
  onActiveChange,
}: {
  messages: ChatMessage[];
  containerRef: RefObject<HTMLDivElement | null>;
  onActiveChange?: (messageId: string) => void;
}) {
  const [activeId, setActiveId] = useState('');
  // 平滑滚动期间不要用"基准线"重算高亮，否则刚点的那一条会被上一条抢走。
  const jumpLockRef = useRef(0);

  const exportableIds = useMemo(
      () => new Set(messages.filter(isExportableMessage).map((message) => message.id)),
      [messages],
  );

  useEffect(() => {
    onActiveChange?.(activeId);
  }, [activeId, onActiveChange]);

  // 滚动联动：以可视区上方 1/3 处为基准线，取最后一条越过基准线的消息作为"当前消息"。
  useEffect(() => {
    const container = containerRef.current;
    if (!container) {
      return;
    }
    let frame = 0;
    const update = () => {
      frame = 0;
      if (Date.now() < jumpLockRef.current) {
        return;
      }
      const nodes = container.querySelectorAll<HTMLElement>('[data-message-id]');
      if (nodes.length === 0) {
        setActiveId('');
        return;
      }
      // 到底时最后一条可能永远越不过基准线（容器滚不动了），直接判给最后一条。
      const atBottom = container.scrollTop + container.clientHeight >= container.scrollHeight - 8;
      if (atBottom) {
        setActiveId(nodes[nodes.length - 1].dataset.messageId ?? '');
        return;
      }
      const threshold = container.getBoundingClientRect().top + container.clientHeight / 3;
      let current = nodes[0].dataset.messageId ?? '';
      nodes.forEach((node) => {
        if (node.getBoundingClientRect().top <= threshold) {
          current = node.dataset.messageId ?? current;
        }
      });
      setActiveId(current);
    };
    const schedule = () => {
      if (!frame) {
        frame = window.requestAnimationFrame(update);
      }
    };
    container.addEventListener('scroll', schedule, { passive: true });
    window.addEventListener('resize', schedule);
    update();
    return () => {
      container.removeEventListener('scroll', schedule);
      window.removeEventListener('resize', schedule);
      if (frame) {
        window.cancelAnimationFrame(frame);
      }
    };
  }, [containerRef, messages.length]);

  const jumpTo = useCallback((messageId: string) => {
    jumpLockRef.current = Date.now() + 700;
    setActiveId(messageId);
    const node = containerRef.current?.querySelector<HTMLElement>(`[data-message-id="${messageId}"]`);
    node?.scrollIntoView({ behavior: 'smooth', block: 'start' });
  }, [containerRef]);

  if (!shouldShowElevatorNav(messages)) {
    return null;
  }

  return (
      <nav className="elevator-nav" aria-label="对话页缘索引">
        <div className="elevator-rule" aria-hidden="true" />
        <div className="elevator-list">
          {messages.map((message, index) => {
            const exportable = exportableIds.has(message.id);
            const active = message.id === activeId;
            const hue = sectionHueAt(index);
            const extent = message.content.length
                + message.toolEvents.reduce((total, tool) => total + (tool.result?.length ?? 0), 0);
            const height = Math.min(22, Math.max(8, 8 + Math.round(extent / 240)));
            const classes = [
              'elevator-item',
              active ? 'elevator-item-active' : '',
              exportable ? 'elevator-item-exportable' : '',
            ].filter(Boolean).join(' ');
            return (
                <div className="elevator-slot" key={message.id}>
                  <button
                      type="button"
                      className={classes}
                      style={{ '--tab-height': `${height}px`, '--section-hue': hue.value } as CSSProperties}
                      onClick={() => jumpTo(message.id)}
                      aria-current={active ? 'true' : undefined}
                      title={`#${index + 1} · ${message.role === 'user' ? '我' : 'AI'}：${previewOf(message)}${exportable ? '\n可导出' : ''}`}
                      aria-label={`跳到第 ${index + 1} 条消息：${previewOf(message)}`}
                  >
                    <span className="elevator-tab" aria-hidden="true" />
                    {exportable && <span className="elevator-export-mark" aria-hidden="true" />}
                  </button>
                </div>
            );
          })}
        </div>
        <div className="elevator-rule" aria-hidden="true" />
      </nav>
  );
});
