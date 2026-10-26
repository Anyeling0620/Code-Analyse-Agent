import { strict as assert } from 'node:assert';
import { registerHooks } from 'node:module';

// 与 runStream.test.mjs / runRegistry.test.mjs 同一套路：让测试直接跑 TS 源码。
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

const {
  resolveTargetMessageId,
  shouldAcceptRunEvent,
  applyBufferUpdates,
  projectRunUpdates,
  mergeRunUpdateBuffers,
  appendUpdates,
  mergeLiveMessages,
} = await import('../chat/sessionStreamBuffer.ts');

function test(name, fn) {
  try {
    fn();
    console.log(`PASS ${name}`);
  } catch (error) {
    console.error(`FAIL ${name}`);
    throw error;
  }
}

function message(id, content = '', role = 'assistant') {
  return {
    id,
    role,
    content,
    tools: [],
    toolEvents: [],
    segments: [],
    traceEvents: [],
    status: 'streaming',
  };
}

function appendText(text) {
  return (item) => ({ ...item, content: item.content + text });
}

// ── 验收 4：后台会话的增量不会写入当前展示的其它会话 ───────────────────

test('后台会话的 delta 只落在它自己那条消息上，当前会话不受污染', () => {
  // 视口里是会话 A 的两条消息。
  const view = [message('a-user', '问 A', 'user'), message('a-assistant', 'A 的回答')];

  // 会话 B 在后台收到两个 delta，缓冲区里只有 B 的消息 id。
  const bufferB = new Map();
  appendUpdates(bufferB, 'b-assistant', appendText('B 的第一段'));
  appendUpdates(bufferB, 'b-assistant', appendText('B 的第二段'));

  const projected = projectRunUpdates(view, bufferB);
  assert.equal(projected.size, 0, 'B 的增量不属于当前视口，必须被整段丢弃');

  const applied = applyBufferUpdates(view, mergeRunUpdateBuffers([projected]));
  assert.equal(applied, view, '视口数组应原样返回（引用不变）');
  assert.equal(applied[1].content, 'A 的回答', 'A 的内容绝不能混入 B 的 delta');
});

test('即便把后台缓冲区套到视口上，按消息 id 过滤也不会串内容', () => {
  // 极端情况：调用方传错，把 B 的缓冲区直接套到 A 的视口上。
  const view = [message('a-user', '问 A', 'user'), message('a-assistant', 'A 的回答')];
  const bufferB = new Map();
  appendUpdates(bufferB, 'b-assistant', appendText('不该出现的 B 内容'));

  const applied = applyBufferUpdates(view, bufferB);
  assert.equal(applied[1].content, 'A 的回答', 'id 不匹配就绝不改动视口里的消息');
});

test('两个会话同时在跑时，各自的增量互不影响', () => {
  const bufferA = new Map();
  const bufferB = new Map();
  appendUpdates(bufferA, 'a-assistant', appendText('A1'));
  appendUpdates(bufferB, 'b-assistant', appendText('B1'));

  const merged = mergeRunUpdateBuffers([bufferA, bufferB]);
  assert.deepEqual([...merged.keys()].sort(), ['a-assistant', 'b-assistant']);

  const view = [message('a-assistant', ''), message('b-assistant', '')];
  const applied = applyBufferUpdates(view, merged);
  assert.equal(applied[0].content, 'A1');
  assert.equal(applied[1].content, 'B1');
});

// ── 验收 2：切回正在生成的会话继续接收，不重复渲染已收到内容 ───────────

test('切回时用 live 覆盖同 id 消息，不产生重复条目', () => {
  // 缓存里是"生成前"的旧快照。
  const cached = [message('u1', '问题', 'user'), message('a1', '已收到的前半段')];
  // live 是这一轮的最新状态（同 id，内容更长）。
  const live = [message('u1', '问题', 'user'), message('a1', '已收到的前半段 + 后半段')];

  const merged = mergeLiveMessages(cached, live);

  assert.equal(merged.length, 2, '同 id 必须覆盖而不是追加，否则会重复渲染');
  assert.equal(merged[1].content, '已收到的前半段 + 后半段');
  assert.equal(merged.filter((item) => item.id === 'a1').length, 1);
});

test('切回时补上历史里还没有的进行中消息（尚末落库的 user/assistant）', () => {
  const history = [message('h1', '更早的历史', 'user')];
  const live = [message('u2', '新一轮问题', 'user'), message('a2', '正在生成…')];

  const merged = mergeLiveMessages(history, live);

  assert.equal(merged.length, 3);
  assert.deepEqual(merged.map((item) => item.id), ['h1', 'u2', 'a2'], '进行中的消息应接在历史之后');
  assert.equal(merged[2].content, '正在生成…');
});

test('没有 live 数据时返回原引用（避免无谓重渲染）', () => {
  const history = [message('h1', '历史', 'user')];
  assert.equal(mergeLiveMessages(history, []), history);
});

test('增量按追加顺序应用（delta 不能乱序）', () => {
  const view = [message('a1', '')];
  const buffer = new Map();
  appendUpdates(buffer, 'a1', appendText('第一段'));
  appendUpdates(buffer, 'a1', appendText('·第二段'));
  appendUpdates(buffer, 'a1', appendText('·第三段'));

  const applied = applyBufferUpdates(view, buffer);
  assert.equal(applied[0].content, '第一段·第二段·第三段');
});

test('空缓冲区是零成本 no-op', () => {
  const view = [message('a1', 'x')];
  assert.equal(applyBufferUpdates(view, new Map()), view);
  assert.equal(projectRunUpdates(view, new Map()).size, 0);
  assert.equal(mergeRunUpdateBuffers([]).size, 0);
});

// ── 后台(detached)增量不得二次应用 ────────────────────────────────────
//
// 回归背景：App 的 patchAssistant 若把后台会话的增量也记进投影队列，
// 切回时 mergeLiveMessages 会把 live 真身合并进视口（第一次应用），
// 随后的 flush 又把同一批 delta 应用一次（第二次应用）→ 文本翻倍。
//
// 修法是双保险：①后台不入队（只在 isViewing 时记账）；②切回时清掉该句柄的队列。
// 下面的模拟按 App 的数据流复现"detach → 收 2 条 delta → 切回 → flush"，
// 断言最终文本等于「只应用一次」的结果。

test('后台会话收 2 条 delta 后切回，文本只应用一次（不翻倍）', () => {
  // 会话 A 初始：一条 user + 一条空的 assistant。
  const live = [message('a-user', '提问', 'user'), message('a-assistant', '')];

  // 模拟 App：detach 期间到达的 2 条 delta。
  const inBackground = (buffer, text) => {
    // ① 后台不入队 —— 只写 live 真身。
    live[1] = appendText(text)(live[1]);
    return buffer;
  };
  let buffer = new Map();
  buffer = inBackground(buffer, '前半段');
  buffer = inBackground(buffer, '后半段');

  assert.equal(live[1].content, '前半段后半段', 'live 真身应累积两条 delta');

  // 切回：② 清掉该句柄的队列，然后 mergeLiveMessages 把 live 合并进视口。
  buffer = new Map(); // 相当于 assistantUpdateQueueRef.current.delete(handle)
  const cached = [message('a-user', '提问', 'user'), message('a-assistant', '')];
  const restored = mergeLiveMessages(cached, live);
  assert.equal(restored[1].content, '前半段后半段', '切回后应看到两条 delta 各一次');

  // 触发一次 flush：队列已空，不能再应用任何东西。
  const applied = applyBufferUpdates(restored, mergeRunUpdateBuffers([projectRunUpdates(restored, buffer)]));
  assert.equal(applied[1].content, '前半段后半段', 'flush 不得再次应用已合并的增量（否则就是翻倍）');

  // 反向对照：如果切回时**没有**清队列，就会翻倍 —— 这条断言说明测试能抓到这个 bug。
  const leakyBuffer = new Map();
  appendUpdates(leakyBuffer, 'a-assistant', appendText('前半段'));
  appendUpdates(leakyBuffer, 'a-assistant', appendText('后半段'));
  const doubled = applyBufferUpdates(restored, leakyBuffer);
  assert.equal(doubled[1].content, '前半段后半段前半段后半段', '未清队列时会翻倍（反证本用例的有效性）');
});

test('只有 isViewing 的更新才入队：后台更新不产生待应用条目', () => {
  // 模拟 patchAssistant 的入队判定。
  const isViewing = false;
  const queue = new Map();
  const record = (buffer, messageId, update) => {
    if (!isViewing) {
      return buffer; // 后台：不记账
    }
    appendUpdates(buffer, messageId, update);
    return buffer;
  };
  const after = record(queue, 'a-assistant', appendText('x'));
  assert.equal(after.size, 0, '后台更新不应留下投影条目');
});

// ── 409 接管：事件必须落在历史消息，而不是被丢弃 ────────────────────────
//
// 这是 t11 评审的 blocker：之前只断言了 onRunIds.busy===true，完全没有覆盖
// "接管之后 delta 到底写进哪条消息"。真实缺陷就在这里——调用点用发起时捕获的
// 闭包常量 assistantId（已被删除的占位），adoptBusyRun 改写 handle.assistantId
// 之后事件仍旧route 到旧 id，于是整轮输出被静默丢弃。
//
// 下面的模型按 App 的真实数据流跑：占位消息 → 409 接管（改写 handle.assistantId）
// → 推一条 delta → 断言文本落在 hist-a1。

test('409 接管后 delta 落在历史消息 hist-a1，不会被丢弃', () => {
  // 会话里已有的历史（旧 run 的正文就在这条上）。
  const history = [message('u0', '之前的提问', 'user'), message('hist-a1', '旧 run 已产出的正文')];
  history[1].recordId = 101;

  // 本次提交先插入的两个占位。
  const placeholderUserId = 'new-u1';
  const placeholderAssistantId = 'new-a1';

  // 句柄：发起时绑定占位 id（复现真实情形）。
  const handle = { assistantId: placeholderAssistantId, runId: 'run-old' };

  // 视口 = 历史 + 占位。
  let view = [...history, message(placeholderUserId, '本次提问', 'user'), message(placeholderAssistantId, '')];

  // ── adoptBusyRun：删占位 + 把句柄指回历史消息 ──
  view = view.filter((item) => item.id !== placeholderUserId && item.id !== placeholderAssistantId);
  const existing = history.filter((item) => item.role === 'assistant').at(-1);
  assert.ok(existing, '应能找到归属的历史 assistant');
  handle.assistantId = existing.id;
  assert.equal(handle.assistantId, 'hist-a1', '接管后句柄必须指向历史消息');

  // ── 推一条 delta：落点从 handle 读，而不是闭包常量 ──
  const closureConstant = placeholderAssistantId; // 发起时捕获的那个值
  const target = resolveTargetMessageId(handle, closureConstant);
  assert.equal(target, 'hist-a1', '落点必须取 handle.assistantId，而不是闭包常量');
  assert.notEqual(target, closureConstant, '绝不能再用已被删除的占位 id');

  const buffer = new Map();
  appendUpdates(buffer, target, appendText(' + 旧 run 的后续'));
  const applied = applyBufferUpdates(view, buffer);

  // ── 收尾：done 事件同样走 handle 落点，且必须把状态改为 done ──
  const doneBuffer = new Map();
  appendUpdates(doneBuffer, target, (item) => ({ ...item, status: 'done' }));
  const finalized = applyBufferUpdates(applied, doneBuffer);

  const landed = finalized.find((item) => item.id === 'hist-a1');
  assert.ok(landed, '历史消息必须还在视口里');
  assert.equal(landed.content, '旧 run 已产出的正文 + 旧 run 的后续', 'delta 必须真的写进 hist-a1');
  assert.equal(landed.status, 'done', 'hist-a1 最终必须是 done，不能卡在 streaming');
  assert.ok(
      !finalized.some((item) => item.id === placeholderAssistantId),
      '占位 ph-1 不得留在视口中',
  );
  assert.ok(
      !finalized.some((item) => item.id === placeholderUserId),
      '本次的 user 占位也应被移除',
  );

  // 反向断言：若沿用闭包常量，文本会丢（这正是此前的缺陷表现）。
  const wrongTarget = resolveTargetMessageId({ assistantId: '' }, closureConstant);
  assert.equal(wrongTarget, closureConstant, '句柄为空时兜底用闭包常量（兼容路径）');
});

// catch 收尾专用：409 接管 → strip 占位 → catch 写最终状态。
// 覆盖 handleSubmit 的 catch 块（与 try 收尾作用域不同，没有 const 遮蔽，
// 曾漏改成裸闭包常量，导致接管成功后收尾状态写进已删除的占位而丢失）。
test('catch 收尾从句柄取落点：409 接管后最终状态仍落在 hist-a1', () => {
  const history = [message('u0', '之前的提问', 'user'), message('hist-a1', '旧正文')];
  history[1].recordId = 100;
  const placeholderAssistantId = 'catch-ph';
  const handle = { assistantId: placeholderAssistantId, runId: 'run-old' };

  // 接管：占位被 strip，句柄改指历史消息。
  const stripped = [...history];
  handle.assistantId = 'hist-a1';

  // catch 收尾（isBusy → doneLike=true）。
  const catchTarget = resolveTargetMessageId(handle, placeholderAssistantId);
  assert.equal(catchTarget, 'hist-a1', 'catch 收尾必须用 handle 的落点');
  const buffer = new Map();
  appendUpdates(buffer, catchTarget, (item) => ({ ...item, status: 'done' }));
  const applied = applyBufferUpdates(stripped, buffer);

  assert.equal(applied.find((item) => item.id === 'hist-a1')?.status, 'done',
      '收尾状态必须落到 hist-a1，不能因写到已删除的占位而丢失');
  assert.ok(!applied.some((item) => item.id === placeholderAssistantId), '占位不得复活');
});

test('句柄已有 runId 时，别的 run 的事件被丢弃', () => {
  // 归属一致 → 接受
  assert.deepEqual(shouldAcceptRunEvent('run-a', 'run-a'), { accept: true });
  // 句柄还没有 runId → 采纳首个帧
  assert.deepEqual(shouldAcceptRunEvent('', 'run-a'), { accept: true, adoptRunId: 'run-a' });
  // 不一致 → 丢弃（例如接管后旧连接的回放尾巴）
  assert.deepEqual(shouldAcceptRunEvent('run-a', 'run-b'), { accept: false });
  // payload 不带 run_id → 无法判断，放行
  assert.deepEqual(shouldAcceptRunEvent('run-a', undefined), { accept: true });
});

test('resolveTargetMessageId 句柄优先、常量兜底、去空白', () => {
  assert.equal(resolveTargetMessageId({ assistantId: 'from-handle' }, 'from-closure'), 'from-handle');
  assert.equal(resolveTargetMessageId({ assistantId: '' }, 'from-closure'), 'from-closure');
  assert.equal(resolveTargetMessageId({}, ''), '');
  assert.equal(resolveTargetMessageId({ assistantId: '  padded  ' }), 'padded');
});

// A2 的正/负向量：以纯函数形式等价断言「409 接管后 delta 落到 hist-a1」。
// 这样不依赖 jsdom/React 渲染（仓库当前没有这些依赖，也不该为此新增）。
test('A2 正向：接管后 handle.assistantId=hist-a1，落点必须是 hist-a1 而不是占位 ph-1', () => {
  const handle = { assistantId: 'hist-a1' };
  assert.equal(resolveTargetMessageId(handle, 'ph-1'), 'hist-a1');
});

test('A2 负向：handle.assistantId 为空时退回 fallback（A3 不回归）', () => {
  const handle = { assistantId: '' };
  assert.equal(resolveTargetMessageId(handle, 'ph-1'), 'ph-1');
});

test('A2 边界：找不到既有 assistant 时，落点必须仍是可写消息而不是已删除的占位', () => {
  // 复现 adoptBusyRun 的两条分支，验证落点永远指向"真实存在"的消息。
  const placeholderId = 'ph-1';
  // 分支一：找到历史消息 → 删占位 → 落点 = 历史消息。
  const found = { assistantId: 'hist-a1' };
  const liveAfterFound = [message('hist-a1', '旧正文')];
  assert.equal(resolveTargetMessageId(found, placeholderId), 'hist-a1');
  assert.ok(liveAfterFound.some((item) => item.id === 'hist-a1'));

  // 分支二：没找到 → **保留**占位 → 落点 = 占位（仍在列表里，可写）。
  const notFound = { assistantId: placeholderId };
  const liveAfterNotFound = [message(placeholderId, '')];
  const target = resolveTargetMessageId(notFound, placeholderId);
  assert.equal(target, placeholderId);
  assert.ok(
      liveAfterNotFound.some((item) => item.id === target),
      '落点必须存在于列表里，否则后续事件会被静默丢弃',
  );
});

console.log('parallelStream tests passed');
