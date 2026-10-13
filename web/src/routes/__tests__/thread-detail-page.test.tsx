import { act, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { MemoryRouter, Route, Routes } from "react-router";
import { toast } from "sonner";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { ApiRequestError } from "@/lib/api/client";
import ThreadDetailPage from "@/routes/threads/$environmentId/$threadId";
import type { ThreadDetail, ThreadItem } from "@/lib/api/threads";
import { signInAs, teardownAuth } from "@/test/auth";
import { makeActivity, makeEnvironment, makeQueue } from "@/test/threads";

vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: vi.fn() } }));

const api = vi.hoisted(() => ({
  detail: vi.fn(),
  itemDetail: vi.fn(),
  envList: vi.fn(),
  send: vi.fn(),
  stop: vi.fn(),
  resumeQueue: vi.fn(),
  steer: vi.fn(),
  cancelQueued: vi.fn(),
  editQueued: vi.fn(),
  reorderQueued: vi.fn(),
  respond: vi.fn(),
  answerSecret: vi.fn(),
  attachmentContent: vi.fn(),
  uploadAttachment: vi.fn(),
  deleteAttachment: vi.fn(),
}));
const { detail, itemDetail, envList } = api;

vi.mock("@/lib/api/threads", async (importOriginal) => {
  const { envList: environmentList, ...threadMocks } = api;
  return {
    ...(await importOriginal<typeof import("@/lib/api/threads")>()),
    environmentApi: { list: environmentList },
    threadApi: { list: vi.fn(), ...threadMocks },
  };
});

function detailOf(
  items: ThreadItem[],
  status: ThreadDetail["thread"]["status"] = "idle",
  updatedAt = "2026-10-13T10:00:00Z",
  parentThreadId?: string,
  extra: Partial<Pick<ThreadDetail, "queue" | "activity" | "access">> = {},
) {
  return {
    data: {
      thread: {
        id: "t1",
        environment_id: "e1",
        environment_name: "Mac",
        project_id: "p1",
        project_name: "Alpha",
        title: "Fix login",
        status,
        branch: "main",
        provider: "codex",
        model: "gpt-5",
        updated_at: updatedAt,
        pull_request_count: 1,
        ...(parentThreadId ? { parent_thread_id: parentThreadId } : {}),
      },
      items,
      queue: makeQueue(),
      activity: makeActivity(),
      access: "operate",
      ...extra,
    },
  };
}

function mockDetail(items: ThreadItem[], status: ThreadDetail["thread"]["status"] = "idle") {
  detail.mockResolvedValue(detailOf(items, status));
}

function item(over: Partial<ThreadItem> & Pick<ThreadItem, "id" | "type">): ThreadItem {
  return { text: "", summary: "", ...over };
}

let client: QueryClient;

async function renderPage() {
  const view = render(
    <QueryClientProvider client={client}>
      <MemoryRouter initialEntries={["/threads/e1/t1"]}>
        <Routes>
          <Route path="/threads/:environmentId/:threadId" element={<ThreadDetailPage />} />
        </Routes>
      </MemoryRouter>
    </QueryClientProvider>,
  );
  await screen.findByText("Mac · Alpha");
  return view;
}

describe("ThreadDetailPage", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    signInAs("u1");
    envList.mockResolvedValue({ data: { items: [makeEnvironment()] } });
  });
  afterEach(teardownAuth);

  it("requests the thread from the route params", async () => {
    mockDetail([]);
    await renderPage();
    expect(detail).toHaveBeenCalledWith("e1", "t1", expect.any(AbortSignal));
    expect(screen.getByText("这个线程还没有内容")).toBeInTheDocument();
  });

  it("renders full markdown for messages and never shows images", async () => {
    const longTail = "END-OF-LONG-MESSAGE";
    mockDetail([
      item({ id: "1", type: "user_message", text: "## 需求\n\n- 一\n- 二\n\n![图](https://x/y.png)" }),
      item({
        id: "2",
        type: "assistant_message",
        text: `${"很长的内容 ".repeat(3000)}\n\n\`\`\`ts\nconst a = 1;\n\`\`\`\n\n${longTail}`,
      }),
    ]);
    const { container } = await renderPage();

    const user = screen.getByTestId("thread-item-user_message");
    expect(within(user).getByRole("heading", { name: "需求" })).toBeInTheDocument();
    expect(user.querySelectorAll("li")).toHaveLength(2);
    expect(container.querySelector("img")).toBeNull();
    expect(screen.getByTestId("thread-item-assistant_message").textContent).toContain(longTail);
    expect(container.querySelector("pre code")).not.toBeNull();
  });

  it("collapses reasoning, commands, tools and file changes until expanded", async () => {
    const user = userEvent.setup();
    mockDetail([
      item({ id: "r", type: "reasoning", summary: "想一想", text: "SECRET-REASONING" }),
      item({ id: "c", type: "command_execution", summary: "pnpm test", text: "OUT-LINE", status: "completed" }),
      item({ id: "f", type: "file_change", summary: "修改 3 个文件", text: "a.ts\nb.ts" }),
      item({ id: "d", type: "dynamic_tool", summary: "search", text: "TOOL-OUT" }),
    ]);
    await renderPage();

    for (const text of ["SECRET-REASONING", "OUT-LINE", "a.ts", "TOOL-OUT"]) {
      expect(screen.queryByText(text, { exact: false })).toBeNull();
    }
    expect(screen.getByText("想一想")).toBeInTheDocument();
    expect(screen.getByText("pnpm test")).toBeInTheDocument();

    await user.click(screen.getByText("pnpm test"));
    expect(screen.getByText("OUT-LINE")).toBeInTheDocument();
    await user.click(screen.getByText("想一想"));
    expect(screen.getByText("SECRET-REASONING")).toBeInTheDocument();
    await user.click(screen.getByText("修改 3 个文件"));
    expect(screen.getByText(/a\.ts/)).toBeInTheDocument();
    expect(itemDetail).not.toHaveBeenCalled();
  });

  it("links subagents to their child thread", async () => {
    const user = userEvent.setup();
    mockDetail([item({ id: "s", type: "subagent", summary: "Explorer", text: "找到了入口", child_thread_id: "child 1" })]);
    await renderPage();

    expect(screen.queryByRole("link", { name: "查看子线程" })).toBeNull();
    await user.click(screen.getByText("Explorer"));
    expect(screen.getByRole("link", { name: "查看子线程" })).toHaveAttribute(
      "href",
      "/threads/e1/child%201",
    );
  });

  it("is read only for read-only environments: no input, hint, no write actions", async () => {
    envList.mockImplementation(() => new Promise(() => undefined));
    detail.mockResolvedValue(
      detailOf(
        [item({ id: "1", type: "assistant_message", text: "hi" })],
        "running",
        undefined,
        undefined,
        {
          access: "read-only",
          queue: makeQueue({
            items: [{ run_id: "r1", message_id: "m1", text: "排队中" }],
          }),
        },
      ),
    );
    await renderPage();
    expect(await screen.findByText("该环境为只读会话，重新配对后可操作")).toBeInTheDocument();
    expect(screen.queryByRole("textbox")).toBeNull();
    expect(screen.queryByRole("form")).toBeNull();
    expect(screen.queryByRole("button", { name: "停止" })).toBeNull();
    expect(screen.getByText("线程运行中，内容会自动更新。")).toBeInTheDocument();
    expect(screen.getByText("排队中")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "编辑" })).toBeNull();
  });

  it("shows the composer from detail access without waiting for the environment list", async () => {
    envList.mockImplementation(() => new Promise(() => undefined));
    mockDetail([]);
    await renderPage();
    expect(screen.getByRole("form", { name: "消息输入" })).toBeInTheDocument();
    expect(screen.queryByText("该环境为只读会话，重新配对后可操作")).toBeNull();
  });

  describe("chat", () => {
    const composer = () => screen.getByRole("form", { name: "消息输入" });

    it("sends a reply when idle with stable ids reused across retries", async () => {
      const user = userEvent.setup();
      mockDetail([]);
      api.send.mockRejectedValueOnce(new ApiRequestError(502, 50202, "T3 环境调用失败"));
      api.send.mockResolvedValue({ sequence: 1 });
      await renderPage();

      await user.type(await screen.findByRole("textbox", { name: "消息内容" }), "继续");
      await user.click(within(composer()).getByRole("button", { name: "发送" }));
      expect(await screen.findByText("T3 环境调用失败")).toBeInTheDocument();
      await user.click(within(composer()).getByRole("button", { name: "发送" }));

      await waitFor(() => expect(api.send).toHaveBeenCalledTimes(2));
      const [, , first] = api.send.mock.calls[0];
      const [env, thread, second] = api.send.mock.calls[1];
      expect([env, thread]).toEqual(["e1", "t1"]);
      expect(second).toMatchObject({ text: "继续", attachments: [] });
      expect(second.command_id).toBe(first.command_id);
      expect(second.message_id).toBe(first.message_id);
      await waitFor(() => expect(screen.getByRole("textbox", { name: "消息内容" })).toHaveValue(""));
      // 写入后立刻重读详情
      expect(detail.mock.calls.length).toBeGreaterThan(1);
    });

    it("queues instead of steering while a run is active", async () => {
      const user = userEvent.setup();
      mockDetail([], "running");
      api.send.mockResolvedValue({ sequence: 1 });
      await renderPage();

      await user.type(await screen.findByRole("textbox", { name: "消息内容" }), "接着做");
      await user.click(within(composer()).getByRole("button", { name: "加入队列" }));
      await waitFor(() => expect(api.send).toHaveBeenCalledTimes(1));
      expect(screen.queryByRole("button", { name: "Steer" })).toBeNull();
    });

    it("keeps Stop pending until the refreshed detail confirms it", async () => {
      const user = userEvent.setup();
      mockDetail([], "running");
      let finish!: () => void;
      api.stop.mockReturnValue(new Promise((r) => (finish = () => r({ stopped: true }))));
      await renderPage();

      await user.click(await screen.findByRole("button", { name: "停止" }));
      expect(screen.getByRole("button", { name: "停止中…" })).toBeDisabled();

      // RPC 返回后详情仍显示 running：按钮不能凭空变成 idle。
      detail.mockResolvedValue(
        detailOf([], "running", undefined, undefined, {
          activity: makeActivity({ stopping: true, active_run_id: "r1" }),
        }),
      );
      await act(async () => finish());
      expect(await screen.findByRole("button", { name: "停止中…" })).toBeDisabled();
      expect(screen.getByText("运行中")).toBeInTheDocument();
    });

    const queued = (over = {}) =>
      makeQueue({
        items: [
          { run_id: "r1", message_id: "m1", text: "第一条" },
          { run_id: "r2", message_id: "m2", text: "第二条" },
        ],
        ...over,
      });

    it("shows the queue with Steer only when allowed, plus cancel and reorder", async () => {
      const user = userEvent.setup();
      detail.mockResolvedValue(
        detailOf([], "running", undefined, undefined, {
          queue: queued({ can_steer: true, can_reorder: true }),
          activity: makeActivity({ active_run_id: "r0" }),
        }),
      );
      api.steer.mockResolvedValue({ sequence: 1 });
      api.cancelQueued.mockResolvedValue({ sequence: 1 });
      api.reorderQueued.mockResolvedValue({ sequence: 1 });
      await renderPage();

      const rows = await screen.findAllByTestId("queue-item");
      expect(rows).toHaveLength(2);
      await user.click(within(rows[0]).getByRole("button", { name: "Steer" }));
      expect(api.steer).toHaveBeenCalledWith("e1", "t1", "r1");
      await user.click(within(rows[1]).getByRole("button", { name: "取消" }));
      expect(api.cancelQueued).toHaveBeenCalledWith("e1", "t1", "r2");
      await user.click(within(rows[1]).getByRole("button", { name: "上移" }));
      expect(api.reorderQueued).toHaveBeenCalledWith("e1", "t1", "r2", "r1");
      await user.click(within(rows[0]).getByRole("button", { name: "下移" }));
      expect(api.reorderQueued).toHaveBeenCalledWith("e1", "t1", "r1", null);
    });

    it("hides Steer and reorder when the provider cannot", async () => {
      detail.mockResolvedValue(
        detailOf([], "running", undefined, undefined, {
          queue: queued(),
          activity: makeActivity({ active_run_id: "r0" }),
        }),
      );
      await renderPage();
      await screen.findAllByTestId("queue-item");
      expect(screen.queryByRole("button", { name: "Steer" })).toBeNull();
      expect(screen.queryByRole("button", { name: "上移" })).toBeNull();
      expect(screen.getAllByRole("button", { name: "编辑" })).toHaveLength(2);
    });

    it("edits a queued message in the composer; unchanged attachments are omitted", async () => {
      const user = userEvent.setup();
      detail.mockResolvedValue(
        detailOf([], "running", undefined, undefined, {
          queue: queued(),
          activity: makeActivity({ active_run_id: "r0" }),
        }),
      );
      api.editQueued.mockResolvedValue({ sequence: 1 });
      await renderPage();

      const box = await screen.findByRole("textbox", { name: "消息内容" });
      await user.type(box, "草稿");
      await user.click(within(screen.getAllByTestId("queue-item")[0]).getByRole("button", { name: "编辑" }));
      expect(screen.getByText(/正在编辑排队消息/)).toBeInTheDocument();
      expect(box).toHaveValue("第一条");

      await user.clear(box);
      await user.type(box, "改过的");
      await user.click(screen.getByRole("button", { name: "保存修改" }));
      await waitFor(() => expect(api.editQueued).toHaveBeenCalledTimes(1));
      const [, , runId, body] = api.editQueued.mock.calls[0];
      expect(runId).toBe("r1");
      expect(body).toEqual({ text: "改过的", attachments: undefined });
      await waitFor(() => expect(box).toHaveValue("草稿"));
      expect(screen.queryByText(/正在编辑排队消息/)).toBeNull();
    });

    it("exits edit mode without saving and restores the draft", async () => {
      const user = userEvent.setup();
      detail.mockResolvedValue(
        detailOf([], "running", undefined, undefined, {
          queue: queued(),
          activity: makeActivity({ active_run_id: "r0" }),
        }),
      );
      await renderPage();
      const box = await screen.findByRole("textbox", { name: "消息内容" });
      await user.type(box, "草稿");
      await user.click(within(screen.getAllByTestId("queue-item")[1]).getByRole("button", { name: "编辑" }));
      expect(box).toHaveValue("第二条");
      await user.click(screen.getByRole("button", { name: "退出编辑" }));
      expect(box).toHaveValue("草稿");
      expect(api.editQueued).not.toHaveBeenCalled();
    });

    it("keeps unsent draft images when entering and leaving queue edit", async () => {
      const user = userEvent.setup();
      detail.mockResolvedValue(
        detailOf([], "running", undefined, undefined, {
          queue: queued(),
          activity: makeActivity({ active_run_id: "r0" }),
        }),
      );
      api.uploadAttachment.mockResolvedValue({
        type: "image",
        id: "att-draft",
        name: "draft.png",
        mime_type: "image/png",
        size_bytes: 4,
      });
      api.deleteAttachment.mockResolvedValue(null);
      await renderPage();
      const file = new File([new Uint8Array([1, 2, 3, 4])], "draft.png", { type: "image/png" });
      await user.upload(screen.getByTestId("image-input"), file);
      expect(await screen.findByLabelText("移除图片 draft.png")).toBeInTheDocument();
      await user.click(within(screen.getAllByTestId("queue-item")[0]).getByRole("button", { name: "编辑" }));
      expect(screen.queryByLabelText("移除图片 draft.png")).toBeNull();
      expect(api.deleteAttachment).not.toHaveBeenCalled();
      await user.click(screen.getByRole("button", { name: "退出编辑" }));
      expect(screen.getByLabelText("移除图片 draft.png")).toBeInTheDocument();
      expect(api.deleteAttachment).not.toHaveBeenCalled();
    });

    it("keeps a draft image that finishes uploading while queue-editing", async () => {
      const user = userEvent.setup();
      let finish!: (attachment: {
        type: "image";
        id: string;
        name: string;
        mime_type: string;
        size_bytes: number;
      }) => void;
      detail.mockResolvedValue(
        detailOf([], "running", undefined, undefined, {
          queue: queued(),
          activity: makeActivity({ active_run_id: "r0" }),
        }),
      );
      api.uploadAttachment.mockReturnValue(
        new Promise((resolve) => {
          finish = resolve;
        }),
      );
      api.deleteAttachment.mockResolvedValue(null);
      await renderPage();
      const file = new File([new Uint8Array([1, 2, 3, 4])], "draft.png", { type: "image/png" });
      await user.upload(screen.getByTestId("image-input"), file);
      expect(await screen.findByLabelText("上传中")).toBeInTheDocument();
      await user.click(within(screen.getAllByTestId("queue-item")[0]).getByRole("button", { name: "编辑" }));
      await act(async () =>
        finish({
          type: "image",
          id: "att-draft",
          name: "draft.png",
          mime_type: "image/png",
          size_bytes: 4,
        }),
      );
      expect(api.deleteAttachment).not.toHaveBeenCalled();
      await user.click(screen.getByRole("button", { name: "退出编辑" }));
      expect(await screen.findByLabelText("移除图片 draft.png")).toBeInTheDocument();
      expect(screen.queryByLabelText("上传中")).toBeNull();
      expect(api.deleteAttachment).not.toHaveBeenCalled();
    });

    it("deletes images added during queue edit when exiting without saving", async () => {
      const user = userEvent.setup();
      detail.mockResolvedValue(
        detailOf([], "running", undefined, undefined, {
          queue: queued(),
          activity: makeActivity({ active_run_id: "r0" }),
        }),
      );
      api.uploadAttachment
        .mockResolvedValueOnce({
          type: "image",
          id: "att-draft",
          name: "draft.png",
          mime_type: "image/png",
          size_bytes: 4,
        })
        .mockResolvedValueOnce({
          type: "image",
          id: "att-edit",
          name: "edit.png",
          mime_type: "image/png",
          size_bytes: 4,
        });
      api.deleteAttachment.mockResolvedValue(null);
      await renderPage();
      await user.upload(
        screen.getByTestId("image-input"),
        new File([new Uint8Array([1, 2, 3, 4])], "draft.png", { type: "image/png" }),
      );
      expect(await screen.findByLabelText("移除图片 draft.png")).toBeInTheDocument();
      await waitFor(() => expect(screen.queryByLabelText("上传中")).toBeNull());
      await user.click(within(screen.getAllByTestId("queue-item")[0]).getByRole("button", { name: "编辑" }));
      await user.upload(
        screen.getByTestId("image-input"),
        new File([new Uint8Array([5, 6, 7, 8])], "edit.png", { type: "image/png" }),
      );
      expect(await screen.findByLabelText("移除图片 edit.png")).toBeInTheDocument();
      await waitFor(() => expect(screen.queryByLabelText("上传中")).toBeNull());
      await user.click(screen.getByRole("button", { name: "退出编辑" }));
      await waitFor(() => expect(api.deleteAttachment).toHaveBeenCalledWith("e1", "att-edit"));
      expect(screen.getByLabelText("移除图片 draft.png")).toBeInTheDocument();
      expect(screen.queryByLabelText("移除图片 edit.png")).toBeNull();
      expect(api.deleteAttachment).not.toHaveBeenCalledWith("e1", "att-draft");
    });

    it("shows a held queue and resumes it", async () => {
      const user = userEvent.setup();
      detail.mockResolvedValue(
        detailOf([], "idle", undefined, undefined, { queue: queued({ held: true }) }),
      );
      api.resumeQueue.mockResolvedValue({ sequence: 1 });
      await renderPage();
      expect(await screen.findByText("队列已暂停")).toBeInTheDocument();
      await user.click(screen.getByRole("button", { name: "恢复队列" }));
      expect(api.resumeQueue).toHaveBeenCalledWith("e1", "t1");
    });

    it("disables queue and stop buttons when the snapshot is stale", async () => {
      detail.mockResolvedValue(
        detailOf([], "running", undefined, undefined, {
          queue: queued({ can_steer: true }),
          activity: makeActivity({ active_run_id: "r0" }),
        }),
      );
      await renderPage();
      await screen.findAllByTestId("queue-item");

      detail.mockRejectedValue(new Error("down"));
      await act(() => client.refetchQueries({ predicate: (q) => q.queryKey[2] === "thread" }));
      await screen.findByText(/刷新失败/);
      for (const name of ["Steer", "编辑", "取消", "停止"]) {
        for (const button of screen.getAllByRole("button", { name })) expect(button).toBeDisabled();
      }
    });
  });

  describe("request cards", () => {
    const approval = (over: Partial<ThreadItem> = {}) =>
      item({
        id: "src::a1",
        type: "approval_request",
        text: "",
        summary: "运行 rm",
        prompt: "允许运行 rm -rf？",
        request_id: "req-1",
        request_status: "pending",
        response_capability: "live",
        source_thread_id: "t1",
        ...over,
      });

    it("falls back to the default four approval options and answers in place", async () => {
      const user = userEvent.setup();
      mockDetail([approval()], "waiting");
      api.respond.mockResolvedValue({ sequence: 1 });
      await renderPage();

      const card = screen.getByTestId("thread-item-approval_request");
      expect(within(card).getAllByRole("button").map((b) => b.textContent)).toEqual([
        "Approve",
        "Decline",
        "Always allow this session",
        "Cancel",
      ]);
      await user.click(within(card).getByRole("button", { name: "Always allow this session" }));
      expect(api.respond).toHaveBeenCalledWith("e1", "t1", "req-1", {
        type: "approval",
        decision: "acceptForSession",
      });
    });

    it("uses the options carried by the request, including warnings", async () => {
      mockDetail(
        [
          approval({
            options: [
              { decision: "accept", label: "Allow once", warning: "可能存在提示注入" },
              { decision: "decline", label: "Deny" },
            ],
          }),
        ],
        "waiting",
      );
      await renderPage();
      const card = screen.getByTestId("thread-item-approval_request");
      expect(within(card).getAllByRole("button").map((b) => b.textContent)).toEqual(["Allow once", "Deny"]);
      expect(within(card).queryByRole("button", { name: "Approve" })).toBeNull();
      expect(card).toHaveTextContent("可能存在提示注入");
    });

    it("shows inherited requests read-only with a link to the source thread", async () => {
      mockDetail(
        [approval({ inherited: true, source_thread_id: "parent 1" })],
        "idle",
      );
      await renderPage();
      const card = screen.getByTestId("thread-item-approval_request");
      expect(within(card).queryByRole("button")).toBeNull();
      expect(within(card).getByRole("link", { name: "查看来源线程" })).toHaveAttribute(
        "href",
        "/threads/e1/parent%201",
      );
    });

    it("does not offer answers for resolved or non-resumable requests", async () => {
      mockDetail(
        [
          approval({ id: "a", request_status: "resolved", decision: "accept" }),
          approval({ id: "b", response_capability: "not_resumable" }),
        ],
        "idle",
      );
      await renderPage();
      expect(screen.queryByRole("button", { name: "Approve" })).toBeNull();
    });

    it("answers a user input request with the chosen option", async () => {
      const user = userEvent.setup();
      mockDetail(
        [
          item({
            id: "src::q1",
            type: "user_input_request",
            summary: "选方案",
            request_id: "req-2",
            request_status: "pending",
            response_capability: "live",
            questions: [
              {
                id: "q1",
                header: "方案",
                question: "用哪个方案？",
                options: [
                  { label: "A", description: "快" },
                  { label: "B", description: "稳", value: "b-value" },
                ],
              },
            ],
          }),
        ],
        "waiting",
      );
      api.respond.mockResolvedValue({ sequence: 1 });
      await renderPage();
      const form = screen.getByRole("form", { name: "回答提问" });
      expect(within(form).getByRole("button", { name: "提交回答" })).toBeDisabled();
      await user.click(within(form).getByLabelText(/B/));
      await user.click(within(form).getByRole("button", { name: "提交回答" }));
      expect(api.respond).toHaveBeenCalledWith("e1", "t1", "req-2", {
        type: "user_input",
        answers: { q1: "b-value" },
      });
    });

    describe("secret requests", () => {
      const secretItem = (over: Partial<ThreadItem> = {}) =>
        item({
          id: "src::s1",
          type: "secret_request",
          summary: "API key",
          label: "OpenAI key",
          reason: "用于调用模型",
          secret_status: "pending",
          source_thread_id: "t1",
          ...over,
        });

      it("masks the field, sends the secret once, clears it, and never leaks it", async () => {
        const user = userEvent.setup();
        const spies = (["log", "info", "warn", "error", "debug"] as const).map((m) =>
          vi.spyOn(console, m).mockImplementation(() => {}),
        );
        mockDetail([secretItem()], "waiting");
        api.answerSecret.mockResolvedValue(null);
        const { container } = await renderPage();

        expect(screen.getByText(/不会保存在 Fast Ship/)).toBeInTheDocument();
        const field = screen.getByLabelText("OpenAI key");
        expect(field.className).toContain("text-security");
        await user.type(field, "sk-TOP-SECRET");
        await user.click(screen.getByRole("button", { name: "安全保存" }));

        await waitFor(() =>
          expect(api.answerSecret).toHaveBeenCalledWith("e1", "t1", "src::s1", {
            type: "save",
            secret: "sk-TOP-SECRET",
          }),
        );
        await waitFor(() => expect(field).toHaveValue(""));
        expect(toast.success).not.toHaveBeenCalled();
        expect(toast.error).not.toHaveBeenCalled();
        for (const spy of spies) expect(JSON.stringify(spy.mock.calls)).not.toContain("sk-TOP-SECRET");
        expect(container.textContent).not.toContain("sk-TOP-SECRET");
        spies.forEach((s) => s.mockRestore());
      });

      it("shows a fixed error and keeps the value out of the page on failure", async () => {
        const user = userEvent.setup();
        mockDetail([secretItem()], "waiting");
        api.answerSecret.mockRejectedValue(new ApiRequestError(409, 40916, "echo sk-LEAK"));
        const { container } = await renderPage();

        await user.type(screen.getByLabelText("OpenAI key"), "sk-LEAK");
        await user.click(screen.getByRole("button", { name: "安全保存" }));
        expect(await screen.findByRole("alert")).toHaveTextContent("该请求已被回答或代理已停止");
        expect(container.textContent).not.toContain("echo sk-LEAK");
      });

      it("guards against double submit and supports declining", async () => {
        const user = userEvent.setup();
        mockDetail([secretItem()], "waiting");
        let done!: () => void;
        api.answerSecret.mockReturnValue(new Promise((r) => (done = () => r(null))));
        await renderPage();

        await user.type(screen.getByLabelText("OpenAI key"), "abc");
        const save = screen.getByRole("button", { name: "安全保存" });
        await user.click(save);
        await user.click(save);
        expect(api.answerSecret).toHaveBeenCalledTimes(1);
        await act(async () => done());

        api.answerSecret.mockResolvedValue(null);
        await user.click(screen.getByRole("button", { name: "拒绝" }));
        expect(api.answerSecret).toHaveBeenLastCalledWith("e1", "t1", "src::s1", { type: "decline" });
      });

      it("renders answered and inherited secret requests without a form", async () => {
        mockDetail(
          [
            secretItem({ id: "a", secret_status: "saved" }),
            secretItem({ id: "b", inherited: true, source_thread_id: "p" }),
          ],
          "idle",
        );
        await renderPage();
        expect(screen.queryByRole("textbox")).toBeInTheDocument();
        expect(screen.queryByLabelText("OpenAI key")).toBeNull();
        expect(screen.getByText(/已保存/)).toBeInTheDocument();
        expect(screen.getByRole("link", { name: "查看来源线程" })).toHaveAttribute("href", "/threads/e1/p");
      });
    });
  });

  it("shows errors inline", async () => {
    mockDetail([item({ id: "e", type: "error", text: "boom" })]);
    await renderPage();
    expect(screen.getByRole("alert")).toHaveTextContent("boom");
  });

  describe("lazy item detail", () => {
    const cmd = item({
      id: "src:c1",
      type: "command_execution",
      summary: "pnpm test",
      status: "completed",
      detail_available: true,
    });

    it("loads the full command input and output only when expanded", async () => {
      const user = userEvent.setup();
      mockDetail([cmd]);
      itemDetail.mockResolvedValue({
        data: { ...cmd, detail_available: false, text: "$ pnpm test\nFULL-OUTPUT-TAIL" },
      });
      await renderPage();
      expect(itemDetail).not.toHaveBeenCalled();

      await user.click(screen.getByText("pnpm test"));
      expect(await screen.findByText(/FULL-OUTPUT-TAIL/)).toBeInTheDocument();
      expect(itemDetail).toHaveBeenCalledWith("e1", "t1", "src:c1", expect.any(AbortSignal));
    });

    it("shows a loading state, then an error with retry", async () => {
      const user = userEvent.setup();
      mockDetail([cmd]);
      let reject!: (e: Error) => void;
      itemDetail.mockReturnValueOnce(new Promise((_, r) => (reject = r)));
      await renderPage();

      await user.click(screen.getByText("pnpm test"));
      expect(screen.getByRole("status")).toHaveTextContent("加载中");

      await act(async () => reject(new Error("boom")));
      expect(await screen.findByRole("alert")).toHaveTextContent("详情加载失败");

      itemDetail.mockResolvedValueOnce({ data: { ...cmd, text: "RECOVERED" } });
      await user.click(screen.getByRole("button", { name: "重试" }));
      expect(await screen.findByText("RECOVERED")).toBeInTheDocument();
      expect(screen.queryByRole("alert")).toBeNull();
    });

    it("keeps an expanded item's body without re-reading when only the thread updated_at changes", async () => {
      const user = userEvent.setup();
      const running = { ...cmd, status: "running", updated_at: "2026-10-13T09:00:00Z" };
      detail.mockResolvedValue(detailOf([running], "running", "2026-10-13T10:00:00Z"));
      itemDetail.mockResolvedValue({ data: { ...running, text: "OUT-v1" } });
      await renderPage();

      await user.click(screen.getByText("pnpm test"));
      expect(await screen.findByText("OUT-v1")).toBeInTheDocument();

      detail.mockResolvedValue(detailOf([running], "running", "2026-10-13T10:00:05Z"));
      await act(() => client.refetchQueries({ predicate: (q) => q.queryKey[2] === "thread" }));
      expect(detail).toHaveBeenCalledTimes(2);
      expect(screen.getByText("OUT-v1")).toBeInTheDocument();
      expect(itemDetail).toHaveBeenCalledTimes(1);
    });

    it("re-reads an expanded item when its own updated_at changes", async () => {
      const user = userEvent.setup();
      const running = { ...cmd, status: "running", updated_at: "2026-10-13T09:00:00Z" };
      detail.mockResolvedValue(detailOf([running], "running", "2026-10-13T10:00:00Z"));
      itemDetail.mockResolvedValueOnce({ data: { ...running, text: "OUT-v1" } });
      await renderPage();

      await user.click(screen.getByText("pnpm test"));
      expect(await screen.findByText("OUT-v1")).toBeInTheDocument();

      const updated = { ...running, updated_at: "2026-10-13T10:00:05Z" };
      detail.mockResolvedValue(detailOf([updated], "running", "2026-10-13T10:00:05Z"));
      itemDetail.mockResolvedValueOnce({ data: { ...updated, text: "OUT-v2" } });
      await act(() => client.refetchQueries({ predicate: (q) => q.queryKey[2] === "thread" }));
      expect(await screen.findByText("OUT-v2")).toBeInTheDocument();
      expect(itemDetail).toHaveBeenCalledTimes(2);
    });

    it("falls back to the thread updated_at when the item has no updated_at", async () => {
      const user = userEvent.setup();
      const running = { ...cmd, status: "running" };
      detail.mockResolvedValue(detailOf([running], "running", "2026-10-13T10:00:00Z"));
      itemDetail.mockResolvedValueOnce({ data: { ...running, text: "OUT-v1" } });
      await renderPage();

      await user.click(screen.getByText("pnpm test"));
      expect(await screen.findByText("OUT-v1")).toBeInTheDocument();

      detail.mockResolvedValue(detailOf([running], "running", "2026-10-13T10:00:05Z"));
      itemDetail.mockResolvedValueOnce({ data: { ...running, text: "OUT-v2" } });
      await act(() => client.refetchQueries({ predicate: (q) => q.queryKey[2] === "thread" }));
      expect(await screen.findByText("OUT-v2")).toBeInTheDocument();
      expect(itemDetail).toHaveBeenCalledTimes(2);
    });
  });

  it("keeps the stale thread and warns when a refresh fails", async () => {
    const user = userEvent.setup();
    mockDetail([item({ id: "1", type: "assistant_message", text: "still here" })]);
    await renderPage();

    detail.mockRejectedValue(new Error("down"));
    await act(() => client.refetchQueries());
    expect(await screen.findByRole("alert")).toHaveTextContent("刷新失败");
    expect(screen.getByText("still here")).toBeInTheDocument();

    mockDetail([item({ id: "1", type: "assistant_message", text: "fresh" })]);
    await user.click(screen.getByRole("button", { name: "重试" }));
    expect(await screen.findByText("fresh")).toBeInTheDocument();
    expect(screen.queryByRole("alert")).toBeNull();
  });

  it("explains a missing thread when the detail request 404s", async () => {
    detail.mockRejectedValue(new ApiRequestError(404, 40418, "T3 线程不存在"));

    render(
      <QueryClientProvider client={client}>
        <MemoryRouter initialEntries={["/threads/e1/t1"]}>
          <Routes>
            <Route path="/threads/:environmentId/:threadId" element={<ThreadDetailPage />} />
          </Routes>
        </MemoryRouter>
      </QueryClientProvider>,
    );

    expect(await screen.findByText("线程不存在，可能已被删除或移动。")).toBeInTheDocument();
    expect(screen.queryByRole("link", { name: "去重新配对" })).toBeNull();
  });

  it("surfaces the server message and a re-pair link for rejected credentials", async () => {
    detail.mockRejectedValue(
      new ApiRequestError(502, 50202, "T3 环境拒绝凭证（可能已过期或被吊销），请重新配对"),
    );

    render(
      <QueryClientProvider client={client}>
        <MemoryRouter initialEntries={["/threads/e1/t1"]}>
          <Routes>
            <Route path="/threads/:environmentId/:threadId" element={<ThreadDetailPage />} />
          </Routes>
        </MemoryRouter>
      </QueryClientProvider>,
    );

    expect(await screen.findByText(/请重新配对/)).toBeInTheDocument();
    const link = await screen.findByRole("link", { name: "去重新配对" });
    expect(link).toHaveAttribute("href", "/settings/environments");
  });

  it("shows the server message and re-pair link when a refresh fails", async () => {
    mockDetail(
      [item({ id: "1", type: "assistant_message", text: "still here" })],
      "running",
    );
    await renderPage();

    detail.mockRejectedValue(
      new ApiRequestError(502, 50202, "T3 环境拒绝凭证（可能已过期或被吊销），请重新配对"),
    );
    await act(() => client.refetchQueries());

    const banner = await screen.findByRole("alert");
    expect(banner).toHaveTextContent("请重新配对");
    expect(banner).toHaveTextContent("可能不是最新内容");
    expect(
      within(banner).getByRole("link", { name: "去重新配对" }),
    ).toHaveAttribute("href", "/settings/environments");
    expect(screen.getByText("still here")).toBeInTheDocument();
    expect(screen.queryByText("线程运行中，内容会自动更新。")).not.toBeInTheDocument();
  });

  it("links a subagent thread back to its parent", async () => {
    detail.mockResolvedValue(detailOf([], "idle", undefined, "parent-9"));
    await renderPage();

    expect(screen.getByRole("link", { name: /返回父线程/ })).toHaveAttribute(
      "href",
      "/threads/e1/parent-9",
    );
    expect(screen.getByRole("link", { name: /全部线程/ })).toHaveAttribute(
      "href",
      "/threads",
    );
  });
});
