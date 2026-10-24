import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { toast } from "sonner";

import { ForgeIntegrationsCard } from "./forge-integrations-card";
import { ApiRequestError } from "@/lib/api/client";
import {
  useCreateForgeIntegration,
  useDeleteForgeIntegration,
  useForgeIntegrations,
  useTestForgeIntegration,
  useUpdateForgeIntegration,
} from "@/lib/hooks/use-forge-integrations";
import { signInAs, teardownAuth } from "@/test/auth";

vi.mock("sonner", () => ({
  toast: { success: vi.fn(), error: vi.fn() },
}));

vi.mock("@/lib/hooks/use-forge-integrations", () => ({
  useForgeIntegrations: vi.fn(),
  useTestForgeIntegration: vi.fn(),
  useDeleteForgeIntegration: vi.fn(),
  useCreateForgeIntegration: vi.fn(),
  useUpdateForgeIntegration: vi.fn(),
}));

const testMutateAsync = vi.fn();
const deleteMutateAsync = vi.fn();
const createMutateAsync = vi.fn();
const updateMutateAsync = vi.fn();

function mockMutation(mock: ReturnType<typeof vi.fn>) {
  return { mutateAsync: mock, isPending: false };
}

const builtinRow: ForgeIntegration = {
  id: "github-default",
  kind: "github",
  name: "github-default",
  base_url: "https://github.com",
  api_base_url: "https://api.github.com",
  token_set: true,
  webhook_secret_set: false,
  has_ca_cert: false,
  allow_private_network: false,
  builtin: true,
  created_at: null,
  updated_at: null,
};

const forgejoRow: ForgeIntegration = {
  id: "fj-1",
  kind: "forgejo",
  name: "自建 Forgejo",
  base_url: "https://forge.example.com",
  api_base_url: "https://forge.example.com/api/v1",
  token_set: true,
  webhook_secret_set: true,
  has_ca_cert: true,
  allow_private_network: true,
  builtin: false,
  created_at: "2026-10-01T00:00:00Z",
  updated_at: "2026-10-01T00:00:00Z",
};

function mockList(rows: ForgeIntegration[], isLoading = false) {
  vi.mocked(useForgeIntegrations).mockReturnValue({
    data: rows,
    isLoading,
    isError: false,
    isFetching: false,
    refetch: vi.fn(),
  } as unknown as ReturnType<typeof useForgeIntegrations>);
}

beforeEach(() => {
  vi.clearAllMocks();
  vi.mocked(useTestForgeIntegration).mockReturnValue(
    mockMutation(testMutateAsync) as unknown as ReturnType<
      typeof useTestForgeIntegration
    >,
  );
  vi.mocked(useDeleteForgeIntegration).mockReturnValue(
    mockMutation(deleteMutateAsync) as unknown as ReturnType<
      typeof useDeleteForgeIntegration
    >,
  );
  vi.mocked(useCreateForgeIntegration).mockReturnValue(
    mockMutation(createMutateAsync) as unknown as ReturnType<
      typeof useCreateForgeIntegration
    >,
  );
  vi.mocked(useUpdateForgeIntegration).mockReturnValue(
    mockMutation(updateMutateAsync) as unknown as ReturnType<
      typeof useUpdateForgeIntegration
    >,
  );
});

afterEach(() => teardownAuth());

describe("ForgeIntegrationsCard", () => {
  it("内置行只读：内置徽标、GitHub App 凭证状态与去下方卡片管理的提示", () => {
    signInAs("A", "admin");
    mockList([builtinRow, forgejoRow]);
    render(<ForgeIntegrationsCard />);

    const row = screen
      .getByText("github-default")
      .closest("div.border-b") as HTMLElement;
    expect(row).toHaveTextContent("内置");
    expect(row).toHaveTextContent("GitHub App 已配置");
    expect(row).toHaveTextContent("在下方 GitHub App 卡片中管理");
    expect(within(row).queryByRole("button")).toBeNull();
  });

  it("forgejo 行展示名称、地址、种类与状态徽标", () => {
    signInAs("A");
    mockList([builtinRow, forgejoRow]);
    render(<ForgeIntegrationsCard />);

    const row = screen
      .getByText("自建 Forgejo")
      .closest("div.border-b") as HTMLElement;
    expect(row).toHaveTextContent("Forgejo");
    expect(row).toHaveTextContent("https://forge.example.com");
    expect(row).toHaveTextContent("凭证已配置");
    expect(row).toHaveTextContent("允许内网");
    expect(row).toHaveTextContent("自定义 CA");
  });

  it("非管理员没有新建、测试连接、编辑、删除入口", () => {
    signInAs("A");
    mockList([builtinRow, forgejoRow]);
    render(<ForgeIntegrationsCard />);

    expect(screen.queryByRole("button", { name: "新建集成" })).toBeNull();
    expect(screen.queryByRole("button", { name: /测试连接/ })).toBeNull();
    expect(
      screen.queryByRole("button", { name: "编辑 自建 Forgejo" }),
    ).toBeNull();
    expect(
      screen.queryByRole("button", { name: "删除 自建 Forgejo" }),
    ).toBeNull();
  });

  it("加载中显示骨架，失败显示重试", () => {
    signInAs("A", "admin");
    mockList([], true);
    const { rerender } = render(<ForgeIntegrationsCard />);
    expect(document.querySelector("[data-slot=skeleton]")).toBeInTheDocument();

    vi.mocked(useForgeIntegrations).mockReturnValue({
      data: undefined,
      isLoading: false,
      isError: true,
      isFetching: false,
      refetch: vi.fn(),
    } as unknown as ReturnType<typeof useForgeIntegrations>);
    rerender(<ForgeIntegrationsCard />);
    expect(screen.getByText("集成列表加载失败")).toBeInTheDocument();
  });

  it("测试连接成功时 toast 实例版本与机器人账号", async () => {
    signInAs("A", "admin");
    mockList([builtinRow, forgejoRow]);
    testMutateAsync.mockResolvedValue({
      data: { status: "ok", version: "v11.0.1", username: "fs-bot", detail: "" },
    });
    const user = userEvent.setup();
    render(<ForgeIntegrationsCard />);

    await user.click(screen.getByRole("button", { name: /测试连接/ }));
    await waitFor(() =>
      expect(toast.success).toHaveBeenCalledWith(
        "实例版本 v11.0.1 · 账号 @fs-bot",
      ),
    );
  });

  it("测试连接凭证被拒时 toast 服务端说明", async () => {
    signInAs("A", "admin");
    mockList([builtinRow, forgejoRow]);
    testMutateAsync.mockResolvedValue({
      data: { status: "invalid", version: "", username: "", detail: "401 未授权" },
    });
    const user = userEvent.setup();
    render(<ForgeIntegrationsCard />);

    await user.click(screen.getByRole("button", { name: /测试连接/ }));
    await waitFor(() => expect(toast.error).toHaveBeenCalledWith("401 未授权"));
  });

  it("删除被引用的集成时 toast 服务端 message（含项目名）", async () => {
    signInAs("A", "admin");
    mockList([builtinRow, forgejoRow]);
    deleteMutateAsync.mockRejectedValue(
      new ApiRequestError(409, 40935, "该集成仍被项目引用：Fast Ship、官网"),
    );
    const user = userEvent.setup();
    render(<ForgeIntegrationsCard />);

    await user.click(
      screen.getByRole("button", { name: "删除 自建 Forgejo" }),
    );
    const confirm = await screen.findByRole("alertdialog");
    await user.click(
      within(confirm).getByRole("button", { name: "确认删除" }),
    );
    await waitFor(() =>
      expect(toast.error).toHaveBeenCalledWith(
        "该集成仍被项目引用：Fast Ship、官网",
      ),
    );
  });

  it("新建：必填项提交 create 端点，开关状态随表单提交", async () => {
    signInAs("A", "admin");
    mockList([builtinRow]);
    createMutateAsync.mockResolvedValue({ data: forgejoRow });
    const user = userEvent.setup();
    render(<ForgeIntegrationsCard />);

    await user.click(screen.getByRole("button", { name: "新建集成" }));
    const dialog = await screen.findByRole("dialog");
    await user.type(within(dialog).getByLabelText("名称"), "自建 Forgejo");
    await user.type(
      within(dialog).getByLabelText("实例地址"),
      "https://forge.example.com",
    );
    await user.type(
      within(dialog).getByLabelText("访问令牌（PAT）"),
      "pat-123",
    );
    // base-ui Switch 会渲染同 id 的隐藏 input，用 role 定位可见开关。
    await user.click(within(dialog).getByRole("switch"));
    await user.click(within(dialog).getByRole("button", { name: "保存" }));

    await waitFor(() =>
      expect(createMutateAsync).toHaveBeenCalledWith({
        name: "自建 Forgejo",
        base_url: "https://forge.example.com",
        token: "pat-123",
        allow_private_network: true,
      }),
    );
  });

  it("新建：令牌留空被拦截，不发请求", async () => {
    signInAs("A", "admin");
    mockList([builtinRow]);
    const user = userEvent.setup();
    render(<ForgeIntegrationsCard />);

    await user.click(screen.getByRole("button", { name: "新建集成" }));
    const dialog = await screen.findByRole("dialog");
    await user.type(within(dialog).getByLabelText("名称"), "自建");
    await user.type(
      within(dialog).getByLabelText("实例地址"),
      "https://forge.example.com",
    );
    await user.click(within(dialog).getByRole("button", { name: "保存" }));

    expect(await within(dialog).findByText("请输入访问令牌（PAT）"))
      .toBeInTheDocument();
    expect(createMutateAsync).not.toHaveBeenCalled();
  });

  it("编辑：凭证留空=保留（payload 不含 token），勾选清空证书提交空串", async () => {
    signInAs("A", "admin");
    mockList([builtinRow, forgejoRow]);
    updateMutateAsync.mockResolvedValue({ data: forgejoRow });
    const user = userEvent.setup();
    render(<ForgeIntegrationsCard />);

    await user.click(screen.getByRole("button", { name: "编辑 自建 Forgejo" }));
    const dialog = await screen.findByRole("dialog");
    // token 与 webhook_secret 都已设置，两处帮助文案相同。
    const helpers = await within(dialog).findAllByText(
      "已设置，留空保持不变",
    );
    expect(helpers.length).toBeGreaterThan(0);
    await user.click(
      within(dialog).getByRole("checkbox", { name: "清空已存证书" }),
    );
    await user.click(within(dialog).getByRole("button", { name: "保存" }));

    await waitFor(() => expect(updateMutateAsync).toHaveBeenCalled());
    const { id, input } = updateMutateAsync.mock.calls[0][0];
    expect(id).toBe("fj-1");
    expect(input.name).toBe("自建 Forgejo");
    expect(input.base_url).toBe("https://forge.example.com");
    // 留空字段不提交：token/webhook_secret 保留旧值（契约：省略即保留）。
    expect(input.token).toBeUndefined();
    expect(input.webhook_secret).toBeUndefined();
    expect(input.ca_cert_pem).toBe("");
    // api_base_url 未改动不提交：预填值原样回发会让服务端误判为显式钉死，
    // 改 base_url 后推导值无法跟随新实例（P1 修复）。
    expect(input.api_base_url).toBeUndefined();
  });

  it("编辑：清空 API 基址提交空串 = 重置为默认", async () => {
    signInAs("A", "admin");
    mockList([builtinRow, forgejoRow]);
    updateMutateAsync.mockResolvedValue({ data: forgejoRow });
    const user = userEvent.setup();
    render(<ForgeIntegrationsCard />);

    await user.click(screen.getByRole("button", { name: "编辑 自建 Forgejo" }));
    const dialog = await screen.findByRole("dialog");
    await user.click(
      within(dialog).getByRole("button", { name: "重置为默认" }),
    );
    await user.click(within(dialog).getByRole("button", { name: "保存" }));

    await waitFor(() => expect(updateMutateAsync).toHaveBeenCalled());
    const { input } = updateMutateAsync.mock.calls[0][0];
    expect(input.api_base_url).toBe("");
  });
});
