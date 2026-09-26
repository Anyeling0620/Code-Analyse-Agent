import { memo, useCallback, useMemo, useRef, useState } from 'react';
import ReactMarkdown from 'react-markdown';
import remarkGfm from 'remark-gfm';
import remarkBreaks from 'remark-breaks';
import { CodeBlock } from './CodeBlock';
import { MermaidDiagram } from './MermaidDiagram';
import { normalizeMarkdown } from './normalizeMarkdown';

function escapeCsvCell(value: string): string {
  const safeValue = value.replace(/"/g, '""');
  if (/[",\n\r]/.test(value)) {
    return `"${safeValue}"`;
  }
  return safeValue;
}

function TableWithDownload({ children }: { children: React.ReactNode }) {
  const tableRef = useRef<HTMLTableElement>(null);
  const [downloading, setDownloading] = useState(false);

  const handleDownload = useCallback(() => {
    const table = tableRef.current;
    if (!table) return;

    const rows: string[][] = [];
    const collectCells = (cellList: NodeListOf<HTMLTableCellElement>) => {
      const cells: string[] = [];
      cellList.forEach((cell) => cells.push(cell.textContent?.trim() ?? ''));
      return cells;
    };

    table.querySelectorAll('tr').forEach((tr) => {
      const ths = tr.querySelectorAll('th');
      const tds = tr.querySelectorAll('td');
      if (ths.length > 0) {
        rows.push(collectCells(ths));
      } else if (tds.length > 0) {
        rows.push(collectCells(tds));
      }
    });

    if (rows.length === 0) return;

    const csv = rows.map((row) => row.map(escapeCsvCell).join(',')).join('\r\n');
    const blob = new Blob(['﻿' + csv], { type: 'text/csv;charset=utf-8;' });
    const url = URL.createObjectURL(blob);
    const link = document.createElement('a');
    link.href = url;
    link.download = `table-${Date.now()}.csv`;
    document.body.appendChild(link);
    link.click();
    document.body.removeChild(link);
    URL.revokeObjectURL(url);

    setDownloading(true);
    window.setTimeout(() => setDownloading(false), 1200);
  }, []);

  return (
      <div className="markdown-table-wrapper">
        <button
            type="button"
            className="markdown-table-download-button"
            onClick={() => void handleDownload()}
            aria-label="下载 CSV"
            title={downloading ? '已下载' : '下载 CSV'}
        >
          {downloading ? '✓' : '↓'}
        </button>
        <table ref={tableRef} className="markdown-table">
          {children}
        </table>
      </div>
  );
}

function TruncatedCell({ tag, children }: { tag: 'td' | 'th'; children: React.ReactNode }) {
  const [copied, setCopied] = useState(false);

  const extractText = (node: React.ReactNode): string => {
    if (node == null) return '';
    if (typeof node === 'string' || typeof node === 'number') return String(node);
    if (Array.isArray(node)) return node.map(extractText).join('');
    if (typeof node === 'object' && 'props' in node) {
      return extractText((node as React.ReactElement).props?.children);
    }
    return '';
  };

  const handleDoubleClick = useCallback(() => {
    const text = extractText(children);
    if (!text) return;
    navigator.clipboard.writeText(text).then(() => {
      setCopied(true);
      window.setTimeout(() => setCopied(false), 1200);
    }).catch(() => {});
  }, [children]);

  const Tag = tag;
  return (
      <Tag className="markdown-table-cell" onDoubleClick={() => void handleDoubleClick()}>
        <span className="markdown-table-cell-text" title={extractText(children)}>
          {children}
        </span>
        {copied && <span className="markdown-table-cell-copied">已复制</span>}
      </Tag>
  );
}

export const markdownComponents = {
  pre({ children }: any) {
    const child = Array.isArray(children) ? children[0] : children;
    const className = child?.props?.className ?? '';
    const match = /language-([\w-]+)/.exec(className);
    const language = match?.[1] ?? 'text';
    const code = String(child?.props?.children ?? '').replace(/\n$/, '');

    if (language === 'mermaid') {
      return <MermaidDiagram code={code} />;
    }

    return <CodeBlock language={language} code={code} highlighterProps={{}} />;
  },
  code({ className, children, ...props }: any) {
    return (
        <code className={className} {...props}>
          {children}
        </code>
    );
  },
  table({ children }: any) {
    return <TableWithDownload>{children}</TableWithDownload>;
  },
  td({ children }: any) {
    return <TruncatedCell tag="td">{children}</TruncatedCell>;
  },
  th({ children }: any) {
    return <TruncatedCell tag="th">{children}</TruncatedCell>;
  },
};

export const MarkdownBlock = memo(function MarkdownBlock({ content, className }: { content: string; className?: string }) {
  const normalizedContent = useMemo(() => normalizeMarkdown(content), [content]);

  return (
      <div className={`markdown-body${className ? ` ${className}` : ''}`}>
        <ReactMarkdown remarkPlugins={[remarkGfm, remarkBreaks]} components={markdownComponents}>
          {normalizedContent}
        </ReactMarkdown>
      </div>
  );
});
