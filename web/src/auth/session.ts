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

// storageWritable 记录 localStorage 是否可写。隐私模式下写失败时只能依赖内存态，
// 这时不能再拿"存储为空"当作登出信号，否则每次窗口获得焦点都会把人踢下线。
let storageWritable = true;

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
    storageWritable = false;
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

// syncFromStorage 把内存态对齐到 localStorage。
// 覆盖两种情况：其他标签页登出/登录（storage 事件），
// 以及用户在 devtools 里清掉令牌后切回本页（focus 事件，此时 storage 事件不会触发）。
function syncFromStorage(): void {
  if (!storageWritable) {
    return;
  }
  const stored = readStoredSession();
  if ((stored?.token ?? '') === (current?.token ?? '')) {
    return;
  }
  current = stored;
  notify();
}

// 注意：localStorage 被整体 clear 时 event.key 为 null。
if (typeof window !== 'undefined') {
  window.addEventListener('storage', (event) => {
    if (event.key === null || event.key === STORAGE_KEY) {
      syncFromStorage();
    }
  });
  window.addEventListener('focus', syncFromStorage);
}

function notify(): void {
  listeners.forEach((listener) => listener(current));
}
