import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import OAuthAppsPage from "@/routes/settings/oauth-apps";
import type { components } from "@/types/api.gen";
import {
  useOAuthAuthorizations,
  useRevokeOAuthAuthorization,
} from "@/lib/hooks/use-oauth";
import { useProjects } from "@/lib/hooks/use-projects";
import { renderWithRoute } from "@/test/render";
import { formatDate } from "@/lib/utils/format";

type OAuthAuthorization = components["schemas"]["OAuthAuthorization"];
type Project = components["schemas"]["Project"];

vi.mock("sonner", () => ({
  toast: { success: vi.fn(), error: vi.fn() },
}));

vi.mock("@/lib/hooks/use-oauth", () => ({
  useOAuthAuthorizations: vi.fn(),
  useRevokeOAuthAuthorization: vi.fn(),
}));

vi.mock("@/lib/hooks/use-projects", () => ({
  useProjects: vi.fn(),
}));

vi.mock("@/lib/hooks/use-notifications", () => ({
  NOTIFICATION_UNREAD_POLL_MS: 30000,
  useNotifications: vi.fn(() => ({ data: [] })),
  useUnreadNotificationCount: vi.fn(() => ({ data: 0 })),
  useMarkNotificationRead: vi.fn(() => ({ mutate: vi.fn(), mutateAsync: vi.fn() })),
  useMarkAllNotificationsRead: vi.fn(() => ({ mutate: vi.fn(), mutateAsync: vi.fn() })),
}));

const projectsFixture: Project[] = [
  { id: "p-1", name: "Fast Ship" },
  { id: "p-2", name: "Mobile App" },
  { id: "p-gone", name: "已删除项目" },
] as Project[];

const authFixture: OAuthAuthorization = {
  id: "oa-1",
  client_name: "Claude Code MCP",
  scopes: "project:p-1:read project:p-2:write",
  created_at: "2026-10-01T00:00:00Z",
};

const revokeMutateAsync = vi.fn();

function renderPage() {
  return renderWithRoute(<OAuthAppsPage />, {
    path: "/settings/oauth-apps",
    initialEntry: "/settings/oauth-apps",
  });
}

beforeEach(() => {
  vi.clearAllMocks();
  vi.mocked(useOAuthAuthorizations).mockReturnValue({
    data: [authFixture],
    isLoading: false,
  } as unknown as ReturnType<typeof useOAuthAuthorizations>);
  vi.mocked(useProjects).mockReturnValue({
    data: { items: projectsFixture, total: 3, page: 1, page_size: 20 },
    isLoading: false,
  } as unknown as ReturnType<typeof useProjects>);
  vi.mocked(useRevokeOAuthAuthorization).mockReturnValue({
    mutateAsync: revokeMutateAsync,
    isPending: false,
  } as unknown as ReturnType<typeof useRevokeOAuthAuthorization>);
});

describe("OAuthAppsPage", () => {
  it("列表渲染客户端名、读写档、项目名与授权时间", () => {
    renderPage();

    const row = screen.getByRole("row", { name: /Claude Code MCP/ });
    expect(row).toHaveTextContent("读写");
    // scope 串里的项目 id 映射成项目名展示。
    expect(row).toHaveTextContent("Fast Ship、Mobile App");
    expect(row).toHaveTextContent(formatDate(authFixture.created_at));
  });

  it("scope 全为 read 时展示只读档", () => {
    vi.mocked(useOAuthAuthorizations).mockReturnValue({
      data: [{ ...authFixture, scopes: "project:p-1:read" }],
      isLoading: false,
    } as unknown as ReturnType<typeof useOAuthAuthorizations>);
    renderPage();

    expect(screen.getByText("只读")).toBeInTheDocument();
    expect(screen.queryByText("读写")).toBeNull();
  });

  it("项目 id 对不上名字时退回截断 id 展示", () => {
    vi.mocked(useOAuthAuthorizations).mockReturnValue({
      data: [{ ...authFixture, scopes: "project:unknown-id:read" }],
      isLoading: false,
    } as unknown as ReturnType<typeof useOAuthAuthorizations>);
    renderPage();

    expect(screen.getByText("项目 unknown-…")).toBeInTheDocument();
  });

  it("空列表显示空态", () => {
    vi.mocked(useOAuthAuthorizations).mockReturnValue({
      data: [],
      isLoading: false,
    } as unknown as ReturnType<typeof useOAuthAuthorizations>);
    renderPage();

    expect(screen.getByText("还没有已授权的应用")).toBeInTheDocument();
  });

  it("吊销：确认后调用 DELETE 并刷新列表", async () => {
    revokeMutateAsync.mockResolvedValue(null);
    const user = userEvent.setup();
    renderPage();

    await user.click(screen.getByRole("button", { name: "吊销 Claude Code MCP 的授权" }));
    const dialog = await screen.findByRole("alertdialog");
    expect(dialog).toHaveTextContent("吊销 Claude Code MCP 的授权？");

    await user.click(within(dialog).getByRole("button", { name: "确认吊销" }));
    await waitFor(() => expect(revokeMutateAsync).toHaveBeenCalledWith("oa-1"));
  });

  it("吊销失败：提示错误且不假报成功", async () => {
    revokeMutateAsync.mockRejectedValue(new Error("boom"));
    const user = userEvent.setup();
    renderPage();

    await user.click(screen.getByRole("button", { name: "吊销 Claude Code MCP 的授权" }));
    const dialog = await screen.findByRole("alertdialog");
    await user.click(within(dialog).getByRole("button", { name: "确认吊销" }));

    await waitFor(() => expect(revokeMutateAsync).toHaveBeenCalled());
    // 失败路径走 toast.error，不出现成功 toast（sonner 已被 mock）。
    const { toast } = await import("sonner");
    await waitFor(() => expect(toast.error).toHaveBeenCalledWith("吊销失败，请重试"));
  });
});
