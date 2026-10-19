// Appearance registry and persistence for the workspace.
//
// Every design is a pair of (layout, visual language) implemented by a scoped
// stylesheet under `src/theme/designs/<id>.css`. The shell stays the same DOM;
// designs are free to re-flow it with CSS and restyle every surface.
//
// Two independent axes are persisted:
//   design — which layout / visual language is active
//   mode   — light, dark, or follow the OS
//
// The resolved theme is written to <html data-theme="light|dark">, the design
// to <html data-ui="<id>">. Design stylesheets must style both themes.

export type DesignId = 'classic' | 'nocturne' | 'editorial' | 'schematic';
export type ThemeMode = 'light' | 'dark' | 'system';
export type ResolvedTheme = 'light' | 'dark';

export type AppearanceState = {
  design: DesignId;
  mode: ThemeMode;
};

export type DesignOption = {
  id: DesignId;
  label: string;
  en: string;
  summary: string;
  /** Three representative colors used by the compact switcher preview. */
  swatches: [string, string, string];
  /** True for the three agent-authored designs; classic is the original UI. */
  authored: boolean;
};

export const DESIGNS: DesignOption[] = [
  {
    id: 'classic',
    label: '经典手册',
    en: 'Live Reference Manual',
    summary: '深色索引板配米色纸面，当前线上版本。',
    swatches: ['#1b2024', '#f4f1e6', '#b24a22'],
    authored: false,
  },
  {
    id: 'nocturne',
    label: '深空控制台',
    en: 'Nocturne Console',
    summary: '深色仪表台，右侧会话轨道，荧光状态色与等宽标注。',
    swatches: ['#0a1016', '#7ef0c8', '#f5b84a'],
    authored: true,
  },
  {
    id: 'editorial',
    label: '纸本社论',
    en: 'Editorial Ledger',
    summary: '暖白纸面与超大衬线刊头，居中窄栏，朱红点睛。',
    swatches: ['#f7f3ea', '#14110c', '#c8452c'],
    authored: true,
  },
  {
    id: 'schematic',
    label: '蓝图工坊',
    en: 'Schematic Works',
    summary: '工程蓝图网格，右侧编号导航，橙色标记与制图标注。',
    swatches: ['#eef1f4', '#12263a', '#e06b2d'],
    authored: true,
  },
];

export const THEME_OPTIONS: Array<{
  id: ThemeMode;
  label: string;
  hint: string;
}> = [
  { id: 'light', label: '浅色', hint: 'Light' },
  { id: 'dark', label: '深色', hint: 'Dark' },
  { id: 'system', label: '跟随系统', hint: 'Auto' },
];

export const DEFAULT_APPEARANCE: AppearanceState = {
  design: 'classic',
  mode: 'light',
};

const STORAGE_KEY = 'repo-agent.appearance.v1';

const DESIGN_IDS = new Set<string>(DESIGNS.map((item) => item.id));
const THEME_MODES = new Set<string>(THEME_OPTIONS.map((item) => item.id));

function isDesignId(value: unknown): value is DesignId {
  return typeof value === 'string' && DESIGN_IDS.has(value);
}

function isThemeMode(value: unknown): value is ThemeMode {
  return typeof value === 'string' && THEME_MODES.has(value);
}

function readStored(): Partial<AppearanceState> {
  if (typeof window === 'undefined') {
    return {};
  }
  try {
    const raw = window.localStorage.getItem(STORAGE_KEY);
    if (!raw) {
      return {};
    }
    const parsed = JSON.parse(raw) as Record<string, unknown>;
    const next: Partial<AppearanceState> = {};
    if (isDesignId(parsed.design)) {
      next.design = parsed.design;
    }
    if (isThemeMode(parsed.mode)) {
      next.mode = parsed.mode;
    }
    return next;
  } catch {
    return {};
  }
}

// `?ui=nocturne&theme=dark` is a first-class preview hook: it lets a design be
// opened directly in a tab (and lets the design agents screenshot their own
// variant without touching local storage first). Values are validated.
function readFromUrl(): Partial<AppearanceState> {
  if (typeof window === 'undefined') {
    return {};
  }
  const params = new URLSearchParams(window.location.search);
  const next: Partial<AppearanceState> = {};
  const ui = params.get('ui');
  const theme = params.get('theme');
  if (isDesignId(ui)) {
    next.design = ui;
  }
  if (isThemeMode(theme)) {
    next.mode = theme;
  }
  return next;
}

export function readInitialAppearance(): AppearanceState {
  return {
    ...DEFAULT_APPEARANCE,
    ...readStored(),
    ...readFromUrl(),
  };
}

export function persistAppearance(state: AppearanceState) {
  if (typeof window === 'undefined') {
    return;
  }
  try {
    window.localStorage.setItem(STORAGE_KEY, JSON.stringify(state));
  } catch {
    // Private browsing / blocked storage: the session still works in memory.
  }
}

export function systemPrefersDark(): boolean {
  if (typeof window === 'undefined' || typeof window.matchMedia !== 'function') {
    return false;
  }
  return window.matchMedia('(prefers-color-scheme: dark)').matches;
}

export function resolveTheme(mode: ThemeMode, systemDark: boolean): ResolvedTheme {
  if (mode === 'system') {
    return systemDark ? 'dark' : 'light';
  }
  return mode;
}

export function applyAppearance(design: DesignId, resolvedTheme: ResolvedTheme) {
  if (typeof document === 'undefined') {
    return;
  }
  const root = document.documentElement;
  root.dataset.ui = design;
  root.dataset.theme = resolvedTheme;
  root.style.colorScheme = resolvedTheme;

  const themeColor = document.querySelector('meta[name="theme-color"]');
  if (themeColor) {
    themeColor.setAttribute('content', resolvedTheme === 'dark' ? '#0b0f14' : '#f4f1e6');
  }
}
