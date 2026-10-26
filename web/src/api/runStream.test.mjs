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
const {
  streamChatRun,
  attachRun,
  resumeChatRun,
  readActiveRun,
  writeActiveRun,
  clearActiveRun,
  SessionBusyError,
  SESSION_BUSY_MESSAGE,
} = await import('./runStream.ts');

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

// ── 409 SessionBusy（后端同会话串行闸门） ─────────────────────────────

function busyResponse(message = '该会话已有正在生成的回答，请等待完成或先停止', extra = {}) {
  return new Response(
    JSON.stringify({ code: 409, message, data: null, ...extra }),
    { status: 409, headers: { 'content-type': 'application/json' } },
  );
}

await test('409 时优先用 body 里的 run_id 挂载，并从 lastSeq 续接（不抛技术错误串）', async () => {
  sessionStore.clear();
  const calls = [];
  globalThis.fetch = async (url) => {
    const target = String(url);
    calls.push(target);
    if (target.endsWith('/api/chat/stream')) {
      // 同会话已有 run：后端以 409 拒绝，并顺带给出那条 run 的 id。
      return busyResponse(undefined, { data: { run: { run_id: 'busy-run-1', last_seq: 4 } } });
    }
    // 续传请求：从 after 之后回放并收尾。
    return new Response(
      sseStream([
        'id: 5\nevent: delta\ndata: {"type":"delta","delta":"接着刚才的内容","run_id":"busy-run-1","seq":5}\n\n',
        'id: 6\nevent: done\ndata: {"type":"done","run_id":"busy-run-1","seq":6}\n\n',
      ]),
      { status: 200 },
    );
  };

  const deltas = [];
  const outcome = await streamChatRun({
    sessionId: 'session-1',
    message: '重复提交',
    profile: null,
    onEvent: (eventName, payload) => {
      if (eventName === 'delta') {
        deltas.push(payload.delta);
      }
    },
  });

  assert.equal(calls.length, 2, '应只有"建流"+"挂载"两次请求，不应对 409 重试');
  assert.match(calls[1], /\/api\/chat\/stream\/run\?run_id=busy-run-1&after=4$/);
  assert.deepEqual(deltas, ['接着刚才的内容'], '应挂到已有 run 上继续接收');
  assert.equal(outcome.busy, true, 'outcome 应标记这次是被 409 挡下后挂上去的');
  assert.equal(outcome.done, true);
});

// ── 409 挂载游标必须取「body 与本地」的较大值 ────────────────────────────
//
// resolveBusyRun 里是 `Math.max(bodyLastSeq, localSeq)`。上面那条用例只覆盖
// 「body >= 本地」这一半；下面两条锁住另一半，防止将来被改成"只信 body"：
// 一旦退化成只信 body，同一标签页会把自己已经渲染过的内容再回放一遍。

await test('409 挂载游标取较大值：本地游标 9 > body last_seq 4 时用 after=9', async () => {
  sessionStore.clear();
  // 本地已经把这条 run 收到 seq=9（同标签页刚渲染过这批事件）。
  writeActiveRun('session-1', { runId: 'busy-run-1', lastSeq: 9 });

  const calls = [];
  globalThis.fetch = async (url) => {
    const target = String(url);
    calls.push(target);
    if (target.endsWith('/api/chat/stream')) {
      // 服务端视角的游标更旧（last_seq=4）：只信 body 就会重复回放 4→9。
      return busyResponse(undefined, { data: { run: { run_id: 'busy-run-1', last_seq: 4 } } });
    }
    return new Response(
      sseStream([
        'id: 10\nevent: delta\ndata: {"type":"delta","delta":"只该收到这一段","run_id":"busy-run-1","seq":10}\n\n',
        'id: 11\nevent: done\ndata: {"type":"done","run_id":"busy-run-1","seq":11}\n\n',
      ]),
      { status: 200 },
    );
  };

  const deltas = [];
  const outcome = await streamChatRun({
    sessionId: 'session-1',
    message: '重复提交',
    profile: null,
    onEvent: (eventName, payload) => {
      if (eventName === 'delta') {
        deltas.push(payload.delta);
      }
    },
  });

  assert.equal(calls.length, 2, '应只有"建流"+"挂载"两次请求');
  // 必须用本地更大值 9；明确排除 after=4（只信 body）与 after=0（谁都不信）。
  assert.match(calls[1], /\/api\/chat\/stream\/run\?run_id=busy-run-1&after=9$/);
  assert.doesNotMatch(calls[1], /after=4$/, '不能只信 body 的 last_seq，否则会重复回放已渲染内容');
  assert.doesNotMatch(calls[1], /after=0$/, '不能把游标丢掉重头拉');
  assert.deepEqual(deltas, ['只该收到这一段'], '只应收到本地游标之后的增量');
  assert.equal(outcome.busy, true);
  assert.equal(outcome.done, true);
});

await test('409 挂载游标不采信其它 run 的本地游标：runId 不一致时用 body 的 last_seq', async () => {
  sessionStore.clear();
  // sessionStorage 里存的是**另一条** run 的游标（9），绝不能拿它当这条 run 的起点。
  writeActiveRun('session-1', { runId: 'some-other-run', lastSeq: 9 });

  const calls = [];
  globalThis.fetch = async (url) => {
    const target = String(url);
    calls.push(target);
    if (target.endsWith('/api/chat/stream')) {
      return busyResponse(undefined, { data: { run: { run_id: 'busy-run-1', last_seq: 4 } } });
    }
    return new Response(
      sseStream([
        'id: 5\nevent: delta\ndata: {"type":"delta","delta":"从 body 游标续上","run_id":"busy-run-1","seq":5}\n\n',
        'id: 6\nevent: done\ndata: {"type":"done","run_id":"busy-run-1","seq":6}\n\n',
      ]),
      { status: 200 },
    );
  };

  const outcome = await streamChatRun({
    sessionId: 'session-1',
    message: '重复提交',
    profile: null,
    onEvent: () => {},
  });

  // 本地那条是别的 run 的游标（9），必须被忽略，退回 body 的 4。
  assert.match(calls[1], /\/api\/chat\/stream\/run\?run_id=busy-run-1&after=4$/);
  assert.doesNotMatch(calls[1], /after=9$/, '不能采信属于其它 run 的本地游标（防串 run）');
  assert.equal(outcome.busy, true);
});

await test('409 且 body 无 run_id 时回退 GET /api/chat/run/active 定位已有 run', async () => {
  sessionStore.clear();
  const calls = [];
  globalThis.fetch = async (url) => {
    const target = String(url);
    calls.push(target);
    if (target.endsWith('/api/chat/stream')) {
      return busyResponse();
    }
    if (target.includes('/api/chat/run/active')) {
      return jsonResponse({ code: 0, data: { run: { run_id: 'found-run-9', status: 'running', last_seq: 7 } } });
    }
    return new Response(
      sseStream([
        'id: 8\nevent: delta\ndata: {"type":"delta","delta":"回退找到的增量","run_id":"found-run-9","seq":8}\n\n',
        'id: 9\nevent: done\ndata: {"type":"done","run_id":"found-run-9","seq":9}\n\n',
      ]),
      { status: 200 },
    );
  };

  const deltas = [];
  const outcome = await streamChatRun({
    sessionId: 'session-1',
    message: '重复提交',
    profile: null,
    onEvent: (eventName, payload) => {
      if (eventName === 'delta') {
        deltas.push(payload.delta);
      }
    },
  });

  assert.ok(calls.some((url) => url.includes('/api/chat/run/active?session_id=session-1')), '应回退查活跃 run');
  assert.ok(calls.some((url) => /run_id=found-run-9&after=7$/.test(url)), '应按回退查到的游标续接');
  assert.deepEqual(deltas, ['回退找到的增量']);
  assert.equal(outcome.busy, true);
});

await test('409 且定位不到任何 run 时抛 SessionBusyError（带可读中文提示）', async () => {
  sessionStore.clear();
  const calls = [];
  globalThis.fetch = async (url) => {
    const target = String(url);
    calls.push(target);
    if (target.endsWith('/api/chat/stream')) {
      return busyResponse('该会话已有正在生成的回答，请等待完成或先停止');
    }
    if (target.includes('/api/chat/run/active')) {
      return jsonResponse({ code: 0, data: { run: null } });
    }
    throw new Error('不该走到这里：409 不应重试建流');
  };

  await assert.rejects(
    () => streamChatRun({ sessionId: 'session-1', message: '重复提交', profile: null, onEvent: () => {} }),
    (error) => {
      assert.ok(error instanceof SessionBusyError, '应是可识别的 SessionBusyError');
      assert.equal(error.code, 409);
      assert.match(error.userMessage, /正在生成/, '应带服务端给的中文提示');
      assert.doesNotMatch(error.message, /stream request failed/, '绝不能把技术错误串暴露出去');
      return true;
    },
  );
  assert.equal(calls.filter((url) => url.endsWith('/api/chat/stream')).length, 1, '409 不应重试建流');
});

await test('409 时不清空 sessionStorage 里的活跃 run 游标', async () => {
  sessionStore.clear();
  // 本地已经有该会话的游标记录（例如上一次连接就落下的）。
  writeActiveRun('session-1', { runId: 'local-run-3', lastSeq: 11 });
  globalThis.fetch = async (url) => {
    const target = String(url);
    if (target.endsWith('/api/chat/stream')) {
      // body 不带 run_id、active 接口也返回空 → 走本地标记回退。
      return busyResponse();
    }
    if (target.includes('/api/chat/run/active')) {
      return jsonResponse({ code: 0, data: { run: null } });
    }
    return new Response(
      sseStream([
        'id: 12\nevent: done\ndata: {"type":"done","run_id":"local-run-3","seq":12}\n\n',
      ]),
      { status: 200 },
    );
  };

  const outcome = await streamChatRun({
    sessionId: 'session-1',
    message: '重复提交',
    profile: null,
    onEvent: () => {},
  });

  assert.equal(outcome.busy, true);
  assert.ok(outcome.runId === 'local-run-3', '应回退用本地游标记录的那条 run');
  // 收尾（done）后清标记是对的；关键是 409 那一刻没有清。
  assert.equal(sessionStore.has('run:active:session-1'), false, 'done 收尾后清标记');
});

await test('409 不影响 400/404 的"不可续传直接收尾"语义', async () => {
  // 400/404 仍然不重试、不挂载、不抛错。
  for (const status of [400, 404]) {
    let callCount = 0;
    globalThis.fetch = async () => {
      callCount += 1;
      return jsonResponse({ code: 400, message: 'run not found' }, status);
    };
    const outcome = await attachRun({ runId: 'missing', sessionId: 'session-1', onEvent: () => {} });
    assert.equal(callCount, 1, `${status} 不应重试`);
    assert.equal(outcome.done, false);
    assert.notEqual(outcome.busy, true, `${status} 不应被误判成会话忙`);
  }
});

await test('其它非 2xx（如 500）仍抛技术错误串，不被误判成会话忙', async () => {
  globalThis.fetch = async () => jsonResponse({ code: 500, message: 'boom' }, 500);
  await assert.rejects(
    () => streamChatRun({ sessionId: 'session-1', message: 'x', profile: null, onEvent: () => {} }),
    (error) => {
      assert.ok(!(error instanceof SessionBusyError), '500 不能被当成会话忙');
      assert.match(error.message, /stream request failed: 500/);
      return true;
    },
  );
});

await test('resumeChatRun 遇到 409 同样转成可读提示（不抛技术串）', async () => {
  sessionStore.clear();
  globalThis.fetch = async (url) => {
    const target = String(url);
    if (target.endsWith('/api/chat/resume')) {
      return busyResponse('该会话已有正在生成的回答，请等待完成或先停止');
    }
    if (target.includes('/api/chat/run/active')) {
      return jsonResponse({ code: 0, data: { run: null } });
    }
    return jsonResponse({ code: 0, data: { run: null } });
  };

  await assert.rejects(
    () => resumeChatRun({
      sessionId: 'session-1',
      pendingId: 'pending-1',
      approved: true,
      onEvent: () => {},
    }),
    (error) => {
      assert.ok(error instanceof SessionBusyError);
      assert.doesNotMatch(error.message, /stream request failed/);
      return true;
    },
  );
});

// ── 409 接管必须通知调用方"这条 run 不是本次新建的" ─────────────────────
//
// 回归背景：App 在提交时会先插入 user 气泡 + assistant 占位再发请求。
// 若 409 接管不告诉调用方，旧 run 的历史事件会灌进这个"本次才发现的新气泡"，
// 表现为重复渲染与内容错位。onRunIds 的 busy 标记就是这条交接信号。

await test('409 接管时 onRunIds 带 busy:true，且不会先报一个"本次新建"的 run', async () => {
  sessionStore.clear();
  const runIdsCalls = [];
  globalThis.fetch = async (url) => {
    const target = String(url);
    if (target.endsWith('/api/chat/stream')) {
      return busyResponse(undefined, { data: { run: { run_id: 'old-run-7', last_seq: 2 } } });
    }
    return new Response(
      sseStream([
        'id: 3\nevent: delta\ndata: {"type":"delta","delta":"旧 run 的后续","run_id":"old-run-7","seq":3}\n\n',
        'id: 4\nevent: done\ndata: {"type":"done","run_id":"old-run-7","seq":4}\n\n',
      ]),
      { status: 200 },
    );
  };

  const outcome = await streamChatRun({
    sessionId: 'session-1',
    message: '重复提交',
    profile: null,
    onRunIds: (info) => runIdsCalls.push(info),
    onEvent: () => {},
  });

  assert.ok(runIdsCalls.length > 0, 'onRunIds 必须被调用，否则 App 无法废弃占位消息');
  assert.equal(runIdsCalls[0].busy, true, '接管已有 run 时必须显式标记 busy');
  assert.equal(runIdsCalls[0].runId, 'old-run-7', '交给调用方的必须是旧 run 的 id');
  assert.equal(outcome.busy, true);
});

await test('正常建流（非 409）时 onRunIds 不带 busy，调用方保留自己建的占位消息', async () => {
  sessionStore.clear();
  const runIdsCalls = [];
  globalThis.fetch = async () => new Response(
    sseStream([
      'event: ready\ndata: {"type":"ready","run_id":"fresh-run","session_id":"session-1"}\n\n',
      'id: 1\nevent: done\ndata: {"type":"done","run_id":"fresh-run","seq":1}\n\n',
    ]),
    { status: 200 },
  );

  const outcome = await streamChatRun({
    sessionId: 'session-1',
    message: '新提问',
    profile: null,
    onRunIds: (info) => runIdsCalls.push(info),
    onEvent: () => {},
  });

  assert.ok(runIdsCalls.length > 0);
  assert.notEqual(runIdsCalls[0].busy, true, '正常建流不应标 busy');
  assert.equal(runIdsCalls[0].runId, 'fresh-run');
  assert.notEqual(outcome.busy, true);
});

// ── 409 判定必须排除"同族 409"（如邮箱已注册）─────────────────────────
//
// 回归背景：409 不是 SessionBusy 独占。只看状态码会把登录/注册路径的
// EmailRegistered 误判成"会话忙"，进而去查活跃 run、甚至挂到不存在的 run 上。

await test('409 但文案不是会话忙语义时不按 busy 处理（区分 EmailRegistered）', async () => {
  sessionStore.clear();
  let activeRunQueries = 0;
  globalThis.fetch = async (url) => {
    const target = String(url);
    if (target.endsWith('/api/chat/stream')) {
      // 同族 409：码相同，但不是会话忙。
      return new Response(
        JSON.stringify({ code: 409, message: '该邮箱已被注册，请直接登录', data: null }),
        { status: 409, headers: { 'content-type': 'application/json' } },
      );
    }
    if (target.includes('/api/chat/run/active')) {
      activeRunQueries += 1;
      return jsonResponse({ code: 0, data: { run: null } });
    }
    return jsonResponse({ code: 0, data: { run: null } });
  };

  await assert.rejects(
    () => streamChatRun({ sessionId: 'session-1', message: 'x', profile: null, onEvent: () => {} }),
    (error) => {
      assert.ok(!(error instanceof SessionBusyError), '不应被当成会话忙');
      assert.match(error.message, /stream request failed: 409/, '应按普通 409 错误处理');
      return true;
    },
  );
  assert.equal(activeRunQueries, 0, '非会话忙时不应去查活跃 run（更不能挂到不存在的 run 上）');
});

await test('replay 入口（attachRun）收到 409 不按会话忙处理', async () => {
  let activeRunQueries = 0;
  globalThis.fetch = async (url) => {
    if (String(url).includes('/api/chat/run/active')) {
      activeRunQueries += 1;
      return jsonResponse({ code: 0, data: { run: null } });
    }
    return busyResponse();
  };

  await assert.rejects(
    () => attachRun({ runId: 'known-run', sessionId: 'session-1', onEvent: () => {} }),
    (error) => {
      assert.ok(!(error instanceof SessionBusyError), 'replay 的 409 与会话忙无关');
      assert.match(error.message, /stream request failed: 409/);
      return true;
    },
  );
  assert.equal(activeRunQueries, 0, 'replay 入口不应触发会话忙的三级定位');
});

console.log('runStream tests passed');

// 跨会话并行调度的用例（runRegistry / parallelStream）挂在这里一起跑：
// 它们覆盖同一套流式基础能力，这样无需改动 web/package.json 的 test 脚本
// 就能把新用例纳入 `npm run test`。registerHooks 已在文件顶部装好，
// 两个子套件都会自动沿用。
await import('./runRegistry.test.mjs');
await import('./parallelStream.test.mjs');
