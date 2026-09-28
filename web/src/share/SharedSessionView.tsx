import { useEffect, useRef, useState } from 'react';
import { MessageList } from '../chat/MessageList';
import { formatTime } from '../chat/formatters';
import { messageRecordToChatMessage } from '../chat/messageSegments';
import type { ChatMessage, SharedSessionDetail } from '../types/chat';
import { fetchSharedSession } from './api';

type LoadState = 'loading' | 'ready' | 'error';

// SharedSessionView 是只读分享页：拿到分享令牌的任何人（不需要登录）都能查看。
// 展示的是分享创建那一刻的快照，会话后续继续对话不会影响这里的内容。
export function SharedSessionView({ token }: { token: string }) {
  const [state, setState] = useState<LoadState>('loading');
  const [detail, setDetail] = useState<SharedSessionDetail | null>(null);
  const [messages, setMessages] = useState<ChatMessage[]>([]);
  const [errorText, setErrorText] = useState('');
  const scrollRef = useRef<HTMLDivElement | null>(null);
  const bottomRef = useRef<HTMLDivElement | null>(null);

  useEffect(() => {
    let cancelled = false;
    setState('loading');
    setErrorText('');
    fetchSharedSession(token)
        .then((data) => {
          if (cancelled) {
            return;
          }
          setDetail(data);
          setMessages((data.list ?? []).map(messageRecordToChatMessage));
          setState('ready');
        })
        .catch((error) => {
          if (cancelled) {
            return;
          }
          setErrorText(error instanceof Error ? error.message : '分享内容加载失败，请稍后再试。');
          setState('error');
        });
    return () => {
      cancelled = true;
    };
  }, [token]);

  useEffect(() => {
    if (state !== 'ready') {
      return;
    }
    const frame = window.requestAnimationFrame(() => {
      const element = scrollRef.current;
      if (element) {
        element.scrollTop = element.scrollHeight;
      }
    });
    return () => window.cancelAnimationFrame(frame);
  }, [state, messages]);

  const summary = detail?.session?.summary || detail?.session?.last_user_message || '未命名会话';
  const projectName = detail?.session?.current_project_name || '';
  const sharedAt = detail ? formatTime(detail.shared_at) : '';

  return (
      <div className="share-page">
        <header className="share-page-header">
          <div className="share-page-banner" role="note">
            <span className="share-page-banner-badge">只读分享</span>
            <span className="share-page-banner-text">这是会话的历史快照，不可回复、不可修改。</span>
          </div>
          {state === 'ready' && (
              <div className="share-page-meta">
                <h1 className="share-page-title" title={summary}>{summary}</h1>
                <div className="share-page-chips">
                  {projectName && <span className="share-page-chip">项目：{projectName}</span>}
                  {sharedAt && <span className="share-page-chip">分享于 {sharedAt}</span>}
                  <span className="share-page-chip">共 {detail?.total ?? messages.length} 条消息</span>
                </div>
              </div>
          )}
        </header>

        <div ref={scrollRef} className="share-page-scroll">
          {state === 'loading' && <p className="share-page-state">正在加载分享内容…</p>}
          {state === 'error' && <p className="share-page-state share-page-error">{errorText}</p>}
          {state === 'ready' && messages.length === 0 && (
              <p className="share-page-state">这条分享里还没有消息内容。</p>
          )}
          {state === 'ready' && messages.length > 0 && (
              <MessageList
                  messages={messages}
                  bottomRef={bottomRef}
                  isLoadingOlderMessages={false}
                  hasMoreMessages={false}
              />
          )}
        </div>
      </div>
  );
}
