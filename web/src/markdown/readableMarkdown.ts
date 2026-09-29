import { normalizeMarkdown } from './normalizeMarkdown';

/**
 * 阅读排版层：把"一大段一大段"的中文长段落拆成更短的阅读单元。
 *
 * 为什么放在渲染层而不是改 message.content：
 * - 复制、导出、落库都用原始 Markdown，避免把排版噪声写进数据；
 * - 只有真正展示给人看的 Markdown 才需要过这一层。
 *
 * 只在"整块都是普通段落文本"时才动手：标题、列表、表格、引用、
 * 代码块、HTML 块一律原样保留，避免破坏 Markdown 结构。
 */

/** 段落超过这个字数才会被拆分（约 2.5 行中文正文）。 */
export const LONG_PARAGRAPH_THRESHOLD = 160;
/** 拆分后每段的目标字数，超过就断段。 */
export const PARAGRAPH_TARGET_LENGTH = 110;
/** 拆分后每段允许的最大字数（单个句子超过它时按逗号再切）。 */
export const PARAGRAPH_MAX_LENGTH = 220;

const FENCE_RE = /^\s*(```|~~~)/;
const BLOCK_START_RE = /^\s*(#{1,6}\s|[-*+]\s|\d+[.)]\s|>|\||<)/;
const DIVIDER_RE = /^\s*(-{3,}|\*{3,}|_{3,})\s*$/;

const CJK_SENTENCE_ENDINGS = new Set(['。', '！', '？', '；', '!', '?', ';', '…']);
const TRAILING_MARKS = new Set(['”', '」', '』', '）', ')', '】', '’', '"', "'"]);
const CLAUSE_DELIMITERS = new Set(['，', ',', '、', '：', ':']);

export function normalizeReadableMarkdown(input: string): string {
  return splitLongParagraphs(normalizeMarkdown(input));
}

/**
 * 把超长的普通段落按句号/分量标点拆成若干短段落。
 * 其余块（标题、列表、表格、代码、引用）保持原样输出。
 */
export function splitLongParagraphs(markdown: string): string {
  if (!markdown) {
    return '';
  }

  const lines = markdown.split('\n');
  const output: string[] = [];
  let inFence = false;
  let index = 0;

  while (index < lines.length) {
    const line = lines[index];

    if (FENCE_RE.test(line)) {
      inFence = !inFence;
      output.push(line);
      index += 1;
      continue;
    }

    if (inFence) {
      output.push(line);
      index += 1;
      continue;
    }

    // 收集一个"空行分隔的块"。
    let end = index;
    const block: string[] = [];
    while (end < lines.length && lines[end].trim() !== '') {
      block.push(lines[end]);
      end += 1;
    }

    if (block.length === 0) {
      output.push(line);
      index += 1;
      continue;
    }

    if (isPlainParagraphBlock(block)) {
      const text = block.map((item) => item.trim()).join('\n');
      if (text.length > LONG_PARAGRAPH_THRESHOLD) {
        const paragraphs = splitParagraphText(text);
        paragraphs.forEach((paragraph, position) => {
          if (position > 0) {
            // 拆出来的段落必须用空行分隔，否则 Markdown 会把它们重新合成一段。
            output.push('');
          }
          output.push(paragraph);
        });
      } else {
        output.push(...block);
      }
    } else {
      output.push(...block);
    }

    index = end;
  }

  return output.join('\n');
}

/** 整块都是普通段落文本时才允许拆分，任何结构化行都会让整块保持原样。 */
function isPlainParagraphBlock(block: string[]) {
  return block.every((line) => {
    const trimmed = line.trim();
    if (!trimmed) {
      return true;
    }
    if (BLOCK_START_RE.test(trimmed) || DIVIDER_RE.test(trimmed)) {
      return false;
    }
    // 段落续行如果是缩进很深的代码/引用（4 空格以上），按结构化内容对待。
    return !line.startsWith('    ');
  });
}

function splitParagraphText(text: string): string[] {
  const sentences: string[] = [];
  for (const sentence of splitSentences(text)) {
    sentences.push(...splitLongClause(sentence));
  }

  const paragraphs: string[] = [];
  let buffer = '';
  for (const sentence of sentences) {
    // 先看合并后会不会超长：超了就把上一段收掉，避免单段越滚越长。
    if (buffer && buffer.length + sentence.length > PARAGRAPH_TARGET_LENGTH) {
      paragraphs.push(buffer);
      buffer = '';
    }
    buffer += sentence;
    if (buffer.length >= PARAGRAPH_TARGET_LENGTH) {
      paragraphs.push(buffer);
      buffer = '';
    }
  }
  if (buffer.trim()) {
    if (paragraphs.length > 0 && buffer.length < 40) {
      paragraphs[paragraphs.length - 1] += buffer;
    } else {
      paragraphs.push(buffer);
    }
  }

  return paragraphs.filter((item) => item.trim() !== '');
}

/** 按句末标点与换行切句，标点跟随前一句，避免出现只有标点的碎片。 */
function splitSentences(text: string): string[] {
  const chars = Array.from(text);
  const sentences: string[] = [];
  let buffer = '';

  for (let i = 0; i < chars.length; i += 1) {
    const char = chars[i];

    if (char === '\n') {
      if (buffer.trim()) {
        sentences.push(buffer.trim());
      }
      buffer = '';
      continue;
    }

    buffer += char;
    const isEnding = CJK_SENTENCE_ENDINGS.has(char) || (char === '.' && isEnglishSentenceEnd(chars, i, buffer));
    if (!isEnding) {
      continue;
    }

    // 连续标点（“！？”、“…”）和收尾引号并入同一句。
    while (i + 1 < chars.length && (CJK_SENTENCE_ENDINGS.has(chars[i + 1]) || TRAILING_MARKS.has(chars[i + 1]))) {
      i += 1;
      buffer += chars[i];
    }

    sentences.push(buffer.trim());
    buffer = '';
  }

  if (buffer.trim()) {
    sentences.push(buffer.trim());
  }

  return sentences.filter(Boolean);
}

/** 英文句点只在"像句尾"时才断句，避免把 1.22 / go.mod 这类内容切开。 */
function isEnglishSentenceEnd(chars: string[], index: number, buffer: string) {
  if (buffer.trim().length < 20) {
    return false;
  }
  const next = chars[index + 1];
  return next === undefined || next === ' ' || next === '\n' || next === '\t';
}

/** 一个句子本身超过上限时，退而用逗号/分号一类停顿符切短。 */
function splitLongClause(sentence: string): string[] {
  if (sentence.length <= PARAGRAPH_MAX_LENGTH) {
    return [sentence];
  }

  const pieces: string[] = [];
  let buffer = '';
  for (const char of Array.from(sentence)) {
    buffer += char;
    if (CLAUSE_DELIMITERS.has(char) && buffer.length >= PARAGRAPH_TARGET_LENGTH) {
      pieces.push(buffer);
      buffer = '';
    }
  }
  if (buffer) {
    pieces.push(buffer);
  }

  // 完全没有停顿符（超长 URL、无标点长串）时不再强切，交给横向滚动/换行处理。
  return pieces.length > 1 ? pieces : [sentence];
}
