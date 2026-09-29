import { useState } from 'react';
import type { FormEvent } from 'react';

type LoginModalProps = {
  onSubmit: (username: string, password: string) => Promise<void>;
  // 服务端开放游客登录时才展示入口；默认不显示，避免出现点了就报错的按钮。
  guestEnabled?: boolean;
  onGuestSubmit?: () => Promise<void>;
};

// 登录是进入应用的前置条件，因此该弹窗不提供关闭入口。
export function LoginModal({ onSubmit, guestEnabled = false, onGuestSubmit }: LoginModalProps) {
  const [username, setUsername] = useState('');
  const [password, setPassword] = useState('');
  const [error, setError] = useState('');
  const [submitting, setSubmitting] = useState(false);
  const [guestSubmitting, setGuestSubmitting] = useState(false);

  async function handleSubmit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    setError('');
    setSubmitting(true);
    try {
      await onSubmit(username.trim(), password);
    } catch (err) {
      setError(err instanceof Error && err.message ? err.message : '登录失败，请重试');
    } finally {
      setSubmitting(false);
    }
  }

  async function handleGuestSubmit() {
    if (!onGuestSubmit) {
      return;
    }
    setError('');
    setGuestSubmitting(true);
    try {
      await onGuestSubmit();
    } catch (err) {
      setError(err instanceof Error && err.message ? err.message : '游客登录失败，请重试');
    } finally {
      setGuestSubmitting(false);
    }
  }

  return (
    <div className="trace-modal-backdrop" role="presentation">
      <section className="trace-modal profile-modal login-modal" role="dialog" aria-modal="true" aria-label="登录">
        <div className="trace-modal-header">
          <div>
            <h3>登录</h3>
            <p>使用分配的账号登录后即可进入代码分析会话。</p>
          </div>
        </div>

        <form className="profile-form login-form" onSubmit={handleSubmit}>
          <div className="profile-form-grid">
            <label className="profile-field profile-field-full">
              <span>账号</span>
              <input
                value={username}
                onChange={(event) => setUsername(event.target.value)}
                placeholder="账号"
                autoComplete="username"
                autoFocus
              />
            </label>

            <label className="profile-field profile-field-full">
              <span>密码</span>
              <input
                type="password"
                value={password}
                onChange={(event) => setPassword(event.target.value)}
                placeholder="密码"
                autoComplete="current-password"
              />
            </label>
          </div>

          {error && <p className="panel-error">{error}</p>}

          <div className="login-actions">
            <button className="primary-button login-submit" type="submit" disabled={submitting || !username || !password}>
              {submitting ? '登录中…' : '登录'}
            </button>

            {guestEnabled && (
              <div className="guest-login">
                <span className="guest-login-divider">
                  <em>或</em>
                </span>
                <button
                  className="guest-login-button"
                  type="button"
                  onClick={handleGuestSubmit}
                  disabled={guestSubmitting || submitting}
                >
                  <span className="guest-login-icon" aria-hidden="true">
                    <svg viewBox="0 0 24 24">
                      <path d="M12 12a4 4 0 1 0 0-8 4 4 0 0 0 0 8Z" />
                      <path d="M5.5 19.5a6.8 6.8 0 0 1 13 0" />
                    </svg>
                  </span>
                  <span className="guest-login-label">{guestSubmitting ? '正在进入…' : '游客登录'}</span>
                  <svg className="guest-login-arrow" viewBox="0 0 24 24" aria-hidden="true">
                    <path d="m9.5 6 6 6-6 6" />
                  </svg>
                </button>
              </div>
            )}
          </div>
        </form>
      </section>
    </div>
  );
}
