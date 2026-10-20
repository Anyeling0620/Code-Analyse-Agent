import { useEffect, useState } from 'react';
import type { FormEvent } from 'react';
import { SECTION_HUES } from '../ui/palette';

type LoginModalProps = {
  onSubmit: (username: string, password: string) => Promise<void>;
  // 服务端开放邮箱注册时才展示「登录 / 注册」切换与注册表单。
  emailRegisterEnabled?: boolean;
  // 服务端 SMTP 可用时才展示「忘记密码」入口。
  passwordResetEnabled?: boolean;
  onRegister?: (email: string, code: string, password: string) => Promise<void>;
  onResetPassword?: (email: string, code: string, password: string) => Promise<void>;
  // 返回重发倒计时秒数；失败时抛错，由弹窗展示中文提示。
  onSendCode?: (email: string, purpose: 'register' | 'reset') => Promise<number>;
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

const EMAIL_PATTERN = /^[^\s@]+@[^\s@]+\.[^\s@]+$/;
const DEFAULT_RESEND_SECONDS = 60;

function validateEmail(email: string): string {
  if (!email) {
    return '请输入邮箱';
  }
  if (email.length > 254 || !EMAIL_PATTERN.test(email)) {
    return '邮箱格式不正确';
  }
  return '';
}

function validatePassword(password: string): string {
  if (password.length < 8) {
    return '密码长度至少 8 个字符';
  }
  if (new TextEncoder().encode(password).length > 72) {
    return '密码长度不能超过 72 个字节';
  }
  if (!/[A-Za-z]/.test(password) || !/[0-9]/.test(password)) {
    return '密码必须同时包含字母和数字';
  }
  return '';
}

type Mode = 'login' | 'register' | 'reset';

// 登录是进入应用的前置条件，因此该页面不提供关闭入口。
export function LoginModal({
  onSubmit,
  emailRegisterEnabled = false,
  passwordResetEnabled = false,
  onRegister,
  onResetPassword,
  onSendCode,
}: LoginModalProps) {
  const [mode, setMode] = useState<Mode>('login');
  const [username, setUsername] = useState('');
  const [password, setPassword] = useState('');
  const [email, setEmail] = useState('');
  const [code, setCode] = useState('');
  const [newPassword, setNewPassword] = useState('');
  const [confirmPassword, setConfirmPassword] = useState('');
  const [countdown, setCountdown] = useState(0);
  const [error, setError] = useState('');
  const [notice, setNotice] = useState('');
  const [submitting, setSubmitting] = useState(false);
  const [codeSending, setCodeSending] = useState(false);

  // 发送成功后的重发倒计时。每次 countdown 变化重建一个 1 秒定时器，
  // 归零时自然停止；切换页面再切回来也不会重置。
  useEffect(() => {
    if (countdown <= 0) {
      return undefined;
    }
    const timer = window.setTimeout(() => setCountdown((value) => value - 1), 1000);
    return () => window.clearTimeout(timer);
  }, [countdown]);

  function resetFeedback() {
    setError('');
    setNotice('');
  }

  function switchMode(next: Mode) {
    if (next === mode) {
      return;
    }
    if (next === 'reset' && !email && username.includes('@')) {
      setEmail(username.trim());
    }
    setMode(next);
    resetFeedback();
  }

  async function handleSubmit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    resetFeedback();
    setSubmitting(true);
    try {
      await onSubmit(username.trim(), password);
    } catch (err) {
      setError(err instanceof Error && err.message ? err.message : '登录失败，请重试');
    } finally {
      setSubmitting(false);
    }
  }

  async function handleSendCode(purpose: 'register' | 'reset') {
    if (!onSendCode) {
      return;
    }
    const emailError = validateEmail(email.trim());
    if (emailError) {
      setError(emailError);
      return;
    }
    setError('');
    setNotice('');
    setCodeSending(true);
    try {
      const resendAfter = await onSendCode(email.trim(), purpose);
      setCountdown(resendAfter > 0 ? resendAfter : DEFAULT_RESEND_SECONDS);
      setNotice('验证码已发送，请查收邮箱（含垃圾邮件文件夹）');
    } catch (err) {
      setError(err instanceof Error && err.message ? err.message : '验证码发送失败，请稍后重试');
    } finally {
      setCodeSending(false);
    }
  }

  function validateCodeAndPassword(): boolean {
    const emailError = validateEmail(email.trim());
    if (emailError) {
      setError(emailError);
      return false;
    }
    if (!/^\d{6}$/.test(code.trim())) {
      setError('请输入 6 位数字验证码');
      return false;
    }
    const passwordError = validatePassword(newPassword);
    if (passwordError) {
      setError(passwordError);
      return false;
    }
    if (newPassword !== confirmPassword) {
      setError('两次输入的密码不一致');
      return false;
    }
    return true;
  }

  async function handleRegisterSubmit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (!onRegister) {
      return;
    }
    resetFeedback();
    if (!validateCodeAndPassword()) {
      return;
    }
    setSubmitting(true);
    try {
      await onRegister(email.trim(), code.trim(), newPassword);
    } catch (err) {
      setError(err instanceof Error ? err.message : '注册失败，请重试');
    } finally {
      setSubmitting(false);
    }
  }

  async function handleResetSubmit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (!onResetPassword) {
      return;
    }
    resetFeedback();
    if (!validateCodeAndPassword()) {
      return;
    }
    const resetEmail = email.trim();
    setSubmitting(true);
    try {
      await onResetPassword(resetEmail, code.trim(), newPassword);
      // 重设后旧令牌已失效，回到登录页让用户用新密码重新登录。
      setMode('login');
      setUsername(resetEmail);
      setPassword('');
      setCode('');
      setNewPassword('');
      setConfirmPassword('');
      setNotice('密码已重设，请使用新密码登录。');
    } catch (err) {
      setError(err instanceof Error ? err.message : '密码重设失败，请重试');
    } finally {
      setSubmitting(false);
    }
  }

  const registerMode = mode === 'register' && emailRegisterEnabled;
  const resetMode = mode === 'reset' && passwordResetEnabled;
  const heading = registerMode ? '注册' : resetMode ? '重设密码' : '登录';
  const description = registerMode
      ? '使用邮箱验证码创建账号，注册后直接进入工作台。'
      : resetMode
          ? '通过邮箱验证码设置新密码，重设后旧登录会话会失效。'
          : '';

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

            {emailRegisterEnabled && !resetMode && (
                <div className="login-mode-tabs" role="tablist" aria-label="登录或注册">
                  <button
                      type="button"
                      role="tab"
                      aria-selected={mode === 'login'}
                      className={`login-mode-tab${mode === 'login' ? ' is-active' : ''}`}
                      onClick={() => switchMode('login')}
                  >
                    登录
                  </button>
                  <button
                      type="button"
                      role="tab"
                      aria-selected={mode === 'register'}
                      className={`login-mode-tab${mode === 'register' ? ' is-active' : ''}`}
                      onClick={() => switchMode('register')}
                  >
                    注册
                  </button>
                </div>
            )}

            <header className="login-card-header">
              <h2>{heading}</h2>
              {description && <p>{description}</p>}
            </header>

            {registerMode ? (
                <form className="login-form" onSubmit={handleRegisterSubmit}>
                  <label className="field">
                    <span className="field-label">邮箱</span>
                    <input
                        type="email"
                        value={email}
                        onChange={(event) => setEmail(event.target.value)}
                        placeholder="you@example.com"
                        autoComplete="email"
                        autoFocus
                    />
                  </label>

                  <div className="field">
                    <span className="field-label">验证码</span>
                    <div className="code-field-row">
                      <input
                          value={code}
                          onChange={(event) => setCode(event.target.value.replace(/\D/g, '').slice(0, 6))}
                          placeholder="6 位数字验证码"
                          inputMode="numeric"
                          autoComplete="one-time-code"
                      />
                      <button
                          className="code-send-button"
                          type="button"
                          onClick={() => handleSendCode('register')}
                          disabled={codeSending || countdown > 0 || submitting}
                      >
                        {codeSending ? '发送中…' : countdown > 0 ? `${countdown}s 后重发` : '发送验证码'}
                      </button>
                    </div>
                  </div>

                  <label className="field">
                    <span className="field-label">密码</span>
                    <input
                        type="password"
                        value={newPassword}
                        onChange={(event) => setNewPassword(event.target.value)}
                        placeholder="至少 8 位，包含字母和数字"
                        autoComplete="new-password"
                    />
                  </label>

                  <label className="field">
                    <span className="field-label">确认密码</span>
                    <input
                        type="password"
                        value={confirmPassword}
                        onChange={(event) => setConfirmPassword(event.target.value)}
                        placeholder="再次输入密码"
                        autoComplete="new-password"
                    />
                  </label>

                  {notice && <p className="form-notice" role="status">{notice}</p>}
                  {error && <p className="form-error" role="alert">{error}</p>}

                  <button
                      className="primary-button login-submit"
                      type="submit"
                      disabled={submitting || !email || !code || !newPassword || !confirmPassword}
                  >
                    {submitting ? '注册中…' : '注册并进入'}
                  </button>
                </form>
            ) : resetMode ? (
                <form className="login-form" onSubmit={handleResetSubmit}>
                  <label className="field">
                    <span className="field-label">邮箱</span>
                    <input
                        type="email"
                        value={email}
                        onChange={(event) => setEmail(event.target.value)}
                        placeholder="you@example.com"
                        autoComplete="email"
                        autoFocus
                    />
                  </label>

                  <div className="field">
                    <span className="field-label">验证码</span>
                    <div className="code-field-row">
                      <input
                          value={code}
                          onChange={(event) => setCode(event.target.value.replace(/\D/g, '').slice(0, 6))}
                          placeholder="6 位数字验证码"
                          inputMode="numeric"
                          autoComplete="one-time-code"
                      />
                      <button
                          className="code-send-button"
                          type="button"
                          onClick={() => handleSendCode('reset')}
                          disabled={codeSending || countdown > 0 || submitting}
                      >
                        {codeSending ? '发送中…' : countdown > 0 ? `${countdown}s 后重发` : '发送验证码'}
                      </button>
                    </div>
                  </div>

                  <label className="field">
                    <span className="field-label">新密码</span>
                    <input
                        type="password"
                        value={newPassword}
                        onChange={(event) => setNewPassword(event.target.value)}
                        placeholder="至少 8 位，包含字母和数字"
                        autoComplete="new-password"
                    />
                  </label>

                  <label className="field">
                    <span className="field-label">确认新密码</span>
                    <input
                        type="password"
                        value={confirmPassword}
                        onChange={(event) => setConfirmPassword(event.target.value)}
                        placeholder="再次输入新密码"
                        autoComplete="new-password"
                    />
                  </label>

                  {notice && <p className="form-notice" role="status">{notice}</p>}
                  {error && <p className="form-error" role="alert">{error}</p>}

                  <button
                      className="primary-button login-submit"
                      type="submit"
                      disabled={submitting || !email || !code || !newPassword || !confirmPassword}
                  >
                    {submitting ? '重设中…' : '重设密码'}
                  </button>

                  <button className="login-text-link" type="button" onClick={() => switchMode('login')}>
                    返回登录
                  </button>
                </form>
            ) : (
                <form className="login-form" onSubmit={handleSubmit}>
                  <label className="field">
                    <span className="field-label">邮箱 / 账号</span>
                    <input
                        value={username}
                        onChange={(event) => setUsername(event.target.value)}
                        placeholder="输入邮箱或账号"
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

                  {notice && <p className="form-notice" role="status">{notice}</p>}
                  {error && <p className="form-error" role="alert">{error}</p>}

                  <button
                      className="primary-button login-submit"
                      type="submit"
                      disabled={submitting || !username || !password}
                  >
                    {submitting ? '登录中…' : '登录'}
                  </button>

                  {passwordResetEnabled && onResetPassword && (
                      <button
                          className="login-text-link"
                          type="button"
                          onClick={() => switchMode('reset')}
                      >
                          忘记密码？
                      </button>
                  )}
                </form>
            )}
          </div>
        </section>
      </main>
  );
}
