import { getMessageCopyText, isBlankText } from '../chat/messageSegments';
import type { ChatMessage } from '../types/chat';

/**
 * 触发「项目分析」的 Agent 名称。
 *
 * 仓库分析由 repo_analyzer 子 Agent 完成，父 Agent 只能以工具调用的方式触发它，
 * 所以工具名就是唯一可靠的判定依据。该信号在两条链路上都存在：
 *
 *   1. 实时流：tool_call 事件的 tool_name，以及 done 事件里的 used_tools；
 *   2. 历史回放：render_events 里持久化的 tool_call 事件，回放后同样落到 message.tools。
 *
 * 因此同一个判定函数对当前会话和历史会话都成立，前端无需额外接口。
 */
export const PROJECT_ANALYSIS_AGENT = 'repo_analyzer';

export type ExportFormat = 'md' | 'pdf';

/** 只有项目分析回答（其它对话、工具卡片、项目问答都不算）才允许导出。 */
export function isProjectAnalysisAnswer(message: ChatMessage) {
  return message.role === 'assistant' && message.tools.includes(PROJECT_ANALYSIS_AGENT);
}

/**
 * 可导出 = 项目分析回答 + 已经生成结束 + 确有正文。
 * 之所以要求 status === 'done'：流式生成中途 message.tools 里可能已经出现
 * repo_analyzer（tool_call 事件先到），此时正文只写了一半，导出会得到残缺文档。
 */
export function isExportableMessage(message: ChatMessage) {
  return isProjectAnalysisAnswer(message)
      && message.status === 'done'
      && !isBlankText(getMessageCopyText(message));
}

export type ExportEntry = {
  /** 用户原始提问（纯文本，导出时按 Markdown 引用块转义）。 */
  question: string;
  /** AI 回答（已经是 Markdown 原文）。 */
  answer: string;
  createdAt?: string;
};

export type ExportMeta = {
  sessionId?: string;
  exportedAt?: Date;
};

function findPrecedingQuestion(messages: ChatMessage[], index: number) {
  for (let i = index - 1; i >= 0; i -= 1) {
    if (messages[i].role === 'user') {
      return messages[i].content;
    }
  }
  return '';
}

/** 取单条消息的导出内容：回答本身 + 它上面最近的一条用户提问。 */
export function buildEntryAt(messages: ChatMessage[], index: number): ExportEntry | null {
  const message = messages[index];
  if (!message || !isExportableMessage(message)) {
    return null;
  }
  return {
    question: findPrecedingQuestion(messages, index),
    answer: getMessageCopyText(message),
    createdAt: message.createdAt,
  };
}

export function formatExportTime(date: Date) {
  return date.toLocaleString('zh-CN', { hour12: false });
}

/** 历史消息的时间来自后端；解析失败时原样返回，不要因为一个时间戳让导出失败。 */
function formatRecordTime(value: string | undefined) {
  if (!value) {
    return '';
  }
  const date = new Date(value);
  return Number.isNaN(date.getTime()) ? value : formatExportTime(date);
}

/**
 * 用户提问是纯文本，用户可能会输入 `#`、`|`、`---` 这类会被 Markdown 解析的字符，
 * 直接拼接会破坏文档结构，所以统一转成引用块。
 */
function quoteBlock(text: string) {
  const normalized = text.replace(/\r\n?/g, '\n').trimEnd();
  if (!normalized) {
    return '> （未记录到对应的用户提问）';
  }
  return normalized
    .split('\n')
    .map((line) => (line.trim() ? `> ${line}` : '>'))
    .join('\n');
}

export function buildMarkdownDocument(entries: ExportEntry[], meta: ExportMeta = {}) {
  const exportedAt = meta.exportedAt ?? new Date();
  const lines: string[] = [
    '# 项目分析报告',
    '',
    `> 会话 ID：${meta.sessionId || '（未保存会话）'}`,
    `> 导出时间：${formatExportTime(exportedAt)}`,
    `> 生成来源：Repo Agent · 项目分析（${PROJECT_ANALYSIS_AGENT}）`,
    `> 分析条目：${entries.length} 条`,
    '',
  ];

  entries.forEach((entry, index) => {
    const label = entries.length > 1 ? ` ${index + 1}` : '';
    const recordTime = formatRecordTime(entry.createdAt);
    lines.push(
      '---',
      '',
      `## 提问${label}`,
      '',
      quoteBlock(entry.question),
      '',
      ...(recordTime ? [`> 记录时间：${recordTime}`, ''] : []),
      `## 分析${label}`,
      '',
      entry.answer.trim(),
      '',
    );
  });

  return `${lines.join('\n').trimEnd()}\n`;
}

export function buildExportFilename(meta: ExportMeta = {}, extension: ExportFormat = 'md') {
  const stamp = (meta.exportedAt ?? new Date()).toISOString().replace(/[:.]/g, '-').slice(0, 19);
  const session = (meta.sessionId ?? '').replace(/[^\w-]/g, '').slice(0, 16);
  return `项目分析-${session || 'session'}-${stamp}.${extension}`;
}

/** 与表格 CSV 下载保持一致：加 UTF-8 BOM，避免 Windows 记事本打开中文乱码。 */
export function downloadTextFile(filename: string, text: string, mime = 'text/markdown;charset=utf-8') {
  const blob = new Blob(['\ufeff' + text], { type: mime });
  const url = URL.createObjectURL(blob);
  const link = document.createElement('a');
  link.href = url;
  link.download = filename;
  document.body.appendChild(link);
  link.click();
  document.body.removeChild(link);
  URL.revokeObjectURL(url);
}
