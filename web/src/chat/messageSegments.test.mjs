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
      import { messageRecordToChatMessage, resolveStreamEventType } from './src/chat/messageSegments.ts';
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

      // 旧版后端把工具结果也标成 tool_call，只靠 tool_result 字段区分（历史数据同样如此），
      // 这类事件必须按 tool_result 处理，否则结果渲染不出来、状态停在“调用中”。
      assert.equal(resolveStreamEventType('tool_call', { type: 'tool_call', tool_result: '' }), 'tool_call');
      assert.equal(resolveStreamEventType('tool_call', { type: 'tool_call', tool_result: 'ok' }), 'tool_result');
      assert.equal(resolveStreamEventType('tool_result', { type: 'tool_result', tool_result: '' }), 'tool_result');

      const legacyToolCallShape = messageRecordToChatMessage({
        id: 3,
        session_id: 'session-1',
        user_id: 'user-1',
        role: 'assistant',
        content: '最终回答',
        created_at: new Date().toISOString(),
        render_events: [
          { type: 'tool_call', tool_name: 'read_files', tool_call_id: 'call-9', tool_arguments: '{"path":"a.go"}' },
          { type: 'tool_call', tool_name: 'read_files', tool_call_id: 'call-9', tool_result: 'package main' },
        ],
      });
      const legacyToolSegments = legacyToolCallShape.segments.filter((segment) => segment.type === 'tool');
      assert.equal(legacyToolSegments.length, 1);
      assert.equal(legacyToolSegments[0].tool.status, 'done');
      assert.equal(legacyToolSegments[0].tool.result, 'package main');

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
