import { strict as assert } from 'node:assert';
import { randomUUID } from 'node:crypto';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { build } from 'esbuild';

// 会话列表占位逻辑的单测。沿用仓库既有的 esbuild 内联打包范式：
// 目标模块是纯逻辑（不依赖 React/DOM），所以可以直接在 node 里跑。

const outfile = join(tmpdir(), `sessionList.${randomUUID()}.test.mjs`);

await build({
  stdin: {
    contents: `
      import { strict as assert } from 'node:assert';
      import {
        PENDING_SESSION_PREFIX,
        insertPendingSession,
        isPendingSessionId,
        mergeRemoteWithPending,
        removePendingSession,
        replacePendingSession,
      } from './src/sessions/sessionList.ts';

      const session = (id, extra = {}) => ({
        session_id: id,
        user_id: 'u1',
        title: '',
        summary: '',
        last_user_message: 'hi',
        last_assistant_msg: '',
        update_at: '2026-10-27T00:00:00Z',
        ...extra,
      });

      const NOW = '2026-10-27T01:00:00Z';
      const P1 = PENDING_SESSION_PREFIX + 'aaa';
      const P2 = PENDING_SESSION_PREFIX + 'bbb';

      // ── isPendingSessionId ─────────────────────────────────
      assert.equal(isPendingSessionId(P1), true);
      assert.equal(isPendingSessionId('real-session'), false);
      assert.equal(PENDING_SESSION_PREFIX, 'pending:');

      // ── insertPendingSession ───────────────────────────────
      {
        const out = insertPendingSession([], P1, '第一个问题', 'u1', NOW);
        assert.equal(out.length, 1, '应插入一条');
        assert.equal(out[0].session_id, P1, 'id 应为待定键');
        assert.equal(out[0].last_user_message, '第一个问题', '应带上首问内容');
        assert.equal(out[0].update_at, NOW);
      }

      // 插到最前面（新会话 = 最新）
      {
        const existing = [session('real-1'), session('real-2')];
        const out = insertPendingSession(existing, P1, '问题', 'u1', NOW);
        assert.equal(out.length, 3);
        assert.equal(out[0].session_id, P1, '占位项必须排在最前');
      }

      // 幂等：同 id 不重复插入
      {
        const once = insertPendingSession([], P1, '问题', 'u1', NOW);
        const twice = insertPendingSession(once, P1, '问题', 'u1', NOW);
        assert.equal(twice.length, 1, '不应重复插入');
        assert.equal(twice, once, '幂等时应返回同一引用');
      }

      // 空 key 不插入
      {
        const out = insertPendingSession([session('real-1')], '', '问题', 'u1', NOW);
        assert.equal(out.length, 1);
      }

      // ── replacePendingSession ──────────────────────────────
      // 正常替换：占位 -> 真实 id，位置保持在最前
      {
        const before = [session(P1), session('real-1')];
        const out = replacePendingSession(before, P1, 'server-abc');
        assert.equal(out.length, 2, '数量不变');
        assert.equal(out[0].session_id, 'server-abc', '应替换成真实 id');
        assert.equal(out[0].last_user_message, 'hi', '其它字段应保留');
        assert.equal(out[1].session_id, 'real-1', '其余项不受影响');
      }

      // 真实 id 已存在 -> 丢弃占位，避免重复
      {
        const before = [session(P1), session('server-abc')];
        const out = replacePendingSession(before, P1, 'server-abc');
        assert.equal(out.length, 1, '不应出现两条同 id');
        assert.equal(out[0].session_id, 'server-abc');
      }

      // 列表里没有该占位 -> 原样返回
      {
        const before = [session('real-1')];
        const out = replacePendingSession(before, P1, 'server-abc');
        assert.equal(out, before, '无占位时应返回原引用');
      }

      // 参数异常 -> 原样返回
      {
        const before = [session(P1)];
        assert.equal(replacePendingSession(before, '', 'x'), before);
        assert.equal(replacePendingSession(before, P1, ''), before);
        assert.equal(replacePendingSession(before, P1, P1), before);
      }

      // ── removePendingSession ───────────────────────────────
      {
        const before = [session(P1), session('real-1')];
        const out = removePendingSession(before, P1);
        assert.equal(out.length, 1);
        assert.equal(out[0].session_id, 'real-1');
      }
      // 不存在时返回原引用（避免无谓重渲染）
      {
        const before = [session('real-1')];
        assert.equal(removePendingSession(before, P1), before);
        assert.equal(removePendingSession(before, ''), before);
      }

      // ── mergeRemoteWithPending ─────────────────────────────
      // 无本地占位 -> 直接用服务端列表
      {
        const remote = [session('real-1')];
        const out = mergeRemoteWithPending([session('real-0')], remote);
        assert.equal(out, remote, '无占位时应直接返回服务端列表');
      }

      // 有占位且服务端还没有它 -> 占位保留在最前（核心场景：首问进行中刷新列表）
      {
        const current = [session(P1, { last_user_message: '正在生成的问题' }), session('real-1')];
        const remote = [session('real-1')];
        const out = mergeRemoteWithPending(current, remote);
        assert.equal(out.length, 2, '占位不能被刷掉');
        assert.equal(out[0].session_id, P1);
        assert.equal(out[0].last_user_message, '正在生成的问题', '占位内容要保留');
        assert.equal(out[1].session_id, 'real-1');
      }

      // 服务端已经返回了同 id -> 去重
      {
        const current = [session(P1)];
        const remote = [session(P1)];
        const out = mergeRemoteWithPending(current, remote);
        assert.equal(out.length, 1, '不应出现重复');
      }

      // 多个占位都保留，且顺序保持在服务端列表之前
      {
        const current = [session(P1), session(P2), session('real-1')];
        const remote = [session('real-1'), session('real-2')];
        const out = mergeRemoteWithPending(current, remote);
        assert.equal(out.length, 4);
        assert.deepEqual(
          out.map((i) => i.session_id),
          [P1, P2, 'real-1', 'real-2'],
          '占位在前、服务端项在后',
        );
      }

      // 已替换成真实 id 的不再被当作占位
      {
        const current = [session('server-abc')];
        const remote = [];
        const out = mergeRemoteWithPending(current, remote);
        assert.equal(out.length, 0, '真实 id 不是占位，应完全交给服务端列表');
      }

      console.log('sessionList tests passed');
    `,
    resolveDir: process.cwd(),
    loader: 'ts',
  },
  bundle: true,
  format: 'esm',
  outfile,
  platform: 'node',
  logLevel: 'silent',
});

await import(outfile);
