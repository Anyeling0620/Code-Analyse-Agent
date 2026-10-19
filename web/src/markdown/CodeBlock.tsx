import { useState, type CSSProperties } from 'react';
import { LightAsync as SyntaxHighlighter } from 'react-syntax-highlighter';
import { CheckIcon, CopyIcon } from '../ui/icons';

// 代码块只用一套克制的机器腔配色：铬黄关键字、草绿字符串、赭石数字，
// 其余保持乳白墨色。全部在深墨色底上，和手册的“机器声音”一致。
const manualCodeTheme: Record<string, CSSProperties> = {
  hljs: { display: 'block', overflowX: 'auto', background: 'transparent', color: '#E9E4D6' },
  'hljs-keyword': { color: '#E0B11F' },
  'hljs-built_in': { color: '#7FB4CA' },
  'hljs-type': { color: '#7FB4CA' },
  'hljs-literal': { color: '#E0B11F' },
  'hljs-number': { color: '#D9906A' },
  'hljs-string': { color: '#A7C080' },
  'hljs-regexp': { color: '#A7C080' },
  'hljs-comment': { color: '#8B9084', fontStyle: 'italic' },
  'hljs-doctag': { color: '#8B9084' },
  'hljs-title': { color: '#D6A0A0' },
  'hljs-section': { color: '#D6A0A0' },
  'hljs-name': { color: '#D6A0A0' },
  'hljs-attr': { color: '#7FB4CA' },
  'hljs-attribute': { color: '#7FB4CA' },
  'hljs-variable': { color: '#E9E4D6' },
  'hljs-template-variable': { color: '#E9E4D6' },
  'hljs-tag': { color: '#8B9084' },
  'hljs-symbol': { color: '#D9906A' },
  'hljs-bullet': { color: '#D9906A' },
  'hljs-meta': { color: '#C9C5B7' },
  'hljs-deletion': { color: '#E07A6A', background: 'rgba(207,59,42,.12)' },
  'hljs-addition': { color: '#A7C080', background: 'rgba(95,127,63,.14)' },
  'hljs-emphasis': { fontStyle: 'italic' },
  'hljs-strong': { fontWeight: '600' },
};

export function CodeBlock({ language, code, highlighterProps }: { language: string; code: string; highlighterProps: any }) {
  const [copied, setCopied] = useState(false);

  async function copyCode() {
    await navigator.clipboard.writeText(code);
    setCopied(true);
    window.setTimeout(() => setCopied(false), 1200);
  }

  return (
      <div className="code-block-shell">
        <div className="code-block-bar">
          <span className="code-block-language">{language || 'text'}</span>
          <button className="code-block-copy-button" type="button" onClick={() => void copyCode()}>
            {copied ? <CheckIcon size={13} /> : <CopyIcon size={13} />}
            {copied ? '已复制' : '复制'}
          </button>
        </div>
        <SyntaxHighlighter
            {...highlighterProps}
            language={language}
            style={manualCodeTheme}
            PreTag="div"
            className="idea-code-block"
            customStyle={{
              margin: 0,
              borderRadius: 0,
              padding: '16px 18px',
              overflow: 'auto',
              border: 0,
              boxShadow: 'none',
              background: 'transparent',
            }}
            codeTagProps={{
              style: {
                fontFamily: '"Azeret Mono", "JetBrains Mono", "SFMono-Regular", Consolas, monospace',
                fontSize: '0.84rem',
                lineHeight: 1.7,
              },
            }}
        >
          {code}
        </SyntaxHighlighter>
      </div>
  );
}
