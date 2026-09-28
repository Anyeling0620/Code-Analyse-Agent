// 登录态的唯一真相源：令牌与账号信息持久化在 localStorage，并通过订阅通知 UI 重新渲染，
// 避免每个组件各自维护一份登录状态。
export type AuthSession = {
  token: string;
  user_id: string;
  plan: string;
};

const STORAGE_KEY = 'edu.agent.code.auth';

const listeners = new Set<(session: AuthSession | null) => void>();

function readStoredSession(): AuthSession | null {
  try {
    const raw = window.localStorage.getItem(STORAGE_KEY);
    if (!raw) {
      return null;
    }
    const parsed = JSON.parse(raw) as AuthSession;
    return parsed && parsed.token ? parsed : null;
  } catch {
    // 存储内容被手工改坏时不阻塞启动，按未登录处理。
    return null;
  }
}

let current = readStoredSession();

export function getSession(): AuthSession | null {
  return current;
}

export function getToken(): string {
  return current ? current.token : '';
}

export function saveSession(session: AuthSession): void {
  current = session;
  try {
    window.localStorage.setItem(STORAGE_KEY, JSON.stringify(session));
  } catch {
    // 隐私模式等场景 localStorage 不可写，此时仅保留内存态，刷新后需重新登录。
  }
  notify();
}

export function clearSession(): void {
  current = null;
  try {
    window.localStorage.removeItem(STORAGE_KEY);
  } catch {
    // 同上。
  }
  notify();
}

export function subscribeSession(listener: (session: AuthSession | null) => void): () => void {
  listeners.add(listener);
  return () => {
    listeners.delete(listener);
  };
}

function notify(): void {
  listeners.forEach((listener) => listener(current));
}
