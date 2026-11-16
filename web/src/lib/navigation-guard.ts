// 站内导航守卫：页面（如文档编辑器）有未保存内容时登记确认回调，
// 侧边栏链接与项目切换在导航前逐一询问。覆盖范围：侧边栏链接、用户菜单
// 导航项、项目切换器；浏览器前进/后退（popstate）当前无拦截（声明式
// 路由下 useBlocker 不可用），强制刷新/关标签由页面的 beforeunload 兑底。

type NavigationGuard = () => string | null;

const guards = new Set<NavigationGuard>();

/**
 * 登记导航确认回调：返回确认文案表示需要拦截（用户取消则中止导航），
 * 返回 null 表示放行。返回反登记函数，供 effect 清理。
 */
export function registerNavigationGuard(guard: NavigationGuard): () => void {
  guards.add(guard);
  return () => {
    guards.delete(guard);
  };
}

/** 导航前询问所有守卫；任一被取消返回 false，调用方应中止本次导航。 */
export function confirmNavigationLeave(): boolean {
  for (const guard of guards) {
    const message = guard();
    if (message && !window.confirm(message)) {
      return false;
    }
  }
  return true;
}
