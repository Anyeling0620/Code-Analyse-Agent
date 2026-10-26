import { strict as assert } from 'node:assert';
import { registerHooks } from 'node:module';

// 被测源码用"无扩展名相对导入"，Node ESM 解析器不接受；这里只在解析失败时补 .ts，
// 让测试直接跑源码、不依赖打包器（与 runStream.test.mjs 同一套路）。
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

const { RunRegistry, MAX_ACTIVE_RUNS } = await import('./runRegistry.ts');

function test(name, fn) {
  try {
    fn();
    console.log(`PASS ${name}`);
  } catch (error) {
    console.error(`FAIL ${name}`);
    throw error;
  }
}

function makeController() {
  const controller = new AbortController();
  let aborted = false;
  const originalAbort = controller.abort.bind(controller);
  controller.abort = () => {
    aborted = true;
    originalAbort();
  };
  return { controller, wasAborted: () => aborted };
}

// T1：跨会话并行 —— 两个会话各自持有句柄，互不覆盖（S1）
test('T1 两个会话各自 begin，句柄互不覆盖且都算 busy', () => {
  const registry = new RunRegistry();
  registry.begin({ sessionId: 'A', runId: 'run-a', assistantId: 'msg-a' });
  registry.begin({ sessionId: 'B', runId: 'run-b', assistantId: 'msg-b' });

  assert.equal(registry.get('A').runId, 'run-a');
  assert.equal(registry.get('B').runId, 'run-b');
  assert.notEqual(registry.get('A'), registry.get('B'));
  assert.equal(registry.isBusy('A'), true);
  assert.equal(registry.isBusy('B'), true);
  assert.deepEqual(registry.busySessions().sort(), ['A', 'B']);
});

// T2：单例 bug 已消除 —— A 收尾不影响 B（S1/S3）
test('T2 A 结束后 B 仍然 busy（不再有"先结束者清空全局"）', () => {
  const registry = new RunRegistry();
  const handleA = registry.begin({ sessionId: 'A', runId: 'run-a', assistantId: 'msg-a' });
  registry.begin({ sessionId: 'B', runId: 'run-b', assistantId: 'msg-b' });

  registry.finish(handleA);

  assert.equal(registry.isBusy('A'), false);
  assert.equal(registry.isBusy('B'), true, 'B 不能被 A 的收尾影响');
});

// T3：同会话串行 —— 二次 begin 被拒，原句柄不被覆盖（S2）
test('T3 同会话二次 begin 被拒且原句柄保持不变', () => {
  const registry = new RunRegistry();
  const original = registry.begin({ sessionId: 'A', runId: 'run-a1', assistantId: 'msg-1' });

  assert.throws(
      () => registry.begin({ sessionId: 'A', runId: 'run-a2', assistantId: 'msg-2' }),
      /已有正在生成的回复/,
      '同会话并发起第二条 run 必须被拒',
  );
  assert.equal(registry.get('A'), original);
  assert.equal(registry.get('A').runId, 'run-a1');
});

// T3b：同一条 run 重复登记是幂等的（重新挂载场景），而不是报错
test('T3b 同一个 runId 重复 begin 复用原句柄（重挂载路径）', () => {
  const registry = new RunRegistry();
  const original = registry.begin({ sessionId: 'A', runId: 'run-a', assistantId: 'msg-1' });
  const again = registry.begin({ sessionId: 'A', runId: 'run-a', assistantId: 'msg-2' });

  assert.equal(again, original);
  assert.equal(again.assistantId, 'msg-1', '重挂载不应换掉已绑定的消息');
});

// T4：切走是 detach 不是 abort（S3）
test('T4 detach 不清句柄、不 abort；abort 才断开传输层', () => {
  const registry = new RunRegistry();
  const { controller, wasAborted } = makeController();
  const handle = registry.begin({ sessionId: 'A', runId: 'run-a', assistantId: 'msg-a', controller });

  registry.markDetached(handle);

  assert.equal(registry.isBusy('A'), true, 'detach 不能让会话变成"没在生成"');
  assert.equal(wasAborted(), false, 'detach 绝不能 abort（执行还在服务端继续）');
  assert.equal(handle.detached, true);
  assert.equal(handle.live, true, '首版保活连接：detach 不断开传输层');

  // 切回来重新接管视口
  registry.markAttached(handle);
  assert.equal(handle.detached, false);
  assert.equal(registry.isBusy('A'), true);

  // 只有用户主动停止才 abort
  registry.abort(handle);
  assert.equal(wasAborted(), true);
  assert.equal(handle.live, false);
});

// T5：游标单调推进，切回时用它做续传起点（S4）
test('T5 lastSeq 单调推进，回退值不会被采纳', () => {
  const registry = new RunRegistry();
  const handle = registry.begin({ sessionId: 'A', runId: 'run-a', assistantId: 'msg-a' });

  registry.touch(handle, 3);
  registry.touch(handle, 7);
  registry.touch(handle, 5); // 重连回放落后事件：不能把起点拉回去

  assert.equal(handle.lastSeq, 7, '续传起点必须是已见最大 seq');
});

// T6：句柄数超上限时按 LRU 释放并 abort（R1，浏览器连接数保护）
test('T6 超过上限时淘汰最久未活动的会话并 abort 其连接', () => {
  const registry = new RunRegistry();
  const first = registry.begin({ sessionId: 's-0', runId: 'run-0', assistantId: 'msg-0' });

  // 依次登记到上限，并保持 s-0 最旧（每次 begin 都会刷新 updatedAt）。
  for (let index = 1; index < MAX_ACTIVE_RUNS; index += 1) {
    registry.begin({ sessionId: `s-${index}`, runId: `run-${index}`, assistantId: `msg-${index}` });
  }
  assert.equal(registry.size(), MAX_ACTIVE_RUNS);

  // 再登记一个：最久未活动的 s-0 被淘汰。
  registry.begin({ sessionId: 's-new', runId: 'run-new', assistantId: 'msg-new' });

  assert.equal(registry.size(), MAX_ACTIVE_RUNS);
  assert.equal(registry.isBusy('s-0'), false, '最久未活动的会话应被释放');
  assert.equal(registry.isBusy('s-new'), true);
  assert.equal(first.live, false);
});

// 会话 id 迁移：新建会话本地是待定键，服务端给出 session_id 后搬过去（S1/S2）
test('rekey 把待定键的句柄搬到真正的 session_id 上', () => {
  const registry = new RunRegistry();
  const handle = registry.begin({ sessionId: 'pending:abc', runId: 'run-a', assistantId: 'msg-a' });

  const moved = registry.rekey('pending:abc', 'sess-1');

  assert.equal(moved, handle);
  assert.equal(handle.sessionId, 'sess-1');
  assert.equal(registry.isBusy('pending:abc'), false);
  assert.equal(registry.isBusy('sess-1'), true, '迁移后仍算 busy，同会话串行继续生效');
});

// 订阅：任何变更都要通知（侧栏/顶栏据此刷新）
test('subscribe 在句柄变化时被通知，cancel 后不再通知', () => {
  const registry = new RunRegistry();
  let calls = 0;
  const cancel = registry.subscribe(() => {
    calls += 1;
  });

  const handle = registry.begin({ sessionId: 'A', runId: 'run-a', assistantId: 'msg-a' });
  assert.ok(calls > 0, 'begin 应通知订阅者');
  const afterBegin = calls;

  registry.touch(handle, 1);
  assert.ok(calls > afterBegin, 'touch 应通知订阅者');

  cancel();
  const afterCancel = calls;
  registry.finish(handle);
  assert.equal(calls, afterCancel, 'cancel 后不应再收到通知');
});

// 释放只删自己的：并行时不会误删别的会话
test('finish 只释放自己的句柄', () => {
  const registry = new RunRegistry();
  const handleA = registry.begin({ sessionId: 'A', runId: 'run-a', assistantId: 'msg-a' });
  registry.begin({ sessionId: 'B', runId: 'run-b', assistantId: 'msg-b' });

  // 用一个"陈旧"的句柄去 finish：不能把同名会话的新 run 删掉。
  registry.finish(handleA);
  const handleB = registry.get('B');
  const staleA = { ...handleA, sessionId: 'B' };
  registry.finish(staleA);

  assert.equal(registry.get('B'), handleB, '陈旧句柄不得释放 B 的句柄');
});

// 审批恢复：同一会话用 takeover 换新句柄，旧句柄不能反过来删掉新句柄
test('takeover 接管后旧句柄被标记 superseded，且释放它不影响新句柄', () => {
  const registry = new RunRegistry();
  const { controller, wasAborted } = makeController();
  const oldHandle = registry.begin({ sessionId: 'A', runId: 'run-a', assistantId: 'msg-a', controller });

  const newHandle = registry.begin({
    sessionId: 'A',
    runId: 'run-a',
    assistantId: 'msg-a',
    takeover: true,
  });

  assert.notEqual(newHandle, oldHandle, 'takeover 必须换新句柄');
  assert.equal(oldHandle.superseded, true, '旧句柄要标记为已被接管');
  assert.equal(wasAborted(), true, '旧句柄的传输层应被断开');
  assert.equal(registry.get('A'), newHandle);

  // 旧句柄的 finally 走到 finish：绝不能把新句柄删掉。
  registry.finish(oldHandle);
  assert.equal(registry.get('A'), newHandle, '旧句柄不得释放接管者的句柄');
  assert.equal(registry.isBusy('A'), true);
});

// abortAll：登出时清理全部连接
test('abortAll 断开全部传输层并清空句柄表', () => {
  const registry = new RunRegistry();
  const { controller, wasAborted } = makeController();
  registry.begin({ sessionId: 'A', runId: 'run-a', assistantId: 'msg-a', controller });
  registry.begin({ sessionId: 'B', runId: 'run-b', assistantId: 'msg-b' });

  registry.abortAll();

  assert.equal(wasAborted(), true);
  assert.equal(registry.size(), 0);
  assert.equal(registry.isBusy('A'), false);
  assert.equal(registry.isBusy('B'), false);
});

console.log('runRegistry tests passed');
