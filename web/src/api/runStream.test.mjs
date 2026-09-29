import { strict as assert } from 'node:assert';
import { registerHooks } from 'node:module';

// 被测源码用"无扩展名相对导入"，Node ESM 解析器不接受；这里只在解析失败时补 .ts，
// 让测试直接跑源码、不依赖打包器（与 src/auth/session.test.mjs 同一套路）。
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

// runStream.ts 会经过 client.ts → auth/session.ts，后者在模块加载时就读 localStorage，
// 所以要先把浏览器替身准备好再 import。
const localStore = new Map();
globalThis.window = {
  localStorage: {
    getItem: (key) => (localStore.has(key) ? localStore.get(key) : null),
    setItem: (key, value) => {
      localStore.set(key, String(value));
    },
    removeItem: (key) => {
      localStore.delete(key);
    },
  },
  addEventListener: () => {},
};

const sessionStore = new Map();
globalThis.sessionStorage = {
  getItem: (key) => (sessionStore.has(key) ? sessionStore.get(key) : null),
  setItem: (key, value) => {
    sessionStore.set(key, String(value));
  },
  removeItem: (key) => {
    sessionStore.delete(key);
  },
};

const { consumeSSE } = await import('./sse.ts');
const { streamChatRun, attachRun, readActiveRun, writeActiveRun, clearActiveRun } = await import('./runStream.ts');

const encoder = new TextEncoder();

function sseStream(chunks) {
  return new ReadableStream({
    start(controller) {
      for (const chunk of chunks) {
        controller.enqueue(encoder.encode(chunk));
      }
      controller.close();
    },
  });
}

function jsonResponse(body, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'content-type': 'application/json' },
  });
}

function abortError() {
  const error = new Error('aborted');
  error.name = 'AbortError';
  return error;
}

async function test(name, fn) {
  try {
    await fn();
    console.log(`PASS ${name}`);
  } catch (error) {
    console.error(`FAIL ${name}`);
    throw error;
  }
}

await test('consumeSSE 解析 id 行：无 id 帧留空、同帧多个 id 取最后一个', async () => {
  const received = [];
  await consumeSSE(
    sseStream([
      'id: 7\nevent: delta\ndata: {"type":"delta","delta":"a"}\n\n',
      'event: heartbeat\ndata: {"type":"heartbeat"}\n\n',
      'id: 8\nid: 9\nevent: done\ndata: {"type":"done"}\n\n',
    ]),
    (eventName, payload, meta) => received.push({ eventName, payload, meta }),
  );

  assert.equal(received.length, 3);
  assert.equal(received[0].meta.id, '7');
  assert.deepEqual(received[1].meta, {});
  assert.equal(received[2].meta.id, '9');
  assert.equal(received[2].eventName, 'done');
});

await test('无 done 的断流会带 Last-Event-ID 自动重连并补齐事件', async () => {
  sessionStore.clear();
  const calls = [];
  let activeOnRetry = null;

  globalThis.fetch = async (url, init) => {
    calls.push({ url: String(url), init });
    // 注意：续传地址 /api/chat/stream/run 也包含 /api/chat/stream，
    // 这里必须精确匹配建流地址，否则重连请求会被喂回同一份断流。
    if (String(url).endsWith('/api/chat/stream')) {
      // 第一次：给到 run_id / session_id 和一条 delta（seq=1）后连接被掐断。
      return new Response(
        sseStream([
          'event: ready\ndata: {"type":"ready","run_id":"run-1","session_id":"session-1"}\n\n',
          'id: 1\nevent: delta\ndata: {"type":"delta","delta":"第一段","run_id":"run-1","seq":1}\n\n',
        ]),
        { status: 200 },
      );
    }
    // 第二次：重连请求。断言此刻 sessionStorage 里已经记下了这条活跃 run。
    if (activeOnRetry === null) {
      activeOnRetry = sessionStore.get('run:active:session-1') ?? 'MISSING';
    }
    return new Response(
      sseStream([
        'event: ready\ndata: {"type":"ready","run_id":"run-1","session_id":"session-1"}\n\n',
        'id: 2\nevent: delta\ndata: {"type":"delta","delta":"第二段","run_id":"run-1","seq":2}\n\n',
        'id: 3\nevent: done\ndata: {"type":"done","run_id":"run-1","seq":3,"result":{"answer":"ok","session_id":"session-1","used_tools":[]}}\n\n',
      ]),
      { status: 200 },
    );
  };

  const phases = [];
  const deltas = [];
  const outcome = await streamChatRun({
    sessionId: 'session-1',
    message: '你好',
    profile: null,
    onPhase: (phase) => phases.push(phase),
    onEvent: (eventName, payload, meta) => {
      if (eventName === 'delta') {
        deltas.push({ text: payload.delta, id: meta.id });
      }
    },
  });

  assert.equal(
    calls.length,
    2,
    `应发生 1 次重连，实际请求 ${calls.length} 次: ${calls.map((call) => call.url).join(' | ')}`,
  );
  assert.equal(
    activeOnRetry,
    JSON.stringify({ runId: 'run-1', lastSeq: 1 }),
    '重连前应已写入活跃 run 标记',
  );
  const reconnect = calls[1];
  assert.match(reconnect.url, /\/api\/chat\/stream\/run\?run_id=run-1&after=1$/);
  const headers = new Headers(reconnect.init.headers);
  assert.equal(headers.get('Last-Event-ID'), '1');

  assert.deepEqual(deltas, [
    { text: '第一段', id: '1' },
    { text: '第二段', id: '2' },
  ]);
  assert.equal(outcome.done, true);
  assert.equal(outcome.runId, 'run-1');
  assert.equal(outcome.lastSeq, 3);
  assert.ok(phases.includes('reconnecting'));
  assert.ok(phases.includes('reconnected'));
  assert.ok(phases.includes('closed'));
  assert.equal(sessionStore.has('run:active:session-1'), false, '收尾后应清理活跃 run 标记');
});

await test('收到 done 后不再重连', async () => {
  sessionStore.clear();
  let callCount = 0;
  globalThis.fetch = async () => {
    callCount += 1;
    return new Response(
      sseStream([
        'event: ready\ndata: {"type":"ready","run_id":"run-2","session_id":"session-1"}\n\n',
        'id: 1\nevent: done\ndata: {"type":"done","run_id":"run-2","seq":1}\n\n',
      ]),
      { status: 200 },
    );
  };

  const outcome = await streamChatRun({
    sessionId: 'session-1',
    message: '你好',
    profile: null,
    onEvent: () => {},
  });
  assert.equal(callCount, 1);
  assert.equal(outcome.done, true);
});

await test('用户主动中止（AbortError）不触发重连', async () => {
  sessionStore.clear();
  let callCount = 0;
  globalThis.fetch = async () => {
    callCount += 1;
    throw abortError();
  };

  await assert.rejects(
    () =>
      streamChatRun({
        sessionId: 'session-1',
        message: '你好',
        profile: null,
        onEvent: () => {},
      }),
    (error) => error?.name === 'AbortError',
  );
  assert.equal(callCount, 1, '中止后不应再发重连请求');
});

await test('attachRun 从 afterSeq 继续，并带上 Last-Event-ID', async () => {
  sessionStore.clear();
  let requestUrl = '';
  let lastEventId = '';
  globalThis.fetch = async (url, init) => {
    requestUrl = String(url);
    lastEventId = new Headers(init.headers).get('Last-Event-ID');
    return new Response(
      sseStream([
        'id: 5\nevent: delta\ndata: {"type":"delta","delta":"补上的内容","run_id":"run-3","seq":5}\n\n',
        'id: 6\nevent: done\ndata: {"type":"done","run_id":"run-3","seq":6}\n\n',
      ]),
      { status: 200 },
    );
  };

  const seen = [];
  const outcome = await attachRun({
    runId: 'run-3',
    sessionId: 'session-9',
    afterSeq: 4,
    onEvent: (eventName, payload, meta) => seen.push({ eventName, id: meta.id, delta: payload.delta }),
  });

  assert.match(requestUrl, /run_id=run-3&after=4$/);
  assert.equal(lastEventId, '4');
  assert.deepEqual(seen, [
    { eventName: 'delta', id: '5', delta: '补上的内容' },
    { eventName: 'done', id: '6', delta: undefined },
  ]);
  assert.equal(outcome.done, true);
  assert.equal(outcome.lastSeq, 6);
});

await test('run 不可续传（404）时不重试', async () => {
  let callCount = 0;
  globalThis.fetch = async () => {
    callCount += 1;
    return jsonResponse({ code: 400, message: 'run not found' }, 404);
  };
  const outcome = await attachRun({ runId: 'missing', onEvent: () => {} });
  assert.equal(callCount, 1);
  assert.equal(outcome.done, false);
});

await test('readActiveRun / writeActiveRun / clearActiveRun 读写一致', () => {
  sessionStore.clear();
  assert.equal(readActiveRun('session-x'), null);

  writeActiveRun('session-x', { runId: 'run-x', lastSeq: 12 });
  assert.deepEqual(readActiveRun('session-x'), { runId: 'run-x', lastSeq: 12 });

  clearActiveRun('session-x');
  assert.equal(readActiveRun('session-x'), null);

  // 脏数据不应抛错。
  sessionStore.set('run:active:session-bad', '{not json');
  assert.equal(readActiveRun('session-bad'), null);
  sessionStore.set('run:active:session-empty', JSON.stringify({ lastSeq: 3 }));
  assert.equal(readActiveRun('session-empty'), null);
});

console.log('runStream tests passed');
