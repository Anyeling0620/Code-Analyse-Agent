import { memo, useMemo, useState } from 'react';
import ReactMarkdown from 'react-markdown';
import remarkGfm from 'remark-gfm';
import remarkBreaks from 'remark-breaks';
import { prettyPayload } from './messageSegments';
import { markdownComponents } from '../markdown/MarkdownBlock';
import { normalizeMarkdown } from '../markdown/normalizeMarkdown';
import type { ToolTrace } from '../types/chat';
import { ChevronRightIcon } from '../ui/icons';

const MAX_RESULT_RUNES = 8000;

function truncateResult(raw: string): string {
  const runes = Array.from(raw);
  if (runes.length <= MAX_RESULT_RUNES) {
    return raw;
  }
  return runes.slice(0, MAX_RESULT_RUNES).join('') + '\n\n*(内容过长已截断)*';
}

export const InlineToolCard = memo(function InlineToolCard({ tool }: { tool: ToolTrace }) {
  // 一条历史回答可能带几百个工具卡片（真实数据里单条最多 400+ 个）。
  // <details> 只是把内容视觉上收起来，DOM 和 Markdown 解析照样会跑，
  // 所以这里改成"展开才渲染正文"，切换会话时不会再为每个工具结果解析一遍 Markdown。
  const [userOpen, setUserOpen] = useState<boolean | null>(null);
  const defaultOpen = tool.status === 'calling';
  const expanded = userOpen ?? defaultOpen;

  const normalizedResult = useMemo(
    () => (expanded ? normalizeMarkdown(truncateResult(tool.result), { looseTables: false }) : ''),
    [expanded, tool.result],
  );

  function handleToggle(event: React.SyntheticEvent<HTMLDetailsElement>) {
    const next = event.currentTarget.open;
    // 程序化的 open 变化（例如工具执行完成后自动收起）也会触发 toggle，
    // 这类和受控值一致的事件要忽略，只把用户的手动切换记成覆写。
    if (next === expanded) {
      return;
    }
    setUserOpen(next);
  }

  return (
      <details
          className={`tool-card timeline-tool${expanded ? ' tool-card-open' : ''}`}
          open={expanded || undefined}
          onToggle={handleToggle}
      >
        <summary className="tool-card-header">
          <span className="tool-card-chevron" aria-hidden="true"><ChevronRightIcon size={14} /></span>
          <strong className="tool-card-name">{tool.name || 'unknown_tool'}</strong>
          <span className={`tool-status tool-status-${tool.status}`}>
            {tool.status === 'calling' ? (
                <>
                  <span className="live-square" aria-hidden="true" />
                  调用中
                </>
            ) : '已完成'}
          </span>
        </summary>
        {expanded && (
            <div className="tool-card-content">
              {tool.arguments && (
                  <div className="tool-card-block">
                    <span className="tool-card-label">参数</span>
                    <pre className="tool-payload">{prettyPayload(tool.arguments)}</pre>
                  </div>
              )}
              <div className="tool-card-block">
                <span className="tool-card-label">结果</span>
                {tool.result ? (
                    <div className="tool-trace-result markdown-body">
                      <ReactMarkdown remarkPlugins={[remarkGfm, remarkBreaks]} components={markdownComponents}>
                        {normalizedResult}
                      </ReactMarkdown>
                    </div>
                ) : (
                    <p className="tool-pending">
                      <span className="live-square" aria-hidden="true" />
                      等待工具返回结果…
                    </p>
                )}
              </div>
            </div>
        )}
      </details>
  );
});
