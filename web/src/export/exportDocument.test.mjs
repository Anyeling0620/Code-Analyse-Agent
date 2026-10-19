import { strict as assert } from 'node:assert';
import { randomUUID } from 'node:crypto';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { pathToFileURL } from 'node:url';
import { build } from 'esbuild';

const outfile = join(tmpdir(), `exportDocument.${randomUUID()}.test.mjs`);

await build({
  stdin: {
    contents: `
      import { strict as assert } from 'node:assert';
      import { buildEntryAt, isExportableMessage, isProjectAnalysisAnswer } from './src/export/exportDocument.ts';

      function message(overrides = {}) {
        return {
          id: 'm1',
          role: 'assistant',
          content: '分析正文',
          tools: [],
          toolEvents: [],
          segments: [],
          traceEvents: [],
          status: 'done',
          ...overrides,
        };
      }

      assert.equal(isProjectAnalysisAnswer(message({ tools: ['repo_analyzer'] })), true);
      // 轻量历史消息没有回放事件，tools 为空，只能靠后端探测标记。
      assert.equal(isProjectAnalysisAnswer(message({ hasProjectAnalysis: true })), true);
      assert.equal(isProjectAnalysisAnswer(message({ tools: ['project_qa'] })), false);
      assert.equal(isProjectAnalysisAnswer(message({ role: 'user', hasProjectAnalysis: true })), false);

      assert.equal(isExportableMessage(message({ tools: ['repo_analyzer'] })), true);
      assert.equal(isExportableMessage(message({ hasProjectAnalysis: true })), true);
      assert.equal(isExportableMessage(message({ hasProjectAnalysis: true, status: 'streaming' })), false);
      assert.equal(isExportableMessage(message({ hasProjectAnalysis: true, content: '   ' })), false);
      assert.equal(isExportableMessage(message({ tools: ['project_qa'] })), false);

      const messages = [
        message({ id: 'q', role: 'user', content: '请分析这个仓库' }),
        message({ id: 'a', content: '分析正文', hasProjectAnalysis: true }),
      ];
      const entry = buildEntryAt(messages, 1);
      assert.ok(entry);
      assert.equal(entry.question, '请分析这个仓库');
      assert.equal(entry.answer, '分析正文');
      assert.equal(buildEntryAt(messages, 0), null);
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
