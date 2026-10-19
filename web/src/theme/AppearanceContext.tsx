import {
  createContext,
  useContext,
  useEffect,
  useLayoutEffect,
  useMemo,
  useState,
  type ReactNode,
} from 'react';
import {
  applyAppearance,
  persistAppearance,
  readInitialAppearance,
  resolveTheme,
  systemPrefersDark,
  type AppearanceState,
  type DesignId,
  type ResolvedTheme,
  type ThemeMode,
} from './appearance';

type AppearanceContextValue = {
  design: DesignId;
  mode: ThemeMode;
  resolvedTheme: ResolvedTheme;
  setDesign: (design: DesignId) => void;
  setMode: (mode: ThemeMode) => void;
};

const AppearanceContext = createContext<AppearanceContextValue | null>(null);

export function AppearanceProvider({ children }: { children: ReactNode }) {
  const [state, setState] = useState<AppearanceState>(readInitialAppearance);
  const [systemDark, setSystemDark] = useState(systemPrefersDark);

  // Keep "follow system" live: OS appearance changes should not need a reload.
  useEffect(() => {
    if (typeof window === 'undefined' || typeof window.matchMedia !== 'function') {
      return;
    }
    const query = window.matchMedia('(prefers-color-scheme: dark)');
    const onChange = (event: MediaQueryListEvent) => setSystemDark(event.matches);
    setSystemDark(query.matches);
    if (typeof query.addEventListener === 'function') {
      query.addEventListener('change', onChange);
      return () => query.removeEventListener('change', onChange);
    }
    // Safari < 14 fallback.
    query.addListener(onChange);
    return () => query.removeListener(onChange);
  }, []);

  const resolvedTheme = resolveTheme(state.mode, systemDark);

  // Layout effect avoids a flash of the previous design when switching.
  useLayoutEffect(() => {
    applyAppearance(state.design, resolvedTheme);
    persistAppearance(state);
  }, [state, resolvedTheme]);

  // Logging out / leaving the workspace unmounts this provider. Remove the
  // design attributes so the login and shared-session pages get the original
  // classic tokens instead of inheriting half a world.
  useEffect(() => {
    return () => {
      if (typeof document === 'undefined') {
        return;
      }
      const root = document.documentElement;
      delete root.dataset.ui;
      delete root.dataset.theme;
      root.style.colorScheme = '';
    };
  }, []);

  const value = useMemo<AppearanceContextValue>(
    () => ({
      design: state.design,
      mode: state.mode,
      resolvedTheme,
      setDesign: (design) => setState((current) => ({ ...current, design })),
      setMode: (mode) => setState((current) => ({ ...current, mode })),
    }),
    [state.design, state.mode, resolvedTheme],
  );

  return <AppearanceContext.Provider value={value}>{children}</AppearanceContext.Provider>;
}

export function useAppearance(): AppearanceContextValue {
  const value = useContext(AppearanceContext);
  if (!value) {
    throw new Error('useAppearance must be used inside <AppearanceProvider>');
  }
  return value;
}
