import { act, cleanup } from "@testing-library/react";
import { useAuthStore } from "@/lib/store/auth-store";

/** 以指定用户登录真实的 auth store，页面/hook 测试不绕过身份。 */
export function signInAs(id: string, role: UserRole = "member") {
  useAuthStore.getState().setAuth(`token-${id}`, `refresh-${id}`, {
    id,
    username: id,
    email: `${id}@example.com`,
    avatar_url: "",
    role,
    created_at: "2026-10-01T00:00:00Z",
    updated_at: "2026-10-01T00:00:00Z",
  });
}

/** 测试中途登出，视图更新包在 act 里。 */
export function signOut() {
  act(() => useAuthStore.getState().logout());
}

/**
 * afterEach 用：先卸载视图再登出，避免对仍挂载的组件触发 act 之外的更新。
 * Vitest 的 afterEach 倒序执行，所以不能依赖 RTL 的自动 cleanup 排在前面。
 */
export function teardownAuth() {
  cleanup();
  useAuthStore.getState().logout();
}
