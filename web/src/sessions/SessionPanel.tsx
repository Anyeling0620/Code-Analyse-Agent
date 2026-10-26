import { memo, type UIEvent } from 'react';
import { formatTime } from '../chat/formatters';
import type { SessionListItem } from '../types/chat';
import { CloseIcon, LogoutIcon, PlusIcon, ShareIcon, TrashIcon, UserIcon } from '../ui/icons';
import { sectionHueFor } from '../ui/palette';

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
  isStreamingFor,
  busySessions,
  isLoadingMoreSessions,
  hasMoreSessions,
  userId,
  plan,
  onStartNewSession,
  onLoadSession,
  onLoadMoreSessions,
  onDeleteSession,
  onShareSession,
  onOpenProfile,
  onOpenCost,
  onLogout,
  onClose,
  shareNotice,
}: {
  health: 'checking' | 'online' | 'offline';
  panelError: string;
  sessions: SessionListItem[];
  sessionId: string;
  /** 单个会话是否正在生成。跨会话并行后不能再有"整页忙碌"的布尔量。 */
  isStreamingFor: (sessionId: string) => boolean;
  /** 正在后台生成的会话 id 列表，用于"有没有别处在跑"的提示。 */
  busySessions: () => string[];
  isLoadingMoreSessions: boolean;
  hasMoreSessions: boolean;
  userId: string;
  plan: string;
  onStartNewSession: () => void;
  onLoadSession: (sessionId: string) => void;
  onLoadMoreSessions: () => void;
  onDeleteSession: (sessionId: string) => void;
  onShareSession: (sessionId: string) => void;
  onOpenProfile: () => void;
  onOpenCost: () => void;
  onLogout: () => void;
  onClose?: () => void;
  shareNotice: string;
}) {
  const busyCount = busySessions().length;

  function handleSessionListScroll(event: UIEvent<HTMLDivElement>) {
    const element = event.currentTarget;
    const distanceToBottom = element.scrollHeight - element.scrollTop - element.clientHeight;
    if (distanceToBottom < 80 && hasMoreSessions && !isLoadingMoreSessions) {
      onLoadMoreSessions();
    }
  }

  return (
      <section className="session-panel">
        <header className="brand-block">
          <span className="brand-mark" aria-hidden="true">
            <span className="brand-mark-line" />
            <span className="brand-mark-line" />
            <span className="brand-mark-line" />
          </span>
          <div className="brand-copy">
            <span className="brand-name">Repo Agent</span>
            <span className="brand-sub">代码分析工作台</span>
          </div>
          <button className="panel-close-button" type="button" onClick={onClose} aria-label="关闭会话索引">
            <CloseIcon size={18} />
          </button>
        </header>

        <button className="new-session-button" type="button" onClick={onStartNewSession}>
          <span className="new-session-icon" aria-hidden="true"><PlusIcon size={16} /></span>
          <span>新建分析</span>
          {busyCount > 0 && (
            <span className="new-session-busy" title={`${busyCount} 个会话正在后台生成`}>{busyCount}</span>
          )}
        </button>

        {panelError && <p className="panel-error" role="status">{panelError}</p>}
        {shareNotice && <p className="share-notice" role="status">{shareNotice}</p>}

        <div className="session-list-section">
          <div className="session-list-heading">
            <span>会话索引</span>
            <span className="session-count">{sessions.length}</span>
          </div>
          <div className="session-list" onScroll={handleSessionListScroll}>
            {sessions.length === 0 ? (
                <p className="empty-session">
                  索引还是空的。发出第一条分析后，会话会保留在这里，可随时切回。
                </p>
            ) : (
                <>
                  {sessions.map((item) => {
                    const hue = sectionHueFor(item.session_id);
                    const active = item.session_id === sessionId;
                    const busy = isStreamingFor(item.session_id);
                    const title = item.title || item.last_user_message || item.summary || '未命名会话';
                    return (
                        <div
                            key={item.session_id}
                            className={`session-item${active ? ' session-item-active' : ''}${busy ? ' session-item-busy' : ''}`}
                        >
                          <button
                              className="session-item-main"
                              type="button"
                              onClick={() => void onLoadSession(item.session_id)}
                              title={`${title}${busy ? '\n正在生成回复' : ''}\n更新时间：${formatTime(item.update_at)}`}
                              aria-label={`${title}，更新时间：${formatTime(item.update_at)}`}
                              aria-current={active ? 'true' : undefined}
                          >
                            <span
                                className="session-swatch"
                                style={{ background: hue.value }}
                                aria-hidden="true"
                            />
                            <span className="session-item-copy">
                              <span className="session-title-line">{title}</span>
                              <span className="session-item-meta">
                                {busy && <span className="session-running-dot" aria-hidden="true" />}
                                {busy ? '正在生成' : formatTime(item.update_at)}
                              </span>
                            </span>
                          </button>
                          <div className="session-item-actions">
                            <button
                                className="session-icon-button session-share-button"
                                type="button"
                                onClick={(event) => {
                                  event.stopPropagation();
                                  onShareSession(item.session_id);
                                }}
                                aria-label="分享会话"
                                title="生成只读分享链接"
                            >
                              <ShareIcon size={14} />
                            </button>
                            {item.session_id !== sessionId && (
                                <button
                                    className="session-icon-button session-delete-button"
                                    type="button"
                                    onClick={(event) => {
                                      event.stopPropagation();
                                      void onDeleteSession(item.session_id);
                                    }}
                                    aria-label="删除会话"
                                    title="删除会话"
                                >
                                  <TrashIcon size={14} />
                                </button>
                            )}
                          </div>
                        </div>
                    );
                  })}
                  <div className="session-list-loader">
                    {isLoadingMoreSessions ? '正在加载更多会话…' : hasMoreSessions ? '下滑加载更多' : '已到索引末尾'}
                  </div>
                </>
            )}
          </div>
        </div>

        <footer className="panel-footer">
          <button className="profile-entry-button" type="button" onClick={onOpenProfile} title="填写基础画像">
            <UserIcon size={15} />
            <span>基础画像</span>
          </button>
          <button type="button" className="account-card" onClick={onOpenCost} aria-haspopup="dialog">
            <span className="account-kind">ACCOUNT</span>
            <span className="account-id" title={userId}>{userId}</span>
            <span className="account-plan">{plan}</span>
            <span className="account-cost-cue" aria-hidden="true">消耗</span>
          </button>
          <div className="panel-status-row">
            <span className={`health health-${health}`} title={healthLabel[health]} aria-label={healthLabel[health]} />
            <span className="health-label">{healthLabel[health]}</span>
            <button className="logout-button" type="button" onClick={onLogout}>
              <LogoutIcon size={14} />
              <span>退出</span>
            </button>
          </div>
        </footer>
      </section>
  );
});
