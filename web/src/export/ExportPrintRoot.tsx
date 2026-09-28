import { createPortal } from 'react-dom';
import { MarkdownBlock } from '../markdown/MarkdownBlock';
import { formatExportTime, PROJECT_ANALYSIS_AGENT, type ExportEntry, type ExportMeta } from './exportDocument';

/**
 * PDF 导出走浏览器打印：这里渲染一份"干净"的文档结构，屏幕上隐藏，
 * 只在 @media print 下显示（见 styles.css 的导出打印样式）。
 *
 * 内容通过 portal 挂到 body：打印时 .app-shell 整块被 display:none 隐藏，
 * 如果这份文档留在外壳内部，会连同外壳一起被隐藏，打出来就是空白页。
 *
 * 回答体刻意复用 .message / .markdown-body 这层既有样式，让打印稿的排版
 * 与页面显示保持一致；交互控件（复制、下载按钮）在打印样式里统一隐藏。
 */
export function ExportPrintRoot({ entries, meta }: { entries: ExportEntry[]; meta: ExportMeta }) {
  const exportedAt = meta.exportedAt ?? new Date();

  return createPortal(
      <div className="export-print-root" id="export-print-root" aria-hidden="true">
        <header className="export-print-header">
          <h1>项目分析报告</h1>
          <p>
            会话 ID：{meta.sessionId || '（未保存会话）'} · 导出时间：{formatExportTime(exportedAt)} · 生成来源：项目分析（{PROJECT_ANALYSIS_AGENT}）
          </p>
        </header>

        {entries.map((entry, index) => (
            <section className="export-print-entry" key={`export-entry-${index}`}>
              <article className="message message-user export-print-question">
                <div className="message-meta">
                  <span className="tool-chip">提问{entries.length > 1 ? ` ${index + 1}` : ''}</span>
                </div>
                <p className="plain-text">{entry.question || '（未记录到对应的用户提问）'}</p>
              </article>

              <article className="message message-assistant export-print-answer">
                <div className="message-meta">
                  <span className="tool-chip">分析{entries.length > 1 ? ` ${index + 1}` : ''}</span>
                </div>
                <MarkdownBlock content={entry.answer} className="timeline-text" />
              </article>
            </section>
        ))}
      </div>,
      document.body,
  );
}
