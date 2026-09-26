import { useCallback, useEffect, useRef, useState } from 'react';

type MermaidModule = { render: (id: string, code: string) => Promise<{ svg: string }> };
type MermaidLoadable = MermaidModule & { initialize: (cfg: Record<string, unknown>) => void };

let mermaidModulePromise: Promise<MermaidModule | null> | null = null;
let mermaidInitialized = false;
const MERMAID_RENDER_VERSION = 'default-theme-v2';

function loadMermaid(): Promise<MermaidModule | null> {
  if (!mermaidModulePromise) {
    mermaidModulePromise = import('mermaid')
        .then((m) => {
          const mod = (m.default ?? m) as MermaidLoadable;
          if (!mermaidInitialized) {
            try {
              mod.initialize({
                startOnLoad: false,
                theme: 'default',
                securityLevel: 'loose',
                fontFamily: '"PingFang SC", "Microsoft YaHei", system-ui, sans-serif',
              });
              mermaidInitialized = true;
            } catch {
            }
          }
          return mod as MermaidModule;
        })
        .catch(() => null);
  }
  return mermaidModulePromise;
}

function downloadBlob(blob: Blob, filename: string) {
  const url = URL.createObjectURL(blob);
  const a = document.createElement('a');
  a.href = url;
  a.download = filename;
  document.body.appendChild(a);
  a.click();
  document.body.removeChild(a);
  URL.revokeObjectURL(url);
}

function downloadSVG(container: HTMLElement) {
  const svgEl = container.querySelector('svg');
  if (!svgEl) return;
  const clone = svgEl.cloneNode(true) as SVGElement;
  const source = svgToDataUrl(clone);
  const blob = new Blob([source], { type: 'image/svg+xml;charset=utf-8' });
  downloadBlob(blob, 'diagram.svg');
}

function svgToDataUrl(svgEl: SVGElement): string {
  const clone = svgEl.cloneNode(true) as SVGElement;
  inlineStyles(svgEl, clone);
  clone.setAttribute('xmlns', 'http://www.w3.org/2000/svg');
  const source = '<?xml version="1.0" encoding="UTF-8"?>\n<!DOCTYPE svg PUBLIC "-//W3C//DTD SVG 1.1//EN" "http://www.w3.org/Graphics/SVG/1.1/DTD/svg11.dtd">\n' +
    new XMLSerializer().serializeToString(clone);
  return source;
}

function inlineStyles(source: Element, target: Element) {
  const computed = window.getComputedStyle(source);
  const styleEl = target.ownerDocument!.createElementNS('http://www.w3.org/2000/svg', 'style');
  const cssProps: string[] = [];
  const copyProps = ['fill', 'stroke', 'stroke-width', 'font-family', 'font-size', 'font-weight', 'font-style', 'text-anchor', 'dominant-baseline', 'opacity'];
  for (const prop of copyProps) {
    const val = computed.getPropertyValue(prop);
    if (val) cssProps.push(`${prop}: ${val}`);
  }
  if (cssProps.length) {
    styleEl.textContent = `g, rect, path, text, line, circle, ellipse, polygon, polyline { ${cssProps.join('; ')} }`;
    target.insertBefore(styleEl, target.firstChild);
  }
  for (let i = 0; i < Math.min(source.children.length, target.children.length); i++) {
    inlineStyles(source.children[i], target.children[i]);
  }
}

function downloadPNG(container: HTMLElement) {
  const svgEl = container.querySelector('svg');
  if (!svgEl) return;
  const source = svgToDataUrl(svgEl);
  const url = 'data:image/svg+xml;base64,' + btoa(unescape(encodeURIComponent(source)));

  const viewBox = svgEl.getAttribute('viewBox');
  let w: number, h: number;
  if (viewBox) {
    const parts = viewBox.split(/\s+/);
    w = parseFloat(parts[2]) || 800;
    h = parseFloat(parts[3]) || 600;
  } else {
    const rect = svgEl.getBoundingClientRect();
    w = rect.width || 800;
    h = rect.height || 600;
  }
  const scale = 2;
  const canvas = document.createElement('canvas');
  canvas.width = w * scale;
  canvas.height = h * scale;
  const ctx = canvas.getContext('2d');
  if (!ctx) return;

  const img = new Image();
  img.onload = () => {
    ctx.fillStyle = '#ffffff';
    ctx.fillRect(0, 0, canvas.width, canvas.height);
    ctx.drawImage(img, 0, 0, canvas.width, canvas.height);
    canvas.toBlob((png) => {
      if (png) downloadBlob(png, 'diagram.png');
    }, 'image/png');
  };
  img.src = url;
}

export function MermaidDiagram({ code }: { code: string }) {
  const [svg, setSvg] = useState('');
  const [errored, setErrored] = useState(false);
  const [menuOpen, setMenuOpen] = useState(false);
  const idRef = useRef(`mermaid-${Math.random().toString(36).slice(2)}`);
  const containerRef = useRef<HTMLDivElement>(null);

  useEffect(() => {
    let cancelled = false;
    setErrored(false);
    const timer = setTimeout(async () => {
      try {
        const mod = await loadMermaid();
        if (!mod) {
          if (!cancelled) setErrored(true);
          return;
        }
        try {
          (mod as MermaidLoadable).initialize({
            startOnLoad: false,
            theme: 'default',
            securityLevel: 'loose',
            fontFamily: '"PingFang SC", "Microsoft YaHei", system-ui, sans-serif',
          });
        } catch {
        }
        const { svg: rendered } = await mod.render(idRef.current, code);
        if (!cancelled) {
          setSvg(rendered);
        }
      } catch {
        if (!cancelled) {
          setErrored(true);
        }
      }
    }, 200);
    return () => {
      cancelled = true;
      clearTimeout(timer);
    };
  }, [code, MERMAID_RENDER_VERSION]);

  const handleDownloadSVG = useCallback(() => {
    if (containerRef.current) downloadSVG(containerRef.current);
    setMenuOpen(false);
  }, []);

  const handleDownloadPNG = useCallback(() => {
    if (containerRef.current) downloadPNG(containerRef.current);
    setMenuOpen(false);
  }, []);

  const handleCopyCode = useCallback(async () => {
    await navigator.clipboard.writeText(code);
    setMenuOpen(false);
  }, [code]);

  useEffect(() => {
    if (!menuOpen) return;
    const close = () => setMenuOpen(false);
    document.addEventListener('click', close);
    return () => document.removeEventListener('click', close);
  }, [menuOpen]);

  if (errored || !svg) {
    return (
        <pre className="mermaid-fallback" style={{
          margin: '14px 0 18px',
          padding: '14px 16px',
          borderRadius: '14px',
          background: 'rgba(20,26,33,0.85)',
          color: '#dce0e6',
          overflowX: 'auto',
          fontFamily: '"JetBrains Mono", "Cascadia Code", "Fira Code", "Consolas", monospace',
          fontSize: '0.88rem',
          lineHeight: 1.7,
        }}>
        <code>{code}</code>
      </pre>
    );
  }

  return (
      <div className="mermaid-diagram" ref={containerRef}>
        <div className="mermaid-download-bar">
          <button
              type="button"
              className="mermaid-download-button"
              onClick={(e) => { e.stopPropagation(); setMenuOpen((v) => !v); }}
              title="下载图表"
          >
            下载
          </button>
          {menuOpen && (
              <div className="mermaid-download-menu">
                <button type="button" onClick={handleDownloadSVG}>下载 SVG</button>
                <button type="button" onClick={handleDownloadPNG}>下载 PNG</button>
                <div className="mermaid-download-menu-divider" />
                <button type="button" onClick={handleCopyCode}>复制代码</button>
              </div>
          )}
        </div>
        <div dangerouslySetInnerHTML={{ __html: svg }} />
      </div>
  );
}
