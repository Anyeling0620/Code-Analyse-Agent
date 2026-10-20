import { useCallback, useEffect, useLayoutEffect, useRef, useState } from 'react';
import { createPortal } from 'react-dom';
import { ChevronDownIcon, CloseIcon, CopyIcon, DownloadIcon } from '../ui/icons';

type MermaidModule = { render: (id: string, code: string) => Promise<{ svg: string }> };
type MermaidLoadable = MermaidModule & { initialize: (cfg: Record<string, unknown>) => void };

let mermaidModulePromise: Promise<MermaidModule | null> | null = null;
let mermaidInitialized = false;
const MERMAID_RENDER_VERSION = 'manual-acetate-v1';

// 图表也走手册的纸面系统：纸色节点、石墨线、Archivo 标签。
// 不引用外部主题样式，Mermaid 的变量在这里一次写死。
const MERMAID_THEME_VARIABLES = {
  background: '#f8f5ec',
  primaryColor: '#f4f1e6',
  primaryTextColor: '#1b2024',
  primaryBorderColor: '#54584f',
  lineColor: '#54584f',
  secondaryColor: '#eae5d6',
  tertiaryColor: '#f8f5ec',
  mainBkg: '#f4f1e6',
  secondBkg: '#eae5d6',
  tertiaryBkg: '#f8f5ec',
  nodeBorder: '#54584f',
  clusterBkg: '#f8f5ec',
  clusterBorder: '#c9c5b7',
  edgeLabelBackground: '#f4f1e6',
  textColor: '#1b2024',
  fontFamily: '"Archivo", "PingFang SC", "Microsoft YaHei", sans-serif',
  fontSize: '14px',
  actorBkg: '#f4f1e6',
  actorBorder: '#54584f',
  actorTextColor: '#1b2024',
  signalColor: '#54584f',
  signalTextColor: '#1b2024',
  labelBoxBkgColor: '#f4f1e6',
  labelBoxBorderColor: '#54584f',
  labelTextColor: '#1b2024',
  loopTextColor: '#1b2024',
  noteBkgColor: '#e0b11f',
  noteTextColor: '#1b2024',
  noteBorderColor: '#54584f',
};

const MERMAID_CONFIG = {
  startOnLoad: false,
  theme: 'base',
  securityLevel: 'loose',
  fontFamily: '"Archivo", "PingFang SC", "Microsoft YaHei", sans-serif',
  themeVariables: MERMAID_THEME_VARIABLES,
};

function loadMermaid(): Promise<MermaidModule | null> {
  if (!mermaidModulePromise) {
    mermaidModulePromise = import('mermaid')
        .then((m) => {
          const mod = (m.default ?? m) as MermaidLoadable;
          if (!mermaidInitialized) {
            try {
              mod.initialize(MERMAID_CONFIG);
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

type Size = { w: number; h: number };

const ZOOM_PADDING = 20;
const ZOOM_MIN_SCALE = 0.1;
const ZOOM_MAX_SCALE = 5;
const ZOOM_FOCUSABLE = 'button:not([disabled]), [href], [tabindex]:not([tabindex="-1"])';

function clamp(value: number, min: number, max: number) {
  return Math.min(max, Math.max(min, value));
}

function readNaturalSize(svgEl: SVGSVGElement): Size {
  const box = svgEl.viewBox?.baseVal;
  if (box && box.width > 0 && box.height > 0) return { w: box.width, h: box.height };
  const width = Number.parseFloat(svgEl.getAttribute('width') ?? '');
  const height = Number.parseFloat(svgEl.getAttribute('height') ?? '');
  if (Number.isFinite(width) && Number.isFinite(height) && width > 0 && height > 0) {
    return { w: width, h: height };
  }
  const rect = svgEl.getBoundingClientRect();
  return { w: rect.width || 800, h: rect.height || 600 };
}

function clampPan(x: number, y: number, scale: number, natural: Size, stage: Size) {
  const availableW = Math.max(0, stage.w - ZOOM_PADDING * 2);
  const availableH = Math.max(0, stage.h - ZOOM_PADDING * 2);
  const maxX = Math.max(0, (natural.w * scale - availableW) / 2);
  const maxY = Math.max(0, (natural.h * scale - availableH) / 2);
  return { x: clamp(x, -maxX, maxX), y: clamp(y, -maxY, maxY) };
}

/**
 * 近全屏图表查看器。
 *
 * 挂到 body 上：消息卡片带圆角、溢出裁剪与自建层叠上下文，浮层留在卡片里会被裁掉。
 * 缩放以“适应屏幕”为 1，滚轮以光标为锚点，放大后可直接拖拽平移。
 */
function MermaidViewer({ svg, code, onClose }: { svg: string; code: string; onClose: () => void }) {
  const canvasRef = useRef<HTMLDivElement>(null);
  const stageRef = useRef<HTMLDivElement>(null);
  const backdropRef = useRef<HTMLDivElement>(null);
  const dialogRef = useRef<HTMLDivElement>(null);
  const closeRef = useRef<HTMLButtonElement>(null);
  const dragRef = useRef<{ id: number; x: number; y: number; panX: number; panY: number } | null>(null);

  const [natural, setNatural] = useState<Size>({ w: 0, h: 0 });
  const [stage, setStage] = useState<Size>({ w: 0, h: 0 });
  const [zoom, setZoom] = useState(1);
  const [pan, setPan] = useState({ x: 0, y: 0 });
  const [dragging, setDragging] = useState(false);
  const [menuOpen, setMenuOpen] = useState(false);

  const availableW = Math.max(0, stage.w - ZOOM_PADDING * 2);
  const availableH = Math.max(0, stage.h - ZOOM_PADDING * 2);
  const fit = natural.w > 0 && natural.h > 0 && availableW > 0 && availableH > 0
      ? Math.min(availableW / natural.w, availableH / natural.h, 1)
      : 1;
  const scale = fit * zoom;
  const maxPanX = Math.max(0, (natural.w * scale - availableW) / 2);
  const maxPanY = Math.max(0, (natural.h * scale - availableH) / 2);
  const panX = clamp(pan.x, -maxPanX, maxPanX);
  const panY = clamp(pan.y, -maxPanY, maxPanY);
  const contentLeft = ZOOM_PADDING + (availableW - natural.w * scale) / 2 + panX;
  const contentTop = ZOOM_PADDING + (availableH - natural.h * scale) / 2 + panY;
  const percent = Math.round(scale * 100);
  const canZoomOut = zoom > ZOOM_MIN_SCALE / fit + 0.001;
  const canZoomIn = zoom < ZOOM_MAX_SCALE / fit - 0.001;
  const ready = natural.w > 0 && availableW > 0;

  useLayoutEffect(() => {
    const svgEl = canvasRef.current?.querySelector('svg');
    if (!svgEl) return;
    setNatural(readNaturalSize(svgEl as SVGSVGElement));
  }, [svg]);

  useLayoutEffect(() => {
    const el = stageRef.current;
    if (!el) return;
    const update = () => setStage({ w: el.clientWidth, h: el.clientHeight });
    update();
    const observer = new ResizeObserver(update);
    observer.observe(el);
    return () => observer.disconnect();
  }, []);

  useEffect(() => {
    const body = document.body;
    const previousOverflow = body.style.overflow;
    const previousPadding = body.style.paddingRight;
    const scrollbar = window.innerWidth - document.documentElement.clientWidth;
    body.style.overflow = 'hidden';
    if (scrollbar > 0) {
      const current = Number.parseFloat(window.getComputedStyle(body).paddingRight) || 0;
      body.style.paddingRight = `${current + scrollbar}px`;
    }
    return () => {
      body.style.overflow = previousOverflow;
      body.style.paddingRight = previousPadding;
    };
  }, []);

  useEffect(() => {
    // 页面真正的滚动容器是 .conversation-scroll，锁 body 不够；
    // 在浮层上拦掉图表区域之外的滚轮/触摸滚动，避免背景跟着动。
    const el = backdropRef.current;
    if (!el) return;
    const blockOutsideStage = (event: WheelEvent | TouchEvent) => {
      const target = event.target as Node | null;
      if (target && stageRef.current?.contains(target)) return;
      event.preventDefault();
    };
    el.addEventListener('wheel', blockOutsideStage, { passive: false });
    el.addEventListener('touchmove', blockOutsideStage, { passive: false });
    return () => {
      el.removeEventListener('wheel', blockOutsideStage);
      el.removeEventListener('touchmove', blockOutsideStage);
    };
  }, []);

  useEffect(() => {
    closeRef.current?.focus();
  }, []);

  useEffect(() => {
    if (!menuOpen) return;
    const close = () => setMenuOpen(false);
    document.addEventListener('click', close);
    return () => document.removeEventListener('click', close);
  }, [menuOpen]);

  const zoomAt = useCallback((factor: number, anchorX?: number, anchorY?: number) => {
    if (!natural.w || !natural.h || !availableW || !availableH) return;
    const nextZoom = clamp(zoom * factor, ZOOM_MIN_SCALE / fit, ZOOM_MAX_SCALE / fit);
    if (Math.abs(nextZoom - zoom) < 0.0001) return;
    const nextScale = fit * nextZoom;
    const ax = anchorX ?? availableW / 2 + ZOOM_PADDING;
    const ay = anchorY ?? availableH / 2 + ZOOM_PADDING;
    // 光标下的图纸坐标缩放到新比例后仍停在光标处。
    const pointX = (ax - contentLeft) / scale;
    const pointY = (ay - contentTop) / scale;
    const nextLeft = ax - pointX * nextScale;
    const nextTop = ay - pointY * nextScale;
    const baseX = ZOOM_PADDING + (availableW - natural.w * nextScale) / 2;
    const baseY = ZOOM_PADDING + (availableH - natural.h * nextScale) / 2;
    setZoom(nextZoom);
    setPan(clampPan(nextLeft - baseX, nextTop - baseY, nextScale, natural, stage));
  }, [availableW, availableH, contentLeft, contentTop, fit, natural, scale, stage, zoom]);

  const resetZoom = useCallback(() => {
    setZoom(1);
    setPan({ x: 0, y: 0 });
  }, []);

  useEffect(() => {
    const el = stageRef.current;
    if (!el) return;
    const onWheel = (event: WheelEvent) => {
      if (!natural.w) return;
      event.preventDefault();
      const rect = el.getBoundingClientRect();
      const delta = event.deltaMode === 1 ? event.deltaY * 16 : event.deltaY;
      zoomAt(Math.exp(-delta * 0.0012), event.clientX - rect.left, event.clientY - rect.top);
    };
    el.addEventListener('wheel', onWheel, { passive: false });
    return () => el.removeEventListener('wheel', onWheel);
  }, [natural.w, zoomAt]);

  useEffect(() => {
    const modal = dialogRef.current;
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.key === 'Escape') {
        event.preventDefault();
        event.stopPropagation();
        if (menuOpen) {
          setMenuOpen(false);
          return;
        }
        onClose();
        return;
      }
      if (!modal) return;
      if (event.key === 'Tab') {
        const items = Array.from(modal.querySelectorAll<HTMLElement>(ZOOM_FOCUSABLE));
        if (!items.length) return;
        const first = items[0];
        const last = items[items.length - 1];
        if (event.shiftKey && document.activeElement === first) {
          event.preventDefault();
          last.focus();
        } else if (!event.shiftKey && document.activeElement === last) {
          event.preventDefault();
          first.focus();
        }
        return;
      }
      if (event.key === '+' || event.key === '=') {
        event.preventDefault();
        zoomAt(1.25);
      } else if (event.key === '-' || event.key === '_') {
        event.preventDefault();
        zoomAt(0.8);
      } else if (event.key === '0') {
        event.preventDefault();
        resetZoom();
      }
    };
    document.addEventListener('keydown', onKeyDown);
    return () => document.removeEventListener('keydown', onKeyDown);
  }, [menuOpen, onClose, resetZoom, zoomAt]);

  const handleDownloadSVG = useCallback(() => {
    if (canvasRef.current) downloadSVG(canvasRef.current);
    setMenuOpen(false);
  }, []);

  const handleDownloadPNG = useCallback(() => {
    if (canvasRef.current) downloadPNG(canvasRef.current);
    setMenuOpen(false);
  }, []);

  const handleCopyCode = useCallback(async () => {
    await navigator.clipboard.writeText(code);
    setMenuOpen(false);
  }, [code]);

  const handlePointerDown = (event: React.PointerEvent<HTMLDivElement>) => {
    if (event.button !== 0) return;
    if (maxPanX <= 0 && maxPanY <= 0) return;
    dragRef.current = { id: event.pointerId, x: event.clientX, y: event.clientY, panX, panY };
    event.currentTarget.setPointerCapture(event.pointerId);
    setDragging(true);
  };

  const handlePointerMove = (event: React.PointerEvent<HTMLDivElement>) => {
    const drag = dragRef.current;
    if (!drag || drag.id !== event.pointerId) return;
    setPan(clampPan(
        drag.panX + event.clientX - drag.x,
        drag.panY + event.clientY - drag.y,
        scale,
        natural,
        stage,
    ));
  };

  const endDrag = (event: React.PointerEvent<HTMLDivElement>) => {
    if (dragRef.current?.id !== event.pointerId) return;
    dragRef.current = null;
    setDragging(false);
  };

  return createPortal(
      <div
          className="mermaid-zoom-backdrop"
          ref={backdropRef}
          role="presentation"
          onClick={(event) => {
            if (event.target === event.currentTarget) onClose();
          }}
      >
        <div
            className="mermaid-zoom-dialog"
            ref={dialogRef}
            role="dialog"
            aria-modal="true"
            aria-label="Mermaid 图表预览"
            onClick={(event) => event.stopPropagation()}
        >
          <header className="mermaid-zoom-head">
            <span className="mermaid-zoom-title">
              <strong>图表预览</strong>
              <span aria-hidden="true">MERMAID</span>
            </span>
            <div className="mermaid-zoom-controls">
              <button
                  type="button"
                  className="mermaid-zoom-button"
                  onClick={() => zoomAt(0.8)}
                  disabled={!canZoomOut}
                  aria-label="缩小"
                  title="缩小（-）"
              >
                <span aria-hidden="true">−</span>
              </button>
              <span className="mermaid-zoom-scale" aria-live="polite">{percent}%</span>
              <button
                  type="button"
                  className="mermaid-zoom-button"
                  onClick={() => zoomAt(1.25)}
                  disabled={!canZoomIn}
                  aria-label="放大"
                  title="放大（+）"
              >
                <span aria-hidden="true">+</span>
              </button>
              <button type="button" className="mermaid-zoom-reset" onClick={resetZoom} title="适应屏幕（0）">
                适应
              </button>
            </div>
            <div className="mermaid-zoom-actions">
              <div className="mermaid-zoom-export">
                <button
                    type="button"
                    className="mermaid-zoom-button mermaid-zoom-export-trigger"
                    onClick={(event) => {
                      event.stopPropagation();
                      setMenuOpen((value) => !value);
                    }}
                    aria-haspopup="menu"
                    aria-expanded={menuOpen}
                    title="导出图表"
                >
                  <DownloadIcon size={13} />
                  导出
                </button>
                {menuOpen && (
                    <div className="mermaid-download-menu mermaid-zoom-menu" role="menu">
                      <button type="button" role="menuitem" onClick={handleDownloadSVG}><DownloadIcon size={14} />下载 SVG</button>
                      <button type="button" role="menuitem" onClick={handleDownloadPNG}><DownloadIcon size={14} />下载 PNG</button>
                      <div className="mermaid-download-menu-divider" />
                      <button type="button" role="menuitem" onClick={handleCopyCode}><CopyIcon size={14} />复制代码</button>
                    </div>
                )}
              </div>
              <button
                  type="button"
                  className="mermaid-zoom-close"
                  ref={closeRef}
                  onClick={onClose}
                  aria-label="关闭图表预览"
                  title="关闭（Esc）"
              >
                <CloseIcon size={16} />
              </button>
            </div>
          </header>
          <div
              className={`mermaid-zoom-stage${dragging ? ' is-dragging' : ''}`}
              ref={stageRef}
              onPointerDown={handlePointerDown}
              onPointerMove={handlePointerMove}
              onPointerUp={endDrag}
              onPointerCancel={endDrag}
          >
            <div
                className="mermaid-zoom-canvas"
                ref={canvasRef}
                data-ready={ready ? 'true' : 'false'}
                style={{
                  width: natural.w || undefined,
                  height: natural.h || undefined,
                  transform: `translate3d(${contentLeft}px, ${contentTop}px, 0) scale(${scale})`,
                }}
                dangerouslySetInnerHTML={{ __html: svg }}
            />
            <span className="mermaid-zoom-hint" aria-hidden="true">滚轮缩放 · 放大后拖拽 · Esc 关闭</span>
          </div>
        </div>
      </div>,
      document.body,
  );
}

export function MermaidDiagram({ code }: { code: string }) {
  const [svg, setSvg] = useState('');
  const [errored, setErrored] = useState(false);
  const [menuOpen, setMenuOpen] = useState(false);
  const [viewerOpen, setViewerOpen] = useState(false);
  const idRef = useRef(`mermaid-${Math.random().toString(36).slice(2)}`);
  const containerRef = useRef<HTMLDivElement>(null);
  const surfaceRef = useRef<HTMLDivElement>(null);

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
          (mod as MermaidLoadable).initialize(MERMAID_CONFIG);
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

  const closeViewer = useCallback(() => {
    setViewerOpen(false);
    // 等浮层卸载后再把焦点交还图表主体，避免焦点落回 body。
    requestAnimationFrame(() => surfaceRef.current?.focus());
  }, []);

  if (errored || !svg) {
    return (
        <pre className="mermaid-fallback">
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
            <DownloadIcon size={14} />
            导出
            <ChevronDownIcon size={12} />
          </button>
          {menuOpen && (
              <div className="mermaid-download-menu">
                <button type="button" onClick={handleDownloadSVG}><DownloadIcon size={14} />下载 SVG</button>
                <button type="button" onClick={handleDownloadPNG}><DownloadIcon size={14} />下载 PNG</button>
                <div className="mermaid-download-menu-divider" />
                <button type="button" onClick={handleCopyCode}><CopyIcon size={14} />复制代码</button>
              </div>
          )}
        </div>
        <div
            className="mermaid-open-surface"
            ref={surfaceRef}
            role="button"
            tabIndex={0}
            aria-label="放大查看图表"
            title="点击放大查看"
            onClick={() => setViewerOpen(true)}
            onKeyDown={(event) => {
              if (event.key !== 'Enter' && event.key !== ' ') return;
              event.preventDefault();
              setViewerOpen(true);
            }}
            dangerouslySetInnerHTML={{ __html: svg }}
        />
        {viewerOpen && <MermaidViewer svg={svg} code={code} onClose={closeViewer} />}
      </div>
  );
}
