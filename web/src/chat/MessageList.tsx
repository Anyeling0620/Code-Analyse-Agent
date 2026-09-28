import { memo, type RefObject } from 'react';
import { MessageItem } from './MessageItem';
import type { ExportFormat } from '../export/exportDocument';
import type { ChatMessage } from '../types/chat';

export const MessageList = memo(function MessageList({
  messages,
  bottomRef,
  isLoadingOlderMessages,
  hasMoreMessages,
  onExportMessage,
}: {
  messages: ChatMessage[];
  bottomRef: RefObject<HTMLDivElement>;
  isLoadingOlderMessages: boolean;
  hasMoreMessages: boolean;
  // 可选：只读分享页没有导出入口，不传即可，消息卡片也不会为导出按钮留位。
  onExportMessage?: (messageId: string, format: ExportFormat) => void;
}) {
  return (
      <section className="message-list">
        {(hasMoreMessages || isLoadingOlderMessages) && (
            <div className="message-history-loader">{isLoadingOlderMessages ? '正在加载更早消息…' : '上滑加载更早消息'}</div>
        )}
        {messages.map((message) => (
            <MessageItem key={message.id} message={message} onExportMessage={onExportMessage} />
        ))}
        <div ref={bottomRef} />
      </section>
  );
});
