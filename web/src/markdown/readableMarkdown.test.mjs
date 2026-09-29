import { strict as assert } from 'node:assert';
import { randomUUID } from 'node:crypto';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { pathToFileURL } from 'node:url';
import { build } from 'esbuild';

const outfile = join(tmpdir(), `readableMarkdown.${randomUUID()}.test.mjs`);

await build({
  stdin: {
    contents: `
      import { strict as assert } from 'node:assert';
      import { normalizeReadableMarkdown, splitLongParagraphs } from './src/markdown/readableMarkdown.ts';

      // 报告正文里的典型长段落：四个长句拼成一段，远超拆分阈值。
      const longParagraph = [
        '这个项目采用典型的分层架构，入口文件负责装配依赖并启动 HTTP 服务，路由层只做参数校验和响应封装，业务逻辑全部下沉到 service 层。',
        '服务层承担业务编排，agent 层负责与模型交互，工具层封装只读的仓库读取能力，每一层都通过接口隔离具体实现，方便替换和补测试。',
        '数据层通过 GORM 访问数据库，会话与消息记录落库后由前端按时间倒序分页读取，默认第一页返回最新的若干条记录以降低首屏延迟。',
        '配置与中间件负责鉴权、配额和日志，整体依赖方向是单向的，配置项集中在 config 目录，避免散落在业务代码里。',
      ].join('');

      const longerParagraph = longParagraph + [
        '最终报告会按固定的八个章节输出，每个章节都要给出已确认事实、推断和建议三部分内容，并标注证据来源。',
        '如果某一节证据不足，需要在该节明确写出未确认，而不是用笼统的描述把结论糊过去。',
      ].join('');

      const segments = splitLongParagraphs(longerParagraph).split('\\n\\n');
      assert.ok(segments.length >= 3, '超长段落必须被拆成多段，实际 ' + segments.length + ' 段');
      assert.ok(segments.every((item) => item.length <= 260), '拆出的每段都要控制在可读长度内');

      // 拆分只是插入段落分隔，不能吞字或改字。
      assert.equal(
        normalizeReadableMarkdown(longerParagraph).replace(/\\s/g, ''),
        longerParagraph.replace(/\\s/g, ''),
      );

      const short = '这是一句很短的说明。';
      assert.equal(splitLongParagraphs(short), short, '短段落不应被改动');

      const twoLine = '这一段的长度大约是一百五十字上下，用于确认两行以内的说明不会被拆分。'.repeat(3);
      assert.equal(splitLongParagraphs(twoLine), twoLine, '未超过阈值的段落不应被改动');

      // 结构化内容一律原样保留，否则会写坏 Markdown。
      const fenced = ['## 一、概览', '', '```go', 'func main() { // 这一段注释很长但属于代码块内容，必须原样保留', '}', '```'].join('\\n');
      assert.equal(splitLongParagraphs(fenced), fenced, '代码块必须原样保留');

      const table = ['| 分类 | 技术 |', '| ---- | ---- |', '| 语言 | Go 1.26 |'].join('\\n');
      assert.equal(splitLongParagraphs(table), table, '表格必须原样保留');

      const list = ['- 第一条说明，这一条比较长，用来确认列表项不会被段落拆分逻辑改写成多段内容。', '- 第二条说明'].join('\\n');
      assert.equal(splitLongParagraphs(list), list, '列表必须原样保留');

      const mixed = ['## 四、核心调用链路', '', longerParagraph].join('\\n');
      const mixedResult = splitLongParagraphs(mixed);
      assert.ok(mixedResult.startsWith('## 四、核心调用链路\\n\\n'), '标题行必须保留');
      assert.ok(mixedResult.split('\\n\\n').length >= 4, '标题下的长段落仍应被拆分');
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
