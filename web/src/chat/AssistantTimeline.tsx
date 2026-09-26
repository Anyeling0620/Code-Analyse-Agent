import { memo } from 'react';
import { InlineToolCard } from './InlineToolCard';
import { isBlankText } from './messageSegments';
import { MarkdownBlock } from '../markdown/MarkdownBlock';
import type { ChatMessage } from '../types/chat';

export const AssistantTimeline = memo(function AssistantTimeline({ message }: { message: ChatMessage }) {
  if (message.segments.length > 0) {
    return (
        <div className="assistant-timeline">
          {message.segments.map((seg) => {
            if (seg.type === 'error') {
              return (
                  <div key={seg.id} className="timeline-error">{seg.content}</div>
              );
            }
            if (seg.type === 'text') {
              if (isBlankText(seg.content)) return null;
              return (
                  <AssistantText key={seg.id} content={seg.content} className="timeline-text" />
              );
            }
            return <InlineToolCard key={seg.id} tool={seg.tool} />;
          })}
          {message.status === 'streaming' && !message.segments.some((s) => s.type === 'text' && !isBlankText(s.content)) && (
              <div className="markdown-body timeline-text"><p>正在思考中...</p></div>
          )}
        </div>
    );
  }

  return <AssistantText content={message.content || (message.status === 'streaming' ? '正在思考中...' : '...')} />;
});

const AssistantText = memo(function AssistantText({ content, className }: { content: string; className?: string }) {
  if (isBlankText(content)) {
    return null;
  }
  return <MarkdownBlock content={content} className={className} />;
});
