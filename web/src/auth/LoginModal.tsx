import { useState } from 'react';
import type { FormEvent } from 'react';
import { SECTION_HUES } from '../ui/palette';
import { ChevronRightIcon, UserIcon } from '../ui/icons';

type LoginModalProps = {
  onSubmit: (username: string, password: string) => Promise<void>;
  // 服务端开放游客登录时才展示入口；默认不显示，避免出现点了就报错的按钮。
  guestEnabled?: boolean;
  onGuestSubmit?: () => Promise<void>;
};

const coverIndex = [
  '仓库分析',
  '项目问答',
  '数据库报表',
  '语义索引',
  '执行证据',
  '断点续跑',
  '只读分享',
];

// 登录是进入应用的前置条件，因此该页面不提供关闭入口。
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
      <main className="login-page">
        <section className="login-cover" aria-label="Repo Agent">
          <div className="login-brand">
            <span className="brand-mark brand-mark-large" aria-hidden="true">
              <span className="brand-mark-line" />
              <span className="brand-mark-line" />
              <span className="brand-mark-line" />
            </span>
            <span className="login-wordmark">Repo Agent</span>
          </div>

          <div className="login-cover-copy">
            <h1>代码仓库<br />分析工作台</h1>
            <p>
              把仓库接入为一张可追问的分析页：项目级语义索引、工具调用、结构化报告与执行证据，
              留在同一条可恢复的 run 上。
            </p>
          </div>

          <div className="login-tab-index" aria-label="产品能力索引">
            {coverIndex.map((name, index) => {
              const hue = SECTION_HUES[index % SECTION_HUES.length];
              return (
                  <span
                      className="login-tab"
                      key={name}
                      style={{ background: hue.value, color: hue.on }}
                  >
                    <span className="login-tab-name">{name}</span>
                  </span>
              );
            })}
          </div>

          <p className="login-cover-foot">仓库分析 · 项目问答 · 数据库报表</p>
        </section>

        <section className="login-panel">
          <div className="login-card">
            <span className="login-card-tab" aria-hidden="true">ACCESS</span>
            <header className="login-card-header">
              <h2>登录</h2>
              <p>使用分配的账号进入工作台，或以游客身份试用。</p>
            </header>

            <form className="login-form" onSubmit={handleSubmit}>
              <label className="field">
                <span className="field-label">账号</span>
                <input
                    value={username}
                    onChange={(event) => setUsername(event.target.value)}
                    placeholder="输入账号"
                    autoComplete="username"
                    autoFocus
                />
              </label>

              <label className="field">
                <span className="field-label">密码</span>
                <input
                    type="password"
                    value={password}
                    onChange={(event) => setPassword(event.target.value)}
                    placeholder="输入密码"
                    autoComplete="current-password"
                />
              </label>

              {error && <p className="form-error" role="alert">{error}</p>}

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
                        <UserIcon size={18} />
                      </span>
                      <span className="guest-login-copy">
                        <strong>{guestSubmitting ? '正在进入…' : '游客登录'}</strong>
                        <span>不创建账号，按浏览器身份试用</span>
                      </span>
                      <ChevronRightIcon size={18} />
                    </button>
                  </div>
              )}
            </form>

            <p className="login-note">高危命令仍需逐条确认；长任务断线后会自动续传。</p>
          </div>
        </section>
      </main>
  );
}
