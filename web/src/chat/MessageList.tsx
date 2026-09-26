import { memo, type RefObject } from 'react';
import { MessageItem } from './MessageItem';
import type { ChatMessage } from '../types/chat';

export const MessageList = memo(function MessageList({
  messages,
  bottomRef,
  isLoadingOlderMessages,
  hasMoreMessages,
}: {
  messages: ChatMessage[];
  bottomRef: RefObject<HTMLDivElement>;
  isLoadingOlderMessages: boolean;
  hasMoreMessages: boolean;
}) {
  return (
      <section className="message-list">
        {(hasMoreMessages || isLoadingOlderMessages) && (
            <div className="message-history-loader">{isLoadingOlderMessages ? '正在加载更早消息…' : '上滑加载更早消息'}</div>
        )}
        {messages.map((message) => (
            <MessageItem key={message.id} message={message} />
        ))}
        <div ref={bottomRef} />
      </section>
  );
});
