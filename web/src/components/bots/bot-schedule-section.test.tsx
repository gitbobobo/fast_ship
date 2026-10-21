import { useState } from "react";
import { render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { toast } from "sonner";
import {
  BotScheduleSection,
  BotScheduleTaskDialog,
} from "./bot-schedule-section";
import { renderWithRoute } from "@/test/render";
import { signInAs, teardownAuth } from "@/test/auth";
import { ApiRequestError } from "@/lib/api/client";
import type { BotScheduleRun, BotScheduleTask } from "@/lib/api/bots";
import { formatDate } from "@/lib/utils/format";
import {
  botKeys,
  useBotScheduleTaskRuns,
  useBotScheduleTasks,
} from "@/lib/hooks/use-bots";

vi.mock("sonner", () => ({
  toast: { success: vi.fn(), error: vi.fn() },
}));

const { setEnabledMutateAsync, clearStateMutateAsync, stopScheduleRun } =
  vi.hoisted(() => ({
    setEnabledMutateAsync: vi.fn(),
    clearStateMutateAsync: vi.fn(),
    stopScheduleRun: vi.fn(),
  }));

// 组件直连 botApi.stopScheduleRun（INT-137）；其余方法保留真实实现。
vi.mock("@/lib/api/bots", async (importOriginal) => {
  const original = await importOriginal<typeof import("@/lib/api/bots")>();
  return {
    ...original,
    botApi: { ...original.botApi, stopScheduleRun },
  };
});

vi.mock("@/lib/hooks/use-bots", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/lib/hooks/use-bots")>()),
  useBotScheduleTasks: vi.fn(() => ({ data: [], isLoading: false })),
  useBotScheduleTaskRuns: vi.fn(() => ({ data: [], isLoading: false })),
  useSetBotScheduleTaskEnabled: vi.fn(() => ({
    mutateAsync: setEnabledMutateAsync,
    isPending: false,
  })),
  useClearBotScheduleTaskState: vi.fn(() => ({
    mutateAsync: clearStateMutateAsync,
    isPending: false,
  })),
}));

const enabledTask: BotScheduleTask = {
  id: "t1",
  bot_id: "b1",
  name: "每日汇总",
  cron_expression: "0 9 * * *",
  timezone: "Asia/Shanghai",
  instruction: "汇总昨天的 Issue 变化并发到对话里",
  model: "",
  needs_report: true,
  enabled: true,
  next_run_at: "2026-10-18T01:00:00Z",
  state: '{"seen_ids":["i-1","i-2"],"last_seq":41}',
  state_updated_at: "2026-10-17T01:02:10Z",
  created_at: "2026-10-15T00:00:00Z",
  updated_at: "2026-10-15T00:00:00Z",
  last_run: {
    id: "r1",
    task_id: "t1",
    status: "completed",
    trigger_source: "cron",
    summary: "已汇总 3 条变化",
    silent: false,
    scheduled_for: "2026-10-17T01:00:00Z",
    started_at: "2026-10-17T01:00:02Z",
    finished_at: "2026-10-17T01:02:10Z",
    duration_ms: 128_000,
  },
};

const disabledTask: BotScheduleTask = {
  ...enabledTask,
  id: "t2",
  name: "周报",
  enabled: false,
  next_run_at: null,
  state: null,
  state_updated_at: null,
  last_run: null,
};

// 界面语言跟随浏览器，测试固定中文（同 automations 页测试的做法）。
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

// 与生产同一结构：展开态与详情选择由宿主持有，弹窗只挂一份。
function Harness() {
  const [expanded, setExpanded] = useState(true);
  const [taskId, setTaskId] = useState<string | null>(null);
  return (
    <>
      <BotScheduleSection
        botId="b1"
        expanded={expanded}
        onExpandedChange={setExpanded}
        onOpenDetail={setTaskId}
      />
      <BotScheduleTaskDialog
        botId="b1"
        taskId={taskId}
        onClose={() => setTaskId(null)}
      />
    </>
  );
}

const renderSection = () => renderWithRoute(<Harness />);

// 详情右栏版默认展开，无需先点标题。
const getSection = () => screen.getByTestId("bot-schedule-section");

beforeEach(() => {
  setLang("zh-CN");
  signInAs("A");
  vi.clearAllMocks();
  // mockReturnValue 的实现不会被 clearAllMocks 清掉，每例前重放默认值。
  vi.mocked(useBotScheduleTasks).mockReturnValue({
    data: [],
    isLoading: false,
  } as never);
  vi.mocked(useBotScheduleTaskRuns).mockReturnValue({
    data: [],
    isLoading: false,
  } as never);
});

afterEach(() => {
  teardownAuth();
  setLang("en-US");
});

describe("BotScheduleSection", () => {
  it("默认展开显示空状态，点击标题可折叠", async () => {
    const user = userEvent.setup();
    renderSection();

    const section = screen.getByTestId("bot-schedule-section");
    const header = within(section).getByRole("button", {
      name: /定时任务 · 0 个/,
    });
    expect(header).toHaveAttribute("aria-expanded", "true");
    expect(
      within(section).getByText("让机器人帮你创建"),
    ).toBeInTheDocument();

    await user.click(header);
    expect(header).toHaveAttribute("aria-expanded", "false");
    expect(
      within(section).queryByText("让机器人帮你创建"),
    ).not.toBeInTheDocument();
  });

  it("列出任务的名称、计划、下次运行、最近结果与开关", async () => {
    vi.mocked(useBotScheduleTasks).mockReturnValue({
      data: [enabledTask, disabledTask],
      isLoading: false,
    } as never);
    renderSection();
    const section = getSection();

    expect(
      within(section).getByRole("button", { name: /定时任务 · 2 个/ }),
    ).toBeInTheDocument();

    const rows = within(section).getAllByTestId("bot-schedule-task");
    expect(rows).toHaveLength(2);

    expect(rows[0]).toHaveTextContent("每日汇总");
    expect(rows[0]).toHaveTextContent("每天 09:00（Asia/Shanghai）");
    expect(rows[0]).toHaveTextContent(
      `下次运行 ${formatDate("2026-10-18T01:00:00Z")}`,
    );
    expect(rows[0]).toHaveTextContent("已完成 · 已汇总 3 条变化");
    expect(
      within(rows[0]).getByRole("switch", { name: "停用 每日汇总" }),
    ).toHaveAttribute("aria-checked", "true");

    expect(rows[1]).toHaveTextContent("周报");
    expect(rows[1]).toHaveTextContent("已停用");
    expect(rows[1]).toHaveTextContent("尚未运行");
    expect(within(rows[1]).getByRole("switch")).toHaveAttribute(
      "aria-checked",
      "false",
    );
  });

  it("点击行打开详情：指令全文与运行记录", async () => {
    vi.mocked(useBotScheduleTasks).mockReturnValue({
      data: [enabledTask],
      isLoading: false,
    } as never);
    vi.mocked(useBotScheduleTaskRuns).mockReturnValue({
      data: [
        {
          id: "r9",
          task_id: "t1",
          status: "failed",
          trigger_source: "manual",
          summary: "",
          silent: false,
          error_code: "model_error",
          error_message: "模型限流",
          scheduled_for: "2026-10-17T01:00:00Z",
          started_at: "2026-10-17T01:00:02Z",
          finished_at: "2026-10-17T01:02:10Z",
          duration_ms: 128_000,
        },
      ],
      isLoading: false,
    } as never);
    const user = userEvent.setup();
    renderSection();
    const section = getSection();

    await user.click(within(section).getByText("每日汇总"));

    expect(useBotScheduleTaskRuns).toHaveBeenLastCalledWith("b1", "t1");
    const dialog = await screen.findByRole("dialog");
    expect(dialog).toHaveTextContent("每天 09:00（Asia/Shanghai）");
    expect(dialog).toHaveTextContent("模型跟随机器人");
    expect(dialog).toHaveTextContent("运行后回报");
    expect(dialog).toHaveTextContent("汇总昨天的 Issue 变化并发到对话里");

    const run = within(dialog).getByTestId("bot-schedule-run");
    expect(run).toHaveTextContent("已失败");
    expect(run).toHaveTextContent("手动触发");
    expect(run).toHaveTextContent(formatDate("2026-10-17T01:00:02Z"));
    expect(run).toHaveTextContent("2 分 8 秒");
    expect(run).toHaveTextContent("模型限流");
  });

  it("开关只切换 enabled", async () => {
    setEnabledMutateAsync.mockResolvedValue({});
    vi.mocked(useBotScheduleTasks).mockReturnValue({
      data: [enabledTask],
      isLoading: false,
    } as never);
    const user = userEvent.setup();
    renderSection();
    const section = getSection();

    await user.click(
      within(section).getByRole("switch", { name: "停用 每日汇总" }),
    );
    await waitFor(() =>
      expect(setEnabledMutateAsync).toHaveBeenCalledWith({
        taskId: "t1",
        enabled: false,
      }),
    );
    await waitFor(() => expect(toast.success).toHaveBeenCalled());
  });

  it("开关失败给出可见反馈", async () => {
    setEnabledMutateAsync.mockRejectedValue(
      new ApiRequestError(500, null, "服务器错误"),
    );
    vi.mocked(useBotScheduleTasks).mockReturnValue({
      data: [enabledTask],
      isLoading: false,
    } as never);
    const user = userEvent.setup();
    renderSection();
    const section = getSection();

    await user.click(
      within(section).getByRole("switch", { name: "停用 每日汇总" }),
    );
    await waitFor(() =>
      expect(toast.error).toHaveBeenCalledWith("服务器错误"),
    );
  });

  it("详情弹窗展示美化 JSON 状态与更新时间", async () => {
    vi.mocked(useBotScheduleTasks).mockReturnValue({
      data: [enabledTask],
      isLoading: false,
    } as never);
    const user = userEvent.setup();
    renderSection();
    const section = getSection();

    await user.click(within(section).getByText("每日汇总"));
    const dialog = await screen.findByRole("dialog");

    expect(dialog).toHaveTextContent("任务状态");
    const state = within(dialog).getByTestId("bot-schedule-task-state");
    expect(state.textContent).toBe(
      JSON.stringify(JSON.parse(enabledTask.state!), null, 2),
    );
    expect(dialog).toHaveTextContent(
      `更新于 ${formatDate("2026-10-17T01:02:10Z")}`,
    );
    expect(
      within(dialog).getByRole("button", { name: "清空状态" }),
    ).toBeEnabled();
  });

  it("含大整数时退回原始 JSON，避免展示精度失真", async () => {
    const bigIntTask: BotScheduleTask = {
      ...enabledTask,
      id: "t3",
      name: "大整数状态",
      state: '{"n":12345678901234567890}',
    };
    vi.mocked(useBotScheduleTasks).mockReturnValue({
      data: [bigIntTask],
      isLoading: false,
    } as never);
    const user = userEvent.setup();
    renderSection();
    const section = getSection();

    await user.click(within(section).getByText("大整数状态"));
    const dialog = await screen.findByRole("dialog");

    const state = within(dialog).getByTestId("bot-schedule-task-state");
    expect(state.textContent).toBe('{"n":12345678901234567890}');
    expect(dialog).toHaveTextContent("含大整数等，按原始 JSON 显示");
  });

  it("无状态时展示空态且清空按钮不可用", async () => {
    vi.mocked(useBotScheduleTasks).mockReturnValue({
      data: [disabledTask],
      isLoading: false,
    } as never);
    const user = userEvent.setup();
    renderSection();
    const section = getSection();

    await user.click(within(section).getByText("周报"));
    const dialog = await screen.findByRole("dialog");

    expect(dialog).toHaveTextContent("暂无状态");
    expect(
      within(dialog).queryByTestId("bot-schedule-task-state"),
    ).not.toBeInTheDocument();
    expect(
      within(dialog).getByRole("button", { name: "清空状态" }),
    ).toBeDisabled();
  });

  it("清空状态调用接口并给出成功反馈", async () => {
    clearStateMutateAsync.mockResolvedValue({});
    vi.mocked(useBotScheduleTasks).mockReturnValue({
      data: [enabledTask],
      isLoading: false,
    } as never);
    const user = userEvent.setup();
    renderSection();
    const section = getSection();

    await user.click(within(section).getByText("每日汇总"));
    const dialog = await screen.findByRole("dialog");
    await user.click(
      within(dialog).getByRole("button", { name: "清空状态" }),
    );

    await waitFor(() =>
      expect(clearStateMutateAsync).toHaveBeenCalledWith("t1"),
    );
    await waitFor(() =>
      expect(toast.success).toHaveBeenCalledWith("状态已清空"),
    );
  });

  it("清空状态失败给出可见反馈", async () => {
    clearStateMutateAsync.mockRejectedValue(
      new ApiRequestError(500, null, "服务器错误"),
    );
    vi.mocked(useBotScheduleTasks).mockReturnValue({
      data: [enabledTask],
      isLoading: false,
    } as never);
    const user = userEvent.setup();
    renderSection();
    const section = getSection();

    await user.click(within(section).getByText("每日汇总"));
    const dialog = await screen.findByRole("dialog");
    await user.click(
      within(dialog).getByRole("button", { name: "清空状态" }),
    );

    await waitFor(() =>
      expect(toast.error).toHaveBeenCalledWith("服务器错误"),
    );
  });

  it("不提供新建、编辑或删除入口", async () => {
    vi.mocked(useBotScheduleTasks).mockReturnValue({
      data: [enabledTask],
      isLoading: false,
    } as never);
    const user = userEvent.setup();
    renderSection();
    const section = getSection();

    expect(
      within(section).queryByRole("button", {
        name: /新建|创建|编辑|删除/,
      }),
    ).not.toBeInTheDocument();

    await user.click(within(section).getByText("每日汇总"));
    const dialog = await screen.findByRole("dialog");
    expect(
      within(dialog).queryByRole("button", {
        name: /新建|创建|编辑|删除|保存/,
      }),
    ).not.toBeInTheDocument();
  });

  // ---- INT-137：运行中的定时任务可停止 ----

  const runningRun: BotScheduleRun = {
    id: "r-run",
    task_id: "t1",
    status: "running",
    trigger_source: "cron",
    summary: "",
    silent: false,
    scheduled_for: "2026-10-18T01:00:00Z",
    started_at: "2026-10-18T01:00:02Z",
    finished_at: null,
    duration_ms: null,
  };
  const completedRun: BotScheduleRun = {
    ...runningRun,
    id: "r-done",
    status: "completed",
    summary: "已汇总 3 条变化",
    finished_at: "2026-10-18T01:02:10Z",
    duration_ms: 128_000,
  };

  function mockRuns(runs: BotScheduleRun[]) {
    vi.mocked(useBotScheduleTasks).mockReturnValue({
      data: [enabledTask],
      isLoading: false,
    } as never);
    vi.mocked(useBotScheduleTaskRuns).mockReturnValue({
      data: runs,
      isLoading: false,
    } as never);
  }

  // 需要探到 invalidateQueries 的调用：用自己的 QueryClient 渲染弹窗。
  function renderDialog() {
    const client = new QueryClient({
      defaultOptions: { queries: { retry: false } },
    });
    const invalidate = vi.spyOn(client, "invalidateQueries");
    render(
      <QueryClientProvider client={client}>
        <MemoryRouter>
          <BotScheduleTaskDialog botId="b1" taskId="t1" onClose={() => {}} />
        </MemoryRouter>
      </QueryClientProvider>,
    );
    return { invalidate, client };
  }

  it("运行中的记录显示停止按钮，点击调用接口并刷新任务与运行记录", async () => {
    stopScheduleRun.mockResolvedValue({
      ...runningRun,
      status: "stopped",
    });
    mockRuns([runningRun, completedRun]);
    const { invalidate } = renderDialog();
    const user = userEvent.setup();

    const dialog = await screen.findByRole("dialog");
    const rows = within(dialog).getAllByTestId("bot-schedule-run");
    expect(rows).toHaveLength(2);
    const stopButton = within(rows[0]).getByRole("button", { name: "停止" });
    // 非 running 行没有停止按钮。
    expect(
      within(rows[1]).queryByRole("button", { name: "停止" }),
    ).not.toBeInTheDocument();

    await user.click(stopButton);
    await waitFor(() =>
      expect(stopScheduleRun).toHaveBeenCalledWith("b1", "t1", "r-run"),
    );
    await waitFor(() =>
      expect(invalidate).toHaveBeenCalledWith({
        queryKey: botKeys.scheduleTaskRuns("A", "b1", "t1"),
      }),
    );
    expect(invalidate).toHaveBeenCalledWith({
      queryKey: botKeys.scheduleTasks("A", "b1"),
    });
    await waitFor(() =>
      expect(toast.success).toHaveBeenCalledWith("已请求停止，等待运行收尾"),
    );
  });

  it("停止成功后把返回的 run 就地写进运行记录缓存", async () => {
    const stoppedRun: BotScheduleRun = {
      ...runningRun,
      status: "stopped",
      finished_at: "2026-10-18T01:01:00Z",
      duration_ms: 58_000,
    };
    stopScheduleRun.mockResolvedValue(stoppedRun);
    mockRuns([runningRun, completedRun]);
    const { client } = renderDialog();
    // 运行记录按 15s 轮询；就地写回让停止按钮立刻消失，不等下次取数。
    // 真实环境由 useBotScheduleTaskRuns 水合，这里 hook 被 mock，预置缓存。
    client.setQueryData<BotScheduleRun[]>(
      botKeys.scheduleTaskRuns("A", "b1", "t1"),
      [runningRun, completedRun],
    );
    const user = userEvent.setup();

    const dialog = await screen.findByRole("dialog");
    await user.click(
      within(dialog).getByRole("button", { name: "停止" }),
    );

    await waitFor(() =>
      expect(
        client
          .getQueryData<BotScheduleRun[]>(
            botKeys.scheduleTaskRuns("A", "b1", "t1"),
          )
          ?.map((r) => `${r.id}:${r.status}`),
      ).toEqual(["r-run:stopped", "r-done:completed"]),
    );
    // 终态合并保留原行的定时运行字段（summary/task_id 等不被主线投影覆盖）。
    const merged = client
      .getQueryData<BotScheduleRun[]>(botKeys.scheduleTaskRuns("A", "b1", "t1"))
      ?.find((r) => r.id === "r-run");
    expect(merged?.summary).toBe(runningRun.summary);
    expect(merged?.finished_at).toBe("2026-10-18T01:01:00Z");
  });

  it("停止受理（返回仍是 running）不整行覆盖运行记录缓存", async () => {
    // 停止接口返回的是主线 run 投影：受理瞬间 status 仍是 running，且缺
    // summary/task_id/scheduled_for 等字段——不能拿它替换定时运行行。
    stopScheduleRun.mockResolvedValue({ ...runningRun });
    mockRuns([runningRun, completedRun]);
    const { client } = renderDialog();
    client.setQueryData<BotScheduleRun[]>(
      botKeys.scheduleTaskRuns("A", "b1", "t1"),
      [runningRun, completedRun],
    );
    const user = userEvent.setup();

    const dialog = await screen.findByRole("dialog");
    await user.click(within(dialog).getByRole("button", { name: "停止" }));

    await waitFor(() => expect(stopScheduleRun).toHaveBeenCalled());
    const rows = client.getQueryData<BotScheduleRun[]>(
      botKeys.scheduleTaskRuns("A", "b1", "t1"),
    );
    expect(rows?.find((r) => r.id === "r-run")?.summary).toBe(
      runningRun.summary,
    );
    expect(rows?.find((r) => r.id === "r-run")?.status).toBe("running");
  });

  it("停止失败给出可见反馈且不刷新", async () => {
    stopScheduleRun.mockRejectedValue(
      new ApiRequestError(500, null, "服务器错误"),
    );
    mockRuns([runningRun]);
    const { invalidate } = renderDialog();
    const user = userEvent.setup();

    const dialog = await screen.findByRole("dialog");
    await user.click(
      within(dialog).getByRole("button", { name: "停止" }),
    );

    await waitFor(() =>
      expect(toast.error).toHaveBeenCalledWith("服务器错误"),
    );
    expect(invalidate).not.toHaveBeenCalled();
  });
});
