import { useState } from 'react';
import { LightAsync as SyntaxHighlighter } from 'react-syntax-highlighter';
import { darcula } from 'react-syntax-highlighter/dist/esm/styles/hljs';

export function CodeBlock({ language, code, highlighterProps }: { language: string; code: string; highlighterProps: any }) {
  const [copied, setCopied] = useState(false);

  async function copyCode() {
    await navigator.clipboard.writeText(code);
    setCopied(true);
    window.setTimeout(() => setCopied(false), 1200);
  }

  return (
      <div className="code-block-shell">
        <button className="code-block-copy-button" type="button" onClick={() => void copyCode()}>
          {copied ? '已复制' : '复制'}
        </button>
        <SyntaxHighlighter
            {...highlighterProps}
            language={language}
            style={darcula}
            PreTag="div"
            className="idea-code-block"
            customStyle={{
              margin: 0,
              borderRadius: 0,
              padding: '18px 78px 18px 18px',
              overflow: 'auto',
              border: 0,
              boxShadow: 'none',
              background: 'transparent',
            }}
            codeTagProps={{
              style: {
                fontFamily: '"JetBrains Mono", "Cascadia Code", "Fira Code", "Consolas", monospace',
                fontSize: '0.92rem',
                lineHeight: 1.75,
              },
            }}
        >
          {code}
        </SyntaxHighlighter>
      </div>
  );
}
