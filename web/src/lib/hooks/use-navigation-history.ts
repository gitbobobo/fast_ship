import { useMemo } from "react";
import { useLocation } from "react-router";

/**
 * 判断当前页面是否需要展示返回入口，以及浏览器历史是否可安全后退。
 */
export function useNavigationHistory() {
  const location = useLocation();

  // 判断是否为顶层页面（应用的主要入口页面）
  const isTopLevelPage = useMemo(() => {
    const path = location.pathname;

    // 顶层一级页面（不能在这些页面返回）
    const topLevelRoutes = [
      "/projects",
      "/issues",
      "/versions",
    ];

    // 顶层一级页面或设置相关页面
    return topLevelRoutes.includes(path) || path.startsWith("/settings");
  }, [location.pathname]);

  const canGoBack = !isTopLevelPage;
  const canUseBrowserHistory =
    typeof window !== "undefined" &&
    typeof window.history.state?.idx === "number" &&
    window.history.state.idx > 0;

  return { canGoBack, canUseBrowserHistory };
}
