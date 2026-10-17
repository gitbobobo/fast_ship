import { useState } from "react";
import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { QuotaAccountFormDialog } from "./account-form-dialog";
import { renderWithRoute } from "@/test/render";
import { signInAs, teardownAuth } from "@/test/auth";
import { quotaApi } from "@/lib/api/quota";

vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: vi.fn() } }));
vi.mock("@/lib/api/quota", () => ({
  quotaApi: {
    listProviders: vi.fn(),
    listAccounts: vi.fn(),
    createAccount: vi.fn(),
    updateAccount: vi.fn(),
    deleteAccount: vi.fn(),
    refreshAccount: vi.fn(),
    probe: vi.fn(),
  },
}));

const PROVIDERS: QuotaProvider[] = [
  {
    id: "commandcode",
    name: "Command Code",
    credential_fields: [
      {
        key: "api_key",
        label: "Command Code API Key",
        secret: true,
        required: true,
        placeholder: "user_...",
        help: "",
      },
    ],
    instructions: ["打开 Command Code 控制台创建 API key"],
    warning: "",
  },
  {
    id: "opencodego",
    name: "OpenCode Go",
    credential_fields: [
      {
        key: "api_key",
        label: "OpenCode Go API Key",
        secret: true,
        required: true,
        placeholder: "",
        help: "",
      },
    ],
    instructions: ["打开 OpenCode Go 页面获取 API key"],
    warning: "",
  },
  {
    id: "zai",
    name: "智谱 GLM Coding Plan",
    credential_fields: [
      {
        key: "api_key",
        label: "智谱 API Key",
        secret: true,
        required: true,
        placeholder: "",
        help: "",
      },
    ],
    instructions: ["打开 BigModel 控制台获取 API key"],
    warning: "",
  },
  {
    id: "grok",
    name: "Grok",
    credential_fields: [
      {
        key: "token",
        label: "Grok Token",
        secret: true,
        required: true,
        placeholder: "",
        help: "",
      },
    ],
    instructions: ["从 Grok CLI 会话中复制 token"],
    warning: "会话令牌权限较大且会过期，过期后请重新粘贴。",
  },
  {
    id: "codex",
    name: "Codex",
    credential_fields: [
      {
        key: "access_token",
        label: "Codex Access Token",
        secret: true,
        required: true,
        placeholder: "",
        help: "",
      },
      {
        key: "account_id",
        label: "Codex Account ID",
        secret: false,
        required: false,
        placeholder: "可选",
        help: "多账号时填写",
      },
    ],
    instructions: ["从 Codex CLI 登录态复制 access token"],
    warning: "会话令牌权限较大且会过期，过期后请重新粘贴。",
  },
  {
    id: "devin",
    name: "Devin",
    credential_fields: [
      {
        key: "token",
        label: "Devin Token",
        secret: true,
        required: true,
        placeholder: "",
        help: "",
      },
      {
        key: "org_id",
        label: "Devin Org ID",
        secret: false,
        required: true,
        placeholder: "org_xxx",
        help: "",
      },
    ],
    instructions: ["从 Devin 设置页获取 token 与 org id"],
    warning: "",
  },
  {
    id: "factory",
    name: "Factory Droid",
    plan_hint: "Pro 套餐",
    credential_fields: [
      {
        key: "api_key",
        label: "Factory API Key",
        secret: true,
        required: true,
        placeholder: "fk-...",
        help: "",
      },
    ],
    instructions: ["打开 Factory 设置页 → API Keys 创建 API key"],
    warning: "",
  },
];

function makeAccount(overrides: Partial<QuotaAccount> = {}): QuotaAccount {
  return {
    id: "acc-1",
    provider: "factory",
    provider_name: "Factory Droid",
    label: "我的 Pro",
    credential_hint: "…ab12",
    plan_name: "Pro",
    status: "ok",
    last_error: "",
    fetched_at: "2026-10-17T04:00:00Z",
    last_success_at: "2026-10-17T04:00:00Z",
    windows: [],
    balance: null,
    created_at: "2026-10-01T00:00:00Z",
    updated_at: "2026-10-01T00:00:00Z",
    ...overrides,
  };
}

function Harness({ account = null }: { account?: QuotaAccount | null }) {
  const [open, setOpen] = useState(false);
  return (
    <>
      <button type="button" onClick={() => setOpen(true)}>
        打开对话框
      </button>
      <QuotaAccountFormDialog
        open={open}
        account={account}
        onOpenChange={setOpen}
      />
    </>
  );
}

async function selectProvider(user: ReturnType<typeof userEvent.setup>, name: string) {
  await user.click(screen.getByRole("combobox", { name: "供应商" }));
  await user.click(
    await screen.findByRole("option", { name: new RegExp(name) }),
  );
}

beforeEach(() => {
  signInAs("A");
  vi.mocked(quotaApi.listProviders).mockResolvedValue({
    code: 0,
    message: "",
    data: PROVIDERS,
  });
  vi.mocked(quotaApi.listAccounts).mockResolvedValue({
    code: 0,
    message: "",
    data: [],
  });
});

afterEach(() => {
  teardownAuth();
  vi.clearAllMocks();
});

describe("QuotaAccountFormDialog 添加模式", () => {
  it("按元数据渲染全部 7 个供应商的凭据表单", async () => {
    const user = userEvent.setup();
    renderWithRoute(<Harness />, { path: "/", initialEntry: "/" });
    await user.click(screen.getByRole("button", { name: "打开对话框" }));

    for (const provider of PROVIDERS) {
      await selectProvider(user, provider.name);
      for (const field of provider.credential_fields) {
        const input = screen.getByLabelText(new RegExp(field.label));
        expect(input).toHaveAttribute(
          "type",
          field.secret ? "password" : "text",
        );
      }
      for (const step of provider.instructions) {
        expect(screen.getByText(step)).toBeInTheDocument();
      }
      if (provider.warning) {
        expect(screen.getByText(provider.warning)).toBeInTheDocument();
      }
    }
  });

  it("创建时提交 provider/label/credentials，凭据明文不出现在 DOM 文本里", async () => {
    const account = makeAccount();
    vi.mocked(quotaApi.createAccount).mockResolvedValue({
      code: 0,
      message: "",
      data: account,
    });
    const user = userEvent.setup();
    renderWithRoute(<Harness />, { path: "/", initialEntry: "/" });
    await user.click(screen.getByRole("button", { name: "打开对话框" }));

    await selectProvider(user, "Factory Droid");
    await user.type(screen.getByLabelText("标签"), "我的 Pro");
    await user.type(screen.getByLabelText("Factory API Key"), "fk-secret-123");
    // 输入框是 password 类型；明文只存在于 input value，不进入任何 DOM 文本
    expect(screen.getByLabelText("Factory API Key")).toHaveAttribute(
      "type",
      "password",
    );
    await user.click(screen.getByRole("button", { name: "保存" }));

    await waitFor(() =>
      expect(quotaApi.createAccount).toHaveBeenCalledWith({
        provider: "factory",
        label: "我的 Pro",
        credentials: { api_key: "fk-secret-123" },
      }),
    );
    // 保存成功后对话框自动关闭，输入框已卸载，明文不会残留在 DOM 文本里
    expect(document.body.textContent).not.toContain("fk-secret-123");
  });

  it("必填字段缺失时不发请求", async () => {
    const user = userEvent.setup();
    renderWithRoute(<Harness />, { path: "/", initialEntry: "/" });
    await user.click(screen.getByRole("button", { name: "打开对话框" }));

    await selectProvider(user, "Factory Droid");
    await user.type(screen.getByLabelText("标签"), "缺凭据");
    await user.click(screen.getByRole("button", { name: "保存" }));

    expect(quotaApi.createAccount).not.toHaveBeenCalled();
  });

  it("测试按钮调用 /api/quota/test 并就地展示结果", async () => {
    vi.mocked(quotaApi.probe).mockResolvedValue({
      code: 0,
      message: "",
      data: {
        status: "ok",
        plan_name: "Pro",
        windows: [
          {
            kind: "daily",
            label: "每天",
            used_percent: 38,
            used: null,
            limit: null,
            unit: "percent",
            reset_at: null,
          },
        ],
        balance: null,
        error: "",
        detail: "",
      },
    });
    const user = userEvent.setup();
    renderWithRoute(<Harness />, { path: "/", initialEntry: "/" });
    await user.click(screen.getByRole("button", { name: "打开对话框" }));

    await selectProvider(user, "Factory Droid");
    // 未填凭据时测试按钮不可用
    expect(screen.getByRole("button", { name: "测试" })).toBeDisabled();

    await user.type(screen.getByLabelText("Factory API Key"), "fk-probe-1");
    await user.click(screen.getByRole("button", { name: "测试" }));

    await waitFor(() =>
      expect(quotaApi.probe).toHaveBeenCalledWith({
        provider: "factory",
        credentials: { api_key: "fk-probe-1" },
      }),
    );
    expect(await screen.findByText("凭据有效")).toBeInTheDocument();
    expect(screen.getByText(/套餐：Pro/)).toBeInTheDocument();
    expect(screen.getByTestId("quota-probe-result").textContent).not.toContain(
      "fk-probe-1",
    );
  });

  it("关闭后重开，凭据输入与探测结果都被清空", async () => {
    vi.mocked(quotaApi.probe).mockResolvedValue({
      code: 0,
      message: "",
      data: {
        status: "ok",
        plan_name: "Pro",
        windows: [],
        balance: null,
        error: "",
        detail: "",
      },
    });
    const user = userEvent.setup();
    renderWithRoute(<Harness />, { path: "/", initialEntry: "/" });
    await user.click(screen.getByRole("button", { name: "打开对话框" }));

    await selectProvider(user, "Factory Droid");
    await user.type(
      screen.getByLabelText("Factory API Key"),
      "fk-should-clear",
    );
    await user.click(screen.getByRole("button", { name: "测试" }));
    expect(await screen.findByText("凭据有效")).toBeInTheDocument();

    await user.keyboard("{Escape}");
    await user.click(screen.getByRole("button", { name: "打开对话框" }));

    await selectProvider(user, "Factory Droid");
    expect(screen.getByLabelText("Factory API Key")).toHaveValue("");
    expect(screen.getByLabelText("标签")).toHaveValue("");
    expect(screen.queryByTestId("quota-probe-result")).toBeNull();
  });
});

describe("QuotaAccountFormDialog 编辑模式", () => {
  it("供应商锁定、标签预填、凭据回显 hint 且留空表示不修改", async () => {
    const account = makeAccount();
    vi.mocked(quotaApi.updateAccount).mockResolvedValue({
      code: 0,
      message: "",
      data: account,
    });
    const user = userEvent.setup();
    renderWithRoute(<Harness account={account} />, {
      path: "/",
      initialEntry: "/",
    });
    await user.click(screen.getByRole("button", { name: "打开对话框" }));

    // 编辑模式没有供应商下拉，显示固定供应商与 hint
    expect(screen.queryByRole("combobox", { name: "供应商" })).toBeNull();
    expect(screen.getByText("Factory Droid")).toBeInTheDocument();
    expect(screen.getByText(/当前 …ab12/)).toBeInTheDocument();
    expect(screen.getByLabelText("标签")).toHaveValue("我的 Pro");
    expect(screen.getByLabelText("Factory API Key")).toHaveValue("");

    await user.click(screen.getByRole("button", { name: "保存" }));
    await waitFor(() =>
      expect(quotaApi.updateAccount).toHaveBeenCalledWith("acc-1", {
        label: "我的 Pro",
      }),
    );
  });

  it("填入凭据字段则整组替换提交", async () => {
    const account = makeAccount({ provider: "devin", provider_name: "Devin" });
    vi.mocked(quotaApi.updateAccount).mockResolvedValue({
      code: 0,
      message: "",
      data: account,
    });
    const user = userEvent.setup();
    renderWithRoute(<Harness account={account} />, {
      path: "/",
      initialEntry: "/",
    });
    await user.click(screen.getByRole("button", { name: "打开对话框" }));

    // 只填一个必填字段不完整，提交被拒绝
    await user.type(screen.getByLabelText("Devin Token"), "dt-new-1");
    await user.click(screen.getByRole("button", { name: "保存" }));
    expect(quotaApi.updateAccount).not.toHaveBeenCalled();

    await user.type(screen.getByLabelText("Devin Org ID"), "org_9");
    await user.click(screen.getByRole("button", { name: "保存" }));
    await waitFor(() =>
      expect(quotaApi.updateAccount).toHaveBeenCalledWith("acc-1", {
        label: "我的 Pro",
        credentials: { token: "dt-new-1", org_id: "org_9" },
      }),
    );
    expect(document.body.textContent).not.toContain("dt-new-1");
  });
});
