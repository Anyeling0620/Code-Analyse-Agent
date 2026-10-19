import { useEffect, useRef, useState } from 'react';
import { DESIGNS, THEME_OPTIONS, type DesignOption, type ThemeMode } from './appearance';
import { useAppearance } from './AppearanceContext';
import { CheckIcon, CloseIcon, MonitorIcon, MoonIcon, PaletteIcon, SunIcon } from '../ui/icons';

const THEME_ICONS: Record<ThemeMode, typeof SunIcon> = {
  light: SunIcon,
  dark: MoonIcon,
  system: MonitorIcon,
};

// DesignPreview is a tiny schematic of each design's layout language. It is
// deliberately abstract: a rail/board block, a reading field, and a composer
// strip, colored with the design's own swatches.
function DesignPreview({ option }: { option: DesignOption }) {
  const [board, paper, accent] = option.swatches;
  return (
    <span
      className={`appearance-preview appearance-preview-${option.id}`}
      style={{
        ['--preview-board' as string]: board,
        ['--preview-paper' as string]: paper,
        ['--preview-accent' as string]: accent,
      }}
      aria-hidden="true"
    >
      <span className="appearance-preview-rail" />
      <span className="appearance-preview-field">
        <span className="appearance-preview-bar" />
        <span className="appearance-preview-line appearance-preview-line-lg" />
        <span className="appearance-preview-line" />
        <span className="appearance-preview-line appearance-preview-line-sm" />
      </span>
      <span className="appearance-preview-composer" />
    </span>
  );
}

export function AppearanceSwitcher() {
  const { design, mode, resolvedTheme, setDesign, setMode } = useAppearance();
  const [isOpen, setIsOpen] = useState(false);
  const controlRef = useRef<HTMLDivElement | null>(null);
  const triggerRef = useRef<HTMLButtonElement | null>(null);

  useEffect(() => {
    if (!isOpen) {
      return;
    }
    const onPointerDown = (event: PointerEvent) => {
      const target = event.target as Node | null;
      if (target && controlRef.current && !controlRef.current.contains(target)) {
        setIsOpen(false);
      }
    };
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.key === 'Escape') {
        setIsOpen(false);
        triggerRef.current?.focus();
      }
    };
    document.addEventListener('pointerdown', onPointerDown);
    document.addEventListener('keydown', onKeyDown);
    return () => {
      document.removeEventListener('pointerdown', onPointerDown);
      document.removeEventListener('keydown', onKeyDown);
    };
  }, [isOpen]);

  const current = DESIGNS.find((item) => item.id === design) ?? DESIGNS[0];

  return (
    <div className="appearance-control" ref={controlRef}>
      <button
        ref={triggerRef}
        type="button"
        className={`appearance-trigger${isOpen ? ' is-open' : ''}`}
        aria-haspopup="dialog"
        aria-expanded={isOpen}
        aria-controls="appearance-panel"
        title={`切换界面布局与主题（当前：${current.label}）`}
        onClick={() => setIsOpen((open) => !open)}
      >
        <PaletteIcon size={17} />
        <span className="appearance-trigger-label">外观</span>
        <span className={`appearance-trigger-chip appearance-trigger-chip-${resolvedTheme}`} aria-hidden="true" />
      </button>

      {isOpen && (
        <div
          id="appearance-panel"
          className="appearance-popover"
          role="dialog"
          aria-label="界面布局与主题"
	        >
          <div className="appearance-popover-head">
            <div className="appearance-popover-heading">
              <span className="appearance-kicker">APPEARANCE</span>
              <h2 className="appearance-title">界面布局与主题</h2>
            </div>
            <button
              type="button"
              className="appearance-close"
              aria-label="关闭外观设置"
              onClick={() => {
                setIsOpen(false);
                triggerRef.current?.focus();
              }}
            >
              <CloseIcon size={16} />
            </button>
          </div>

          <div className="appearance-section">
            <span className="appearance-section-label">布局 / LAYOUT</span>
            <div className="appearance-design-grid">
              {DESIGNS.map((option) => {
                const selected = option.id === design;
                return (
                  <button
                    key={option.id}
                    type="button"
                    className={`appearance-design${selected ? ' is-selected' : ''}`}
                    aria-pressed={selected}
                    onClick={() => setDesign(option.id)}
                  >
                    <DesignPreview option={option} />
                    <span className="appearance-design-copy">
                      <span className="appearance-design-name">
                        {option.label}
                        <span className="appearance-design-en">{option.en}</span>
                      </span>
                      <span className="appearance-design-desc">{option.summary}</span>
                    </span>
                    {selected && (
                      <span className="appearance-selected-mark" aria-hidden="true">
                        <CheckIcon size={12} />
                      </span>
                    )}
                  </button>
                );
              })}
            </div>
          </div>

          <div className="appearance-section">
            <span className="appearance-section-label">主题 / THEME</span>
            <div className="appearance-theme-row" role="group" aria-label="颜色主题">
              {THEME_OPTIONS.map((option) => {
                const ThemeIcon = THEME_ICONS[option.id];
                const selected = option.id === mode;
                return (
                  <button
                    key={option.id}
                    type="button"
                    className={`appearance-theme${selected ? ' is-selected' : ''}`}
                    aria-pressed={selected}
                    onClick={() => setMode(option.id)}
                  >
                    <ThemeIcon size={15} />
                    <span className="appearance-theme-label">{option.label}</span>
                    <span className="appearance-theme-hint">{option.hint}</span>
                  </button>
                );
              })}
            </div>
          </div>

          <p className="appearance-note">选择会保存在本机，刷新后保持；新设计同时提供浅色与深色两套主题。</p>
        </div>
      )}
    </div>
  );
}
