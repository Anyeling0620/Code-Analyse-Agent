type NormalizeMarkdownOptions = {
  looseTables?: boolean;
};

export function normalizeMarkdown(input: string, options: NormalizeMarkdownOptions = {}) {
  if (!input) {
    return '';
  }

  const normalized = input
      .replace(/\r\n/g, '\n')
      .replace(/\r/g, '\n')
      .replace(/ /g, ' ')
      .replace(/[ \t]+\n/g, '\n')
      .trimEnd();

  if (options.looseTables === false) {
    return normalized.trimEnd();
  }

  return normalizeLooseMarkdownTables(normalized).trimEnd();
}

function normalizeLooseMarkdownTables(markdown: string) {
  const lines = markdown.split('\n');
  const output: string[] = [];
  let inFence = false;

  for (let i = 0; i < lines.length; ) {
    if (/^\s*(```|~~~)/.test(lines[i])) {
      inFence = !inFence;
      output.push(lines[i]);
      i += 1;
      continue;
    }

    const whitespaceHeader = !inFence ? parseWhitespaceTableCells(lines[i]) : null;
    if (whitespaceHeader) {
      const rows: string[][] = [];
      let nextIndex = i + 1;
      while (nextIndex < lines.length) {
        const cells = parseWhitespaceTableCells(lines[nextIndex]);
        if (!cells || cells.length !== whitespaceHeader.length) break;
        rows.push(cells);
        nextIndex += 1;
      }

      if (rows.length > 0) {
        output.push(`| ${whitespaceHeader.join(' | ')} |`);
        output.push(`| ${Array.from({ length: whitespaceHeader.length }, () => '---').join(' | ')} |`);
        output.push(...rows.map((cells) => `| ${cells.join(' | ')} |`));
        i = nextIndex;
        continue;
      }
    }

    if (inFence || !isPipeTableRow(lines[i])) {
      output.push(lines[i]);
      i += 1;
      continue;
    }

    const rows: string[] = [];
    while (i < lines.length && isPipeTableRow(lines[i])) {
      rows.push(lines[i]);
      i += 1;
    }

    if (rows.length >= 1 && !isMarkdownTableDivider(rows[1] ?? '')) {
      const columnCount = getPipeCells(rows[0]).length;
      if (rows.length >= 2 && !looksLikeDataTableRow(rows[0])) {
        output.push(rows[0]);
        output.push(`| ${Array.from({ length: columnCount }, () => '---').join(' | ')} |`);
        output.push(...rows.slice(1));
        continue;
      }
      output.push(buildLooseTableHeader(columnCount));
      output.push(`| ${Array.from({ length: columnCount }, () => '---').join(' | ')} |`);
    }

    output.push(...rows);
  }

  return output.join('\n');
}

function parseWhitespaceTableCells(line: string) {
  const trimmed = line.trim();
  if (!trimmed || trimmed.startsWith('|')) {
    return null;
  }
  const cells = trimmed.split(/[ \t]{2,}|\t+/).map((cell) => cell.trim()).filter(Boolean);
  return cells.length >= 2 ? cells : null;
}

function isPipeTableRow(line: string) {
  const trimmed = line.trim();
  return trimmed.startsWith('|') && trimmed.endsWith('|') && getPipeCells(trimmed).length >= 2;
}

function getPipeCells(line: string) {
  return line.trim().replace(/^\|/, '').replace(/\|$/, '').split('|').map((cell) => cell.trim());
}

function isMarkdownTableDivider(line: string) {
  const cells = getPipeCells(line);
  return cells.length > 0 && cells.every((cell) => /^:?-{3,}:?$/.test(cell));
}

function looksLikeDataTableRow(row: string) {
  const cells = getPipeCells(row);
  return cells.some((cell) => {
    const normalized = cell.replace(/^`|`$/g, '').trim();
    return /^(GET|POST|PUT|PATCH|DELETE)$/i.test(normalized) || /^\/?[\w.-]+\//.test(normalized) || /^https?:\/\//i.test(normalized);
  });
}

function buildLooseTableHeader(columnCount: number) {
  return `| ${Array.from({ length: columnCount }, (_, index) => `列 ${index + 1}`).join(' | ')} |`;
}
