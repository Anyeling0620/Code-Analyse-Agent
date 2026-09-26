import { memo, useMemo } from 'react';
import ReactMarkdown from 'react-markdown';
import remarkGfm from 'remark-gfm';
import remarkBreaks from 'remark-breaks';
import { prettyPayload } from './messageSegments';
import { markdownComponents } from '../markdown/MarkdownBlock';
import { normalizeMarkdown } from '../markdown/normalizeMarkdown';
import type { ToolTrace } from '../types/chat';

const MAX_RESULT_RUNES = 8000;

function truncateResult(raw: string): string {
  const runes = Array.from(raw);
  if (runes.length <= MAX_RESULT_RUNES) {
    return raw;
  }
  return runes.slice(0, MAX_RESULT_RUNES).join('') + '\n\n*(内容过长已截断)*';
}

export const InlineToolCard = memo(function InlineToolCard({ tool }: { tool: ToolTrace }) {
  const normalizedResult = useMemo(
    () => normalizeMarkdown(truncateResult(tool.result), { looseTables: false }),
    [tool.result],
  );

  return (
      <details className="tool-trace-card timeline-tool" open={tool.status === 'calling' || undefined}>
        <summary className="tool-trace-header">
          <div className="tool-trace-title">
            <strong>{tool.name || 'unknown_tool'}</strong>
            <span className={`tool-status tool-status-${tool.status}`}>{tool.status === 'calling' ? '调用中...' : '已完成'}</span>
          </div>
        </summary>
        <div className="tool-trace-content">
          {tool.arguments && (
              <div className="tool-trace-block">
                <span className="tool-trace-label">参数</span>
                <pre className="tool-payload">{prettyPayload(tool.arguments)}</pre>
              </div>
          )}
          <div className="tool-trace-block">
            <span className="tool-trace-label">结果</span>
            {tool.result ? (
                <div className="tool-trace-result markdown-body">
                  <ReactMarkdown remarkPlugins={[remarkGfm, remarkBreaks]} components={markdownComponents}>
                    {normalizedResult}
                  </ReactMarkdown>
                </div>
            ) : (
                <p className="tool-pending">等待工具返回结果...</p>
            )}
          </div>
        </div>
      </details>
  );
});
