import { memo, type UIEvent } from 'react';
import { formatTime } from '../chat/formatters';
import type { SessionListItem } from '../types/chat';

const healthLabel: Record<'checking' | 'online' | 'offline', string> = {
  checking: '连通性检查中',
  online: '后端在线',
  offline: '后端离线',
};

export const SessionPanel = memo(function SessionPanel({
  health,
  panelError,
  sessions,
  sessionId,
  isStreaming,
  isLoadingMoreSessions,
  hasMoreSessions,
  onStartNewSession,
  onLoadSession,
  onLoadMoreSessions,
  onDeleteSession,
  onOpenProfile,
}: {
  health: 'checking' | 'online' | 'offline';
  panelError: string;
  sessions: SessionListItem[];
  sessionId: string;
  isStreaming: boolean;
  isLoadingMoreSessions: boolean;
  hasMoreSessions: boolean;
  onStartNewSession: () => void;
  onLoadSession: (sessionId: string) => void;
  onLoadMoreSessions: () => void;
  onDeleteSession: (sessionId: string) => void;
  onOpenProfile: () => void;
}) {
  function handleSessionListScroll(event: UIEvent<HTMLDivElement>) {
    const element = event.currentTarget;
    const distanceToBottom = element.scrollHeight - element.scrollTop - element.clientHeight;
    if (distanceToBottom < 80 && hasMoreSessions && !isLoadingMoreSessions) {
      onLoadMoreSessions();
    }
  }
  return (
      <section className="session-panel">
        <div className="session-panel-header">
          <div>
            <h4>Repo Agent</h4>
          </div>
          <div className="session-panel-actions">
            <button className="profile-entry-button" type="button" onClick={onOpenProfile} title="填写基础画像">
              基础画像
            </button>
            <span className={`health health-${health}`} title={healthLabel[health]} aria-label={healthLabel[health]} />
          </div>
        </div>
        <button className="new-session-button" type="button" onClick={onStartNewSession} disabled={isStreaming}>
          <span className="new-session-icon">＋</span>
          <span className="session-list-heading">新聊天</span>
        </button>
        {panelError && <span className="panel-error">{panelError}</span>}
        <div className="session-list-section">
          <div className="session-list-heading">最近</div>
          <div className="session-list" onScroll={handleSessionListScroll}>
            {sessions.length === 0 ? (
                <p className="empty-session">暂无历史会话，发送第一条消息后会自动保存。</p>
            ) : (
                <>
                  {sessions.map((item) => (
                      <div key={item.session_id} className={`session-item${item.session_id === sessionId ? ' session-item-active' : ''}`}>
                        <button
                            className="session-item-main"
                            type="button"
                            onClick={() => void onLoadSession(item.session_id)}
                            disabled={isStreaming}
                            title={`${item.last_user_message || item.summary || '未命名会话'}\n更新时间：${formatTime(item.update_at)}`}
                            aria-label={`${item.last_user_message || item.summary || '未命名会话'}，更新时间：${formatTime(item.update_at)}`}
                        >
                          <span className="session-title-line">
                            {item.last_user_message || item.summary || '未命名会话'}
                          </span>
                        </button>
                        {item.session_id !== sessionId && (
                            <button
                                className="session-delete-button"
                                type="button"
                                onClick={(event) => {
                                  event.stopPropagation();
                                  void onDeleteSession(item.session_id);
                                }}
                                disabled={isStreaming}
                                aria-label="删除会话"
                                title="删除会话"
                            >
                              ×
                            </button>
                        )}
                      </div>
                  ))}
                  <div className="session-list-loader">
                    {isLoadingMoreSessions ? '正在加载更多会话…' : hasMoreSessions ? '下滑加载更多' : '没有更多会话了'}
                  </div>
                </>
            )}
          </div>
        </div>
      </section>
  );
});
