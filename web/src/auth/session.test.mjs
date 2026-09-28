import { strict as assert } from 'node:assert';
import { registerHooks } from 'node:module';

// 被测源码用的是"无扩展名相对导入"（TS/打包器习惯），Node 的 ESM 解析器不接受。
// 这里只在解析失败时补 .ts，测试因此可以直接跑源码、不依赖打包器
// （本地 node_modules 是 pnpm 布局，顶层没有 esbuild）。
registerHooks({
  resolve(specifier, context, nextResolve) {
    if (specifier.startsWith('.') && !/\.[cm]?[jt]s$/.test(specifier)) {
      try {
        return nextResolve(`${specifier}.ts`, context);
      } catch {
        // 落回原始解析，让 nextResolve 抛出真实错误。
      }
    }
    return nextResolve(specifier, context);
  },
});

// 给 session.ts 备好 window/localStorage 替身后再导入，
// 让模块级的初始化与事件注册都发生在替身就绪之后。
const store = new Map();
const handlers = new Map();
globalThis.window = {
  localStorage: {
    getItem: (key) => (store.has(key) ? store.get(key) : null),
    setItem: (key, value) => {
      store.set(key, String(value));
    },
    removeItem: (key) => {
      store.delete(key);
    },
  },
  addEventListener: (type, handler) => {
    handlers.set(type, handler);
  },
};

const { saveSession, getSession, subscribeSession } = await import('./session.ts');
const { fetchJSON, fetchAuthorized, LOGIN_EXPIRED_MESSAGE } = await import('../api/client.ts');

const notifyLog = [];
subscribeSession((session) => notifyLog.push(session ? session.user_id : null));

globalThis.fetch = async () =>
  new Response(JSON.stringify({ code: 401, msg: 'Please Login' }), {
    status: 401,
    headers: { 'content-type': 'application/json' },
  });

// 1) fetchJSON 路径：401 必须清除登录态、落盘清空并通知订阅者
saveSession({ token: 'tok-1', user_id: 'Anyeling', plan: 'pro' });
assert.equal(getSession()?.user_id, 'Anyeling');
await assert.rejects(() => fetchJSON('/api/quota/today'), new RegExp(LOGIN_EXPIRED_MESSAGE));
assert.equal(getSession(), null, 'fetchJSON 收到 401 后应清除登录态');
assert.equal(notifyLog.at(-1), null, '订阅者应收到登出通知');
assert.equal(store.size, 0, 'localStorage 中的登录态也应被清掉');

// 2) 聊天/审批的 SSE 路径：此前走裸 fetch，401 只会把错误显示在消息气泡里，
//    界面永远不会退回登录页——这正是"令牌被清空却停在原页面"的原因。
saveSession({ token: 'tok-2', user_id: 'visitor', plan: 'plus' });
await assert.rejects(
  () => fetchAuthorized('/api/chat/stream', { method: 'POST' }),
  new RegExp(LOGIN_EXPIRED_MESSAGE),
);
assert.equal(getSession(), null, 'SSE 路径收到 401 后同样应清除登录态');

// 3) 登录接口的 401 表示账号密码错误，不能清除（可能已存在的）登录态
saveSession({ token: 'tok-3', user_id: 'visitor', plan: 'plus' });
await assert.rejects(
  () => fetchJSON('/api/auth/login', { method: 'POST' }, { handleUnauthorized: false }),
  /Please Login/,
);
assert.equal(getSession()?.user_id, 'visitor', '登录失败的 401 不应清除登录态');

// 4) 在 devtools 里清空 localStorage 后切回页面：focus 时应发现并回到未登录
store.clear();
handlers.get('focus')();
assert.equal(getSession(), null, 'focus 时发现令牌已被清空应回到未登录');

console.log('auth session: 4 组断言通过（401 清登录态 / SSE 路径 / 登录接口例外 / focus 同步）');
