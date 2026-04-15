import { create } from "zustand";
import { persist } from "zustand/middleware";

export type Theme = "light" | "dark" | "system";

interface ThemeState {
  theme: Theme;
  setTheme: (theme: Theme) => void;
  resolvedTheme: "light" | "dark";
}

function getSystemTheme(): "light" | "dark" {
  if (typeof window === "undefined") return "light";
  return window.matchMedia("(prefers-color-scheme: dark)").matches
    ? "dark"
    : "light";
}

function resolveTheme(theme: Theme): "light" | "dark" {
  if (theme === "system") {
    return getSystemTheme();
  }
  return theme;
}

export const useThemeStore = create<ThemeState>()(
  persist(
    (set) => ({
      theme: "system",
      resolvedTheme: resolveTheme("system"),
      setTheme: (theme: Theme) => {
        set({ theme, resolvedTheme: resolveTheme(theme) });
      },
    }),
    {
      name: "fast-ship-theme",
      onRehydrateStorage: () => (state) => {
        if (state) {
          const themeState = state as ThemeState;
          themeState.resolvedTheme = resolveTheme(themeState.theme);
        }
      },
    },
  ),
);

// 监听系统主题变化
if (typeof window !== "undefined") {
  const mediaQuery = window.matchMedia("(prefers-color-scheme: dark)");
  mediaQuery.addEventListener("change", () => {
    const store = useThemeStore.getState();
    if (store.theme === "system") {
      useThemeStore.setState({ resolvedTheme: getSystemTheme() });
    }
  });
}
