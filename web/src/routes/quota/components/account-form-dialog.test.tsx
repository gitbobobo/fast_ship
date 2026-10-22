import { useState } from "react";
import { act, fireEvent, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { toast } from "sonner";
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
    startDeviceAuthorization: vi.fn(),
    getDeviceAuthorization: vi.fn(),
    cancelDeviceAuthorization: vi.fn(),
  },
}));

const PROVIDERS: QuotaProvider[] = [
  {
    id: "commandcode",
    name: "Command Code",
    plan_hint: "",
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
    device_authorization: false,
  },
  {
    id: "opencodego",
    name: "OpenCode Go",
    plan_hint: "",
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
    device_authorization: false,
  },
  {
    id: "zai",
    name: "智谱 GLM Coding Plan",
    plan_hint: "",
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
    device_authorization: false,
  },
  {
    id: "grok",
    name: "Grok",
    plan_hint: "",
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
    device_authorization: false,
  },
  {
    id: "codex",
    name: "Codex",
    plan_hint: "",
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
    device_authorization: false,
  },
  {
    id: "devin",
    name: "Devin",
    plan_hint: "",
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
    device_authorization: false,
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
    device_authorization: false,
  },
  {
    id: "opendesign",
    name: "OpenDesign",
    plan_hint: "Design Plan",
    credential_fields: [
      {
        key: "control_key",
        label: "控制面密钥",
        secret: true,
        required: true,
        placeholder: "vela_ctrl_…",
        help: "控制面密钥以 vela_ctrl_ 开头",
      },
    ],
    instructions: ["点「使用浏览器授权」，在打开的页面点批准（推荐）"],
    warning:
      "网页「API Key」页给的 sk-amr-… 是调模型用的密钥，查不了额度；填了会直接报错。",
    device_authorization: true,
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
    note: "",
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
  vi.useRealTimers();
  vi.clearAllMocks();
});

describe("QuotaAccountFormDialog 添加模式", () => {
  // 每供应商一个用例：单例只做一次下拉选择，串行遍历 8 家会顶到测试超时
  for (const provider of PROVIDERS) {
    it(`按元数据渲染 ${provider.name} 的凭据表单`, async () => {
      const user = userEvent.setup();
      renderWithRoute(<Harness />, { path: "/", initialEntry: "/" });
      await user.click(screen.getByRole("button", { name: "打开对话框" }));

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
    });
  }

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
        note: "",
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
        note: "",
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

describe("QuotaAccountFormDialog 设备授权", () => {
  const AUTH: QuotaDeviceAuthorization = {
    id: "da-1",
    provider: "opendesign",
    status: "pending",
    user_code: "ABCD-EFGH",
    activation_url: "https://open-design.ai/activate",
    // 远未来时间，倒计时断言不受测试耗时影响
    expires_at: "2099-01-01T00:00:00Z",
    poll_interval_seconds: 60,
    account_id: "",
    error: "",
  };

  beforeEach(() => {
    vi.mocked(quotaApi.getDeviceAuthorization).mockResolvedValue({
      code: 0,
      message: "",
      data: AUTH,
    });
    vi.mocked(quotaApi.cancelDeviceAuthorization).mockResolvedValue({
      code: 0,
      message: "",
      data: null,
    });
  });

  it("只有 device_authorization=true 的供应商出现「使用浏览器授权」", async () => {
    const user = userEvent.setup();
    renderWithRoute(<Harness />, { path: "/", initialEntry: "/" });
    await user.click(screen.getByRole("button", { name: "打开对话框" }));

    await selectProvider(user, "Factory Droid");
    expect(
      screen.queryByRole("button", { name: "使用浏览器授权" }),
    ).toBeNull();

    await selectProvider(user, "OpenDesign");
    expect(
      screen.getByRole("button", { name: "使用浏览器授权" }),
    ).toBeInTheDocument();
    expect(screen.getByText("或粘贴控制面密钥")).toBeInTheDocument();
    // 粘贴密钥路径仍然保留
    expect(screen.getByLabelText("控制面密钥")).toBeInTheDocument();

    await selectProvider(user, "Codex");
    expect(
      screen.queryByRole("button", { name: "使用浏览器授权" }),
    ).toBeNull();
  });

  it("标签为空时点授权 toast 提示且不发请求", async () => {
    const user = userEvent.setup();
    renderWithRoute(<Harness />, { path: "/", initialEntry: "/" });
    await user.click(screen.getByRole("button", { name: "打开对话框" }));

    await selectProvider(user, "OpenDesign");
    await user.click(
      screen.getByRole("button", { name: "使用浏览器授权" }),
    );

    expect(toast.error).toHaveBeenCalledWith("请填写账号标签");
    expect(quotaApi.startDeviceAuthorization).not.toHaveBeenCalled();
  });

  it("POST 成功后切入授权面板，「取消」回到表单并 DELETE 会话", async () => {
    vi.mocked(quotaApi.startDeviceAuthorization).mockResolvedValue({
      code: 0,
      message: "",
      data: AUTH,
    });
    const user = userEvent.setup();
    renderWithRoute(<Harness />, { path: "/", initialEntry: "/" });
    await user.click(screen.getByRole("button", { name: "打开对话框" }));

    await selectProvider(user, "OpenDesign");
    await user.type(screen.getByLabelText("标签"), "我的 Design");
    await user.click(
      screen.getByRole("button", { name: "使用浏览器授权" }),
    );

    await waitFor(() =>
      expect(quotaApi.startDeviceAuthorization).toHaveBeenCalledWith({
        provider: "opendesign",
        label: "我的 Design",
      }),
    );
    // 新增模式不带 account_id（仅编辑模式带，B4/A5）
    expect(
      vi.mocked(quotaApi.startDeviceAuthorization).mock.calls[0][0],
    ).not.toHaveProperty("account_id");
    expect(await screen.findByTestId("device-auth-panel")).toBeInTheDocument();
    expect(screen.getByTestId("device-auth-user-code")).toHaveTextContent(
      "ABCD-EFGH",
    );
    expect(screen.getByRole("link", { name: /打开批准页面/ })).toHaveAttribute(
      "href",
      "https://open-design.ai/activate",
    );
    // 授权视图中表单按钮（保存/测试）不再出现
    expect(screen.queryByRole("button", { name: "保存" })).toBeNull();
    expect(screen.queryByRole("button", { name: "测试" })).toBeNull();

    await user.click(screen.getByRole("button", { name: "取消" }));
    expect(
      await screen.findByRole("button", { name: "保存" }),
    ).toBeInTheDocument();
    // 面板卸载后对 pending 会话发 DELETE（失败静默），且只发一次
    await waitFor(() =>
      expect(quotaApi.cancelDeviceAuthorization).toHaveBeenCalledWith("da-1"),
    );
    expect(quotaApi.cancelDeviceAuthorization).toHaveBeenCalledTimes(1);
  });

  it("轮询到 approved：toast、关对话框、invalidate 额度数据，不发 DELETE", async () => {
    // 面板轮询到 approved 时父组件 state 里仍是 POST 返回的 pending 视图——
    // 用 fake timers + fireEvent（面板测试同款约定）推进一个轮询周期，走通
    // 「面板停轮 → onApproved → 父组件收尾」的真实路径；approved 会话服务端
    // 已消费，收尾不能再发 DELETE。走编辑模式发起，避开供应商下拉。
    const account = makeAccount({
      provider: "opendesign",
      provider_name: "OpenDesign",
    });
    const session = { ...AUTH, poll_interval_seconds: 2 };
    vi.mocked(quotaApi.startDeviceAuthorization).mockResolvedValue({
      code: 0,
      message: "",
      data: session,
    });
    vi.mocked(quotaApi.getDeviceAuthorization).mockResolvedValue({
      code: 0,
      message: "",
      data: { ...AUTH, status: "approved" },
    });
    vi.useFakeTimers({
      toFake: [
        "setTimeout",
        "clearTimeout",
        "setInterval",
        "clearInterval",
        "Date",
      ],
    });
    try {
      renderWithRoute(<Harness account={account} />, {
        path: "/",
        initialEntry: "/",
      });
      fireEvent.click(screen.getByRole("button", { name: "打开对话框" }));
      // 供应商元数据是 useQuery 异步加载的：推进 0ms 把 react-query
      // 内部用 setTimeout 调的取数放出来，等它落地后授权按钮才出现
      await act(() => vi.advanceTimersByTimeAsync(0));
      fireEvent.click(
        screen.getByRole("button", { name: "使用浏览器授权" }),
      );
      await act(() => vi.advanceTimersByTimeAsync(0));
      expect(
        screen.getByTestId("device-auth-panel"),
      ).toBeInTheDocument();
      // providers 与 accounts 同挂在 ["quota", userId] 前缀下，活跃的
      // providers 查询被 invalidate 会立刻重拉，用调用次数观察 invalidate
      const providerCalls =
        vi.mocked(quotaApi.listProviders).mock.calls.length;

      await act(() => vi.advanceTimersByTimeAsync(2000));
      // 等面板 approved 回调与关闭路径里的收尾全部落地
      await act(async () => {});

      expect(toast.success).toHaveBeenCalledWith(
        "授权成功，额度稍后自动拉取",
      );
      expect(screen.queryByTestId("device-auth-panel")).toBeNull();
      expect(screen.queryByRole("button", { name: "保存" })).toBeNull();
      expect(quotaApi.listProviders).toHaveBeenCalledTimes(
        providerCalls + 1,
      );
      expect(quotaApi.cancelDeviceAuthorization).not.toHaveBeenCalled();
    } finally {
      vi.useRealTimers();
    }
  });

  it("关闭对话框也结束授权会话", async () => {
    vi.mocked(quotaApi.startDeviceAuthorization).mockResolvedValue({
      code: 0,
      message: "",
      data: AUTH,
    });
    const user = userEvent.setup();
    renderWithRoute(<Harness />, { path: "/", initialEntry: "/" });
    await user.click(screen.getByRole("button", { name: "打开对话框" }));

    await selectProvider(user, "OpenDesign");
    await user.type(screen.getByLabelText("标签"), "我的 Design");
    await user.click(
      screen.getByRole("button", { name: "使用浏览器授权" }),
    );
    expect(await screen.findByTestId("device-auth-panel")).toBeInTheDocument();

    await user.keyboard("{Escape}");
    await waitFor(() =>
      expect(quotaApi.cancelDeviceAuthorization).toHaveBeenCalledWith("da-1"),
    );
    // 关闭路径的 DELETE 只发一次（卸载 cleanup 不重复发）
    expect(quotaApi.cancelDeviceAuthorization).toHaveBeenCalledTimes(1);
  });

  it("组件卸载（离开路由）也对 pending 会话发 DELETE", async () => {
    vi.mocked(quotaApi.startDeviceAuthorization).mockResolvedValue({
      code: 0,
      message: "",
      data: AUTH,
    });
    const user = userEvent.setup();
    const { unmount } = renderWithRoute(<Harness />, {
      path: "/",
      initialEntry: "/",
    });
    await user.click(screen.getByRole("button", { name: "打开对话框" }));

    await selectProvider(user, "OpenDesign");
    await user.type(screen.getByLabelText("标签"), "我的 Design");
    await user.click(
      screen.getByRole("button", { name: "使用浏览器授权" }),
    );
    expect(await screen.findByTestId("device-auth-panel")).toBeInTheDocument();

    // 不关对话框直接卸载整棵树（路由跳转）：pending 会话收到 DELETE
    act(() => unmount());
    await waitFor(() =>
      expect(quotaApi.cancelDeviceAuthorization).toHaveBeenCalledTimes(1),
    );
    expect(quotaApi.cancelDeviceAuthorization).toHaveBeenCalledWith("da-1");
  });

  it("在途 POST 未回就卸载：代次作废，响应落地后立刻 DELETE", async () => {
    let resolveStart!: (value: {
      code: number;
      message: string;
      data: QuotaDeviceAuthorization;
    }) => void;
    vi.mocked(quotaApi.startDeviceAuthorization).mockImplementation(
      () =>
        new Promise((resolve) => {
          resolveStart = resolve;
        }),
    );
    const user = userEvent.setup();
    const { unmount } = renderWithRoute(<Harness />, {
      path: "/",
      initialEntry: "/",
    });
    await user.click(screen.getByRole("button", { name: "打开对话框" }));

    await selectProvider(user, "OpenDesign");
    await user.type(screen.getByLabelText("标签"), "我的 Design");
    await user.click(
      screen.getByRole("button", { name: "使用浏览器授权" }),
    );
    await waitFor(() =>
      expect(quotaApi.startDeviceAuthorization).toHaveBeenCalledTimes(1),
    );

    // 响应未回就卸载：还没有会话 id 可删，此刻不应发 DELETE
    act(() => unmount());
    expect(quotaApi.cancelDeviceAuthorization).not.toHaveBeenCalled();

    // 迟到的响应因代次不匹配被丢弃，拿到的会话立刻 DELETE 收尾
    await act(async () => {
      resolveStart({ code: 0, message: "", data: AUTH });
    });
    await waitFor(() =>
      expect(quotaApi.cancelDeviceAuthorization).toHaveBeenCalledTimes(1),
    );
    expect(quotaApi.cancelDeviceAuthorization).toHaveBeenCalledWith("da-1");
  });

  it("发起响应迟到且对话框已重开：丢弃旧会话并 DELETE，不进入授权面板（B2）", async () => {
    let resolveStart!: (value: {
      code: number;
      message: string;
      data: QuotaDeviceAuthorization;
    }) => void;
    vi.mocked(quotaApi.startDeviceAuthorization).mockImplementation(
      () =>
        new Promise((resolve) => {
          resolveStart = resolve;
        }),
    );
    const user = userEvent.setup();
    renderWithRoute(<Harness />, { path: "/", initialEntry: "/" });
    await user.click(screen.getByRole("button", { name: "打开对话框" }));

    await selectProvider(user, "OpenDesign");
    await user.type(screen.getByLabelText("标签"), "我的 Design");
    await user.click(
      screen.getByRole("button", { name: "使用浏览器授权" }),
    );
    await waitFor(() =>
      expect(quotaApi.startDeviceAuthorization).toHaveBeenCalledTimes(1),
    );

    // 响应未回就关闭、再重新打开（请求代次已递增两次，在途响应作废）
    await user.keyboard("{Escape}");
    await user.click(screen.getByRole("button", { name: "打开对话框" }));

    await act(async () => {
      resolveStart({ code: 0, message: "", data: AUTH });
    });

    // 迟到的会话立刻 DELETE 丢弃，不把新表单切到旧授权面板
    await waitFor(() =>
      expect(quotaApi.cancelDeviceAuthorization).toHaveBeenCalledWith("da-1"),
    );
    // 关闭+重开路径只触发这一次 DELETE，不重复发
    expect(quotaApi.cancelDeviceAuthorization).toHaveBeenCalledTimes(1);
    expect(screen.queryByTestId("device-auth-panel")).toBeNull();
    expect(
      screen.getByRole("button", { name: "保存" }),
    ).toBeInTheDocument();
  });

  it("编辑 device_authorization 供应商的账号：显示授权入口且发起时带 account_id（B4）", async () => {
    const account = makeAccount({
      provider: "opendesign",
      provider_name: "OpenDesign",
    });
    vi.mocked(quotaApi.startDeviceAuthorization).mockResolvedValue({
      code: 0,
      message: "",
      data: AUTH,
    });
    const user = userEvent.setup();
    renderWithRoute(<Harness account={account} />, {
      path: "/",
      initialEntry: "/",
    });
    await user.click(screen.getByRole("button", { name: "打开对话框" }));

    // 编辑模式也提供授权入口，批准后服务端整组替换该账号凭据（A5）
    await user.click(
      screen.getByRole("button", { name: "使用浏览器授权" }),
    );
    await waitFor(() =>
      expect(quotaApi.startDeviceAuthorization).toHaveBeenCalledWith({
        provider: "opendesign",
        label: "我的 Pro",
        account_id: "acc-1",
      }),
    );
    expect(await screen.findByTestId("device-auth-panel")).toBeInTheDocument();

    // 取消回到表单并对 pending 会话发 DELETE，粘贴密钥路径仍在
    await user.click(screen.getByRole("button", { name: "取消" }));
    expect(
      await screen.findByRole("button", { name: "保存" }),
    ).toBeInTheDocument();
    await waitFor(() =>
      expect(quotaApi.cancelDeviceAuthorization).toHaveBeenCalledWith("da-1"),
    );
    expect(screen.getByLabelText("控制面密钥")).toBeInTheDocument();
  });

  it("编辑非 device_authorization 供应商的账号不显示授权入口", async () => {
    const user = userEvent.setup();
    renderWithRoute(<Harness account={makeAccount()} />, {
      path: "/",
      initialEntry: "/",
    });
    await user.click(screen.getByRole("button", { name: "打开对话框" }));

    expect(
      screen.queryByRole("button", { name: "使用浏览器授权" }),
    ).toBeNull();
    expect(screen.getByLabelText("Factory API Key")).toBeInTheDocument();
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
