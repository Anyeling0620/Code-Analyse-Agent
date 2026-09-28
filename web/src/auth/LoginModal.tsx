import { useState } from 'react';
import type { FormEvent } from 'react';

type LoginModalProps = {
  onSubmit: (username: string, password: string) => Promise<void>;
};

// 登录是进入应用的前置条件，因此该弹窗不提供关闭入口。
export function LoginModal({ onSubmit }: LoginModalProps) {
  const [username, setUsername] = useState('');
  const [password, setPassword] = useState('');
  const [error, setError] = useState('');
  const [submitting, setSubmitting] = useState(false);

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

  return (
    <div className="trace-modal-backdrop" role="presentation">
      <section className="trace-modal profile-modal" role="dialog" aria-modal="true" aria-label="登录">
        <div className="trace-modal-header">
          <div>
            <h3>登录</h3>
            <p>使用分配的账号登录后即可进入代码分析会话。</p>
          </div>
        </div>

        <form className="profile-form" onSubmit={handleSubmit}>
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

          <div className="profile-modal-footer">
            <button className="primary-button" type="submit" disabled={submitting || !username || !password}>
              {submitting ? '登录中…' : '登录'}
            </button>
          </div>
        </form>
      </section>
    </div>
  );
}
