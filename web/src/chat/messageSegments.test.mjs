import { strict as assert } from 'node:assert';
import { randomUUID } from 'node:crypto';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { pathToFileURL } from 'node:url';
import { build } from 'esbuild';

const outfile = join(tmpdir(), `messageSegments.${randomUUID()}.test.mjs`);

await build({
  stdin: {
    contents: `
      import { strict as assert } from 'node:assert';
      import { messageRecordToChatMessage } from './src/chat/messageSegments.ts';
      import { normalizeMarkdown } from './src/markdown/normalizeMarkdown.ts';

      const message = messageRecordToChatMessage({
        id: 1,
        session_id: 'session-1',
        user_id: 'user-1',
        role: 'assistant',
        content: '先说明。中间说明。最后说明。',
        created_at: new Date().toISOString(),
        render_events: [
          { type: 'delta', delta: '先说明。' },
          { type: 'tool_call', tool_name: 'repo_analyzer', tool_call_id: 'call-1', tool_arguments: '{"path":"src"}' },
          { type: 'tool_result', tool_name: 'repo_analyzer', tool_call_id: 'call-1', tool_result: '分析完成' },
          { type: 'delta', delta: '中间说明。' },
          { type: 'tool_call', tool_name: 'project_qa', tool_call_id: 'call-2', tool_arguments: '{"question":"x"}' },
          { type: 'tool_result', tool_name: 'project_qa', tool_call_id: 'call-2', tool_result: '回答完成' },
          { type: 'delta', delta: '最后说明。' },
        ],
      });

      assert.deepEqual(message.segments.map((segment) => segment.type), ['text', 'tool', 'text', 'tool', 'text']);
      assert.equal(message.segments[0].type === 'text' ? message.segments[0].content : '', '先说明。');
      assert.equal(message.segments[2].type === 'text' ? message.segments[2].content : '', '中间说明。');
      assert.equal(message.segments[4].type === 'text' ? message.segments[4].content : '', '最后说明。');

      const legacyMessage = messageRecordToChatMessage({
        id: 2,
        session_id: 'session-1',
        user_id: 'user-1',
        role: 'assistant',
        content: '旧消息正文',
        created_at: new Date().toISOString(),
      });

      assert.deepEqual(legacyMessage.segments.map((segment) => segment.type), ['text']);
      assert.equal(legacyMessage.segments[0].type === 'text' ? legacyMessage.segments[0].content : '', '旧消息正文');

      const tick = String.fromCharCode(96);
      const toolResult = 'type MgAdminUserDoc struct {\\n    ID                primitive.ObjectID ' + tick + 'bson:"_id"' + tick + '\\n    UserName string   ' + tick + 'bson:"user_name"' + tick + ' // 用户名-账号\\n}';
      assert.equal(normalizeMarkdown(toolResult, { looseTables: false }), toolResult);
      assert.match(normalizeMarkdown('姓名  年龄\\n张三  18'), /^\| 姓名 \| 年龄 \|\\n\| --- \| --- \|\\n\| 张三 \| 18 \|$/);
    `,
    resolveDir: process.cwd(),
    loader: 'ts',
  },
  outfile,
  bundle: true,
  platform: 'node',
  format: 'esm',
  logLevel: 'silent',
});

await import(pathToFileURL(outfile).href);
assert.ok(true);
