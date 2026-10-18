import { act, render, screen } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { MemoryRouter, Route, Routes } from "react-router";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import AutomationEditorPage from "@/routes/automations/editor";
import { automationApi, type Automation } from "@/lib/api/automations";
import { environmentApi } from "@/lib/api/threads";
import { signInAs, teardownAuth } from "@/test/auth";

// 详情查询（useAutomation）走真实 hook + 真实 QueryClient：这里不 mock
// use-automations，只 mock 传输层，覆盖「离开编辑页 → 列表页改了状态 →
// 再次进入编辑页」这条缓存链路（INT-118 回归）。

vi.mock("sonner", () => ({
  toast: { success: vi.fn(), error: vi.fn() },
}));

vi.mock("@/lib/hooks/use-notifications", () => ({
  NOTIFICATION_UNREAD_POLL_MS: 30000,
  useNotifications: vi.fn(() => ({ data: [] })),
  useUnreadNotificationCount: vi.fn(() => ({ data: 0 })),
  useMarkNotificationRead: vi.fn(() => ({ mutate: vi.fn(), mutateAsync: vi.fn() })),
  useMarkAllNotificationsRead: vi.fn(() => ({ mutate: vi.fn(), mutateAsync: vi.fn() })),
}));

vi.mock("@/lib/hooks/use-projects", () => ({
  useProjects: vi.fn(() => ({
    data: { items: [{ id: "p-1", name: "示例项目" }] },
    isLoading: false,
    isError: false,
  })),
}));

vi.mock("@/lib/api/threads", () => ({
  environmentApi: {
    list: vi.fn().mockResolvedValue({
      data: { items: [{ id: "env-1", name: "本机环境" }] },
    }),
  },
  threadApi: {
    composerOptions: vi.fn().mockResolvedValue({
      data: { projects: [{ id: "proj-1", title: "示例项目" }], config: {}, settings: {} },
    }),
    workspaceOptions: vi.fn().mockResolvedValue({
      data: {
        project_id: "proj-1",
        cwd: "/tmp",
        is_repo: true,
        ref_name: "main",
        refs: [],
      },
    }),
  },
}));

// 只替换传输层，保留 automationTriggerLabel 等纯函数（列表摘要要用）。
vi.mock("@/lib/api/automations", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@/lib/api/automations")>();
  return {
    ...actual,
    automationApi: {
      list: vi.fn(),
      get: vi.fn(),
      create: vi.fn(),
      update: vi.fn(),
      remove: vi.fn(),
      listRuns: vi.fn(),
      getRun: vi.fn(),
    },
  };
});

const cronTrigger = {
  id: "t-1",
  trigger_type: "cron" as const,
  cron_expression: "0 9 * * *",
  timezone: "Asia/Shanghai",
  next_run_at: "2026-10-15T01:00:00Z",
  issue_project_id: null,
  event_filter: null,
  model_selections: [],
  debounce_seconds: 0,
  created_at: "2026-10-14T00:00:00Z",
};

const staleAutomation: Automation = {
  id: "a-1",
  name: "旧名称",
  enabled: false,
  environment_id: "env-1",
  t3_project_id: "proj-1",
  triggers: [cronTrigger],
  prompt_template: "回报令牌",
  model_selection: null,
  runtime_mode: "full-access",
  timeout_seconds: 3600,
  post_action: "settle",
  post_on_timeout: false,
  created_at: "2026-10-14T00:00:00Z",
  updated_at: "2026-10-14T00:00:00Z",
};

// 列表页「启用」按钮发出的全量 PUT 之后，服务端详情的新状态。
const freshAutomation: Automation = {
  ...staleAutomation,
  name: "新名称",
  enabled: true,
  updated_at: "2026-10-14T01:00:00Z",
};

function renderEditor(queryClient: QueryClient) {
  return render(
    <QueryClientProvider client={queryClient}>
      <MemoryRouter initialEntries={["/automations/a-1"]}>
        <Routes>
          <Route path="/automations" element={<div>自动化列表页</div>} />
          <Route path="/automations/:id" element={<AutomationEditorPage />} />
        </Routes>
      </MemoryRouter>
    </QueryClientProvider>,
  );
}

const setLang = (lang: string) => {
  Object.defineProperty(window.navigator, "language", {
    value: lang,
    configurable: true,
  });
  Object.defineProperty(window.navigator, "languages", {
    value: [lang],
    configurable: true,
  });
};

describe("AutomationEditorPage 详情缓存", () => {
  beforeEach(() => {
    setLang("zh-CN");
    signInAs("u1");
    vi.mocked(automationApi.get).mockReset();
    vi.mocked(automationApi.listRuns).mockResolvedValue({
      data: { items: [] },
    } as never);
    vi.mocked(environmentApi.list).mockResolvedValue({
      data: { items: [{ id: "env-1", name: "本机环境" }] },
    } as unknown as Awaited<ReturnType<typeof environmentApi.list>>);
  });

  afterEach(() => {
    teardownAuth();
    vi.clearAllMocks();
    setLang("en-US");
  });

  it("再次进入编辑页时用最新详情初始化表单，不复用旧缓存", async () => {
    vi.mocked(automationApi.get).mockResolvedValueOnce({
      data: staleAutomation,
    } as never);
    const queryClient = new QueryClient({
      defaultOptions: { queries: { retry: false } },
    });

    // 第一次进入：详情入缓存，表单是旧值（停用）。
    const first = renderEditor(queryClient);
    expect(await screen.findByDisplayValue("旧名称")).toBeInTheDocument();
    expect(screen.getByText("已停用")).toBeInTheDocument();
    expect(automationApi.get).toHaveBeenCalledTimes(1);

    // 返回列表（卸载编辑页），列表页的启停在服务端改了状态。
    first.unmount();
    vi.mocked(automationApi.get).mockResolvedValue({
      data: freshAutomation,
    } as never);
    // 详情查询 gcTime: 0，离开页面后缓存由 GC 定时器丢弃；等它跑完再进页面。
    await act(async () => {
      await new Promise((resolve) => setTimeout(resolve, 0));
    });

    // 再次进入：必然重新 fetch，表单只会挂在新数据上。
    renderEditor(queryClient);
    expect(await screen.findByDisplayValue("新名称")).toBeInTheDocument();
    expect(automationApi.get).toHaveBeenCalledTimes(2);
    expect(screen.getByText("已启用")).toBeInTheDocument();
    // 触发器也来自最新响应。
    expect(
      screen.getByText("定时（cron） · 每天 09:00（Asia/Shanghai）"),
    ).toBeInTheDocument();
  });
});
