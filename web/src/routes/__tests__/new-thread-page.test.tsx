import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { MemoryRouter, Route, Routes, useParams } from "react-router";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import NewThreadPage from "@/routes/threads/new";
import type {
  ComposerOptions,
  Environment,
  ThreadSummary,
  WorkspaceOptions,
} from "@/lib/api/threads";
import { signInAs, teardownAuth } from "@/test/auth";
import { makeEnvironment } from "@/test/threads";
import { ApiRequestError } from "@/lib/api/client";

const api = vi.hoisted(() => ({
  envList: vi.fn(),
  list: vi.fn(),
  composerOptions: vi.fn(),
  workspaceOptions: vi.fn(),
  create: vi.fn(),
  uploadAttachment: vi.fn(),
  deleteAttachment: vi.fn(),
}));

vi.mock("@/lib/api/threads", async (importOriginal) => {
  const { envList, ...threadMocks } = api;
  return {
    ...(await importOriginal<typeof import("@/lib/api/threads")>()),
    environmentApi: { list: envList },
    threadApi: threadMocks,
  };
});

const composerOptions: ComposerOptions = {
  projects: [
    {
      id: "p1",
      title: "Alpha",
      workspace_root: "/w/alpha",
      default_model_selection: { instance_id: "codex", model: "gpt-5" },
    },
    { id: "p2", title: "Beta", workspace_root: "/w/beta" },
  ],
  config: {
    providers: [
      {
        instanceId: "codex",
        displayName: "Codex",
        enabled: true,
        installed: true,
        models: [
          {
            slug: "gpt-5",
            name: "GPT-5",
            isDefault: true,
            capabilities: {
              optionDescriptors: [
                {
                  id: "effort",
                  label: "Effort",
                  type: "select",
                  options: [
                    { id: "low", label: "Low" },
                    { id: "high", label: "High", isDefault: true },
                  ],
                },
              ],
            },
          },
        ],
      },
    ],
  },
  settings: {},
};

const workspace: WorkspaceOptions = {
  project_id: "p1",
  cwd: "/w/alpha",
  is_repo: true,
  ref_name: "main",
  refs: [
    { name: "main", kind: "local", worktree_path: "/w/alpha" },
    { name: "feat/x", kind: "local", worktree_path: "/w/alpha-feat" },
    { name: "dev", kind: "local" },
  ],
};

function thread(over: Partial<ThreadSummary> = {}): ThreadSummary {
  return {
    id: "old",
    environment_id: "e1",
    environment_name: "Mac",
    project_id: "p1",
    project_name: "Alpha",
    title: "Old",
    status: "idle",
    branch: "feat/x",
    provider: "codex",
    model: "gpt-5",
    updated_at: "2026-10-01T00:00:00Z",
    pull_request_count: 0,
    ...over,
  };
}

function Landing() {
  const { environmentId, threadId } = useParams();
  return <p>landed {environmentId}/{threadId}</p>;
}

function setEnvironments(items: Environment[]) {
  api.envList.mockResolvedValue({ data: { items } });
}

function renderPage() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={client}>
      <MemoryRouter initialEntries={["/threads/new"]}>
        <Routes>
          <Route path="/threads/new" element={<NewThreadPage />} />
          <Route path="/threads/:environmentId/:threadId" element={<Landing />} />
        </Routes>
      </MemoryRouter>
    </QueryClientProvider>,
  );
}

async function ready() {
  const box = await screen.findByRole("textbox", { name: "消息内容" });
  await waitFor(() => expect(screen.getByRole("button", { name: "项目" })).toHaveTextContent("Alpha"));
  await waitFor(() => expect(api.workspaceOptions).toHaveBeenCalled());
  return box;
}

describe("NewThreadPage", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    signInAs("u1");
    setEnvironments([makeEnvironment()]);
    api.list.mockResolvedValue({ data: { items: [], environments: [] } });
    api.composerOptions.mockResolvedValue({ data: composerOptions });
    api.workspaceOptions.mockResolvedValue({ data: workspace });
    api.create.mockImplementation(async (_env, input) => ({ thread_id: input.thread_id, resumed: false }));
  });
  afterEach(teardownAuth);

  it("opens the composer without creating anything until the first message is sent", async () => {
    renderPage();
    await ready();
    expect(api.create).not.toHaveBeenCalled();
    expect(screen.getByRole("button", { name: "发送" })).toBeDisabled();
  });

  it("sends from the current checkout without a branch, then opens the thread", async () => {
    const user = userEvent.setup();
    renderPage();
    const box = await ready();
    await user.type(box, "修一下登录");
    await user.click(screen.getByRole("button", { name: "发送" }));

    await waitFor(() => expect(api.create).toHaveBeenCalledTimes(1));
    const [envId, input] = api.create.mock.calls[0];
    expect(envId).toBe("e1");
    expect(input).toMatchObject({
      project_id: "p1",
      message: { text: "修一下登录", attachments: [] },
      runtime_mode: "full-access",
      workspace: { type: "root" },
      model_selection: {
        instance_id: "codex",
        model: "gpt-5",
        options: [{ id: "effort", value: "high" }],
      },
    });
    expect(input.workspace).not.toHaveProperty("branch");
    expect(await screen.findByText(`landed e1/${input.thread_id}`)).toBeInTheDocument();
  });

  it("shows the current branch for Current checkout without offering a switch", async () => {
    renderPage();
    await ready();
    expect(await screen.findByText("main")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "分支" })).toBeNull();
  });

  it("reuses command, thread and message ids when a send is retried", async () => {
    const user = userEvent.setup();
    api.create.mockRejectedValueOnce(new ApiRequestError(502, 50202, "T3 环境调用失败"));
    renderPage();
    await user.type(await ready(), "hi");
    await user.click(screen.getByRole("button", { name: "发送" }));
    expect(await screen.findByText("T3 环境调用失败")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "发送" }));

    await waitFor(() => expect(api.create).toHaveBeenCalledTimes(2));
    const [first, second] = api.create.mock.calls.map(([, input]) => input);
    expect(second.command_id).toBe(first.command_id);
    expect(second.thread_id).toBe(first.thread_id);
    expect(second.message.message_id).toBe(first.message.message_id);
  });

  it("creates a new worktree from the current branch, omitting an empty branch name", async () => {
    const user = userEvent.setup();
    renderPage();
    const box = await ready();
    await user.type(box, "go");
    await user.click(screen.getByRole("button", { name: "工作区" }));
    await user.click(await screen.findByRole("menuitem", { name: /New worktree/ }));
    expect(screen.getByRole("button", { name: "分支" })).toHaveTextContent("From main");
    await user.click(screen.getByRole("button", { name: "发送" }));

    await waitFor(() => expect(api.create).toHaveBeenCalledTimes(1));
    const { workspace: sent } = api.create.mock.calls[0][1];
    expect(sent).toEqual({ type: "worktree", base_ref: "main" });
  });

  it("sends the typed branch name for a new worktree", async () => {
    const user = userEvent.setup();
    renderPage();
    const box = await ready();
    await user.type(box, "go");
    await user.click(screen.getByRole("button", { name: "工作区" }));
    await user.click(await screen.findByRole("menuitem", { name: /New worktree/ }));
    await user.type(screen.getByLabelText("新分支名"), "feature/new");
    await user.click(screen.getByRole("button", { name: "发送" }));
    await waitFor(() => expect(api.create).toHaveBeenCalledTimes(1));
    expect(api.create.mock.calls[0][1].workspace).toEqual({
      type: "worktree",
      base_ref: "main",
      branch: "feature/new",
    });
  });

  it("selecting a branch that already has a worktree uses existing_worktree", async () => {
    const user = userEvent.setup();
    renderPage();
    const box = await ready();
    await user.type(box, "go");
    await user.click(screen.getByRole("button", { name: "工作区" }));
    await user.click(await screen.findByRole("menuitem", { name: /New worktree/ }));
    await user.click(screen.getByRole("button", { name: "分支" }));
    await user.click(await screen.findByRole("menuitem", { name: /feat\/x/ }));
    await user.click(screen.getByRole("button", { name: "发送" }));

    await waitFor(() => expect(api.create).toHaveBeenCalledTimes(1));
    expect(api.create.mock.calls[0][1].workspace).toEqual({
      type: "existing_worktree",
      worktree_path: "/w/alpha-feat",
    });
  });

  it("offers the most recent other worktree as Previous worktree", async () => {
    const user = userEvent.setup();
    api.list.mockResolvedValue({
      data: {
        items: [
          thread({ id: "a", branch: "feat/x", updated_at: "2026-10-02T00:00:00Z" }),
          thread({ id: "b", branch: "main", updated_at: "2026-10-05T00:00:00Z" }),
          thread({ id: "c", branch: "feat/x", status: "archived", updated_at: "2026-10-06T00:00:00Z" }),
        ],
        environments: [],
      },
    });
    renderPage();
    await user.type(await ready(), "go");
    await user.click(screen.getByRole("button", { name: "工作区" }));
    await user.click(await screen.findByRole("menuitem", { name: /Previous worktree \(feat\/x\)/ }));
    await user.click(screen.getByRole("button", { name: "发送" }));
    await waitFor(() => expect(api.create).toHaveBeenCalledTimes(1));
    expect(api.create.mock.calls[0][1].workspace).toEqual({
      type: "existing_worktree",
      worktree_path: "/w/alpha-feat",
    });
  });

  it("greys out read-only environments in the picker", async () => {
    const user = userEvent.setup();
    setEnvironments([
      makeEnvironment({ id: "e0", name: "Old Mac", access: "read-only", scopes: ["orchestration:read"] }),
      makeEnvironment({ id: "e1", name: "Mac" }),
    ]);
    renderPage();
    await ready();
    await user.click(screen.getByRole("button", { name: "运行环境" }));
    const readOnly = await screen.findByRole("menuitemradio", { name: /Old Mac/ });
    expect(readOnly).toHaveAttribute("aria-disabled", "true");
    expect(screen.getByRole("menuitemradio", { name: /^Mac/ })).not.toHaveAttribute("aria-disabled", "true");
  });

  it("cannot send from a read-only environment", async () => {
    setEnvironments([makeEnvironment({ access: "read-only", scopes: ["orchestration:read"] })]);
    renderPage();
    const box = await screen.findByRole("textbox", { name: "消息内容" });
    expect(box).toBeDisabled();
    expect(screen.getByRole("button", { name: "发送" })).toBeDisabled();
    expect(screen.getAllByText("该环境为只读会话，重新配对后可操作").length).toBeGreaterThan(0);
  });

  it("keeps the message text but drops project and model choices when the environment changes", async () => {
    const user = userEvent.setup();
    setEnvironments([
      makeEnvironment({ id: "e1", name: "Mac" }),
      makeEnvironment({ id: "e2", name: "Linux" }),
    ]);
    api.composerOptions.mockImplementation(async (envId: string) => ({
      data:
        envId === "e1"
          ? composerOptions
          : { projects: [{ id: "px", title: "Gamma" }], config: { providers: [] }, settings: {} },
    }));
    renderPage();
    const box = await ready();
    await user.type(box, "keep me");
    await user.click(screen.getByRole("button", { name: "运行环境" }));
    await user.click(await screen.findByRole("menuitemradio", { name: /Linux/ }));

    await waitFor(() => expect(screen.getByRole("button", { name: "项目" })).toHaveTextContent("Gamma"));
    expect(screen.getByRole("textbox", { name: "消息内容" })).toHaveValue("keep me");
    expect(screen.getByRole("button", { name: "模型" })).toHaveTextContent("默认模型");
    await user.click(screen.getByRole("button", { name: "发送" }));
    await waitFor(() => expect(api.create).toHaveBeenCalledTimes(1));
    expect(api.create.mock.calls[0][0]).toBe("e2");
    expect(api.create.mock.calls[0][1].project_id).toBe("px");
    expect(api.create.mock.calls[0][1].model_selection).toBeUndefined();
  });

  it("uploads a picked image as raw bytes and sends its metadata with the first message", async () => {
    const user = userEvent.setup();
    const attachment = { type: "image", id: "att-1", name: "a.png", mime_type: "image/png", size_bytes: 3 };
    api.uploadAttachment.mockResolvedValue(attachment);
    renderPage();
    await ready();

    const file = new File([new Uint8Array([1, 2, 3])], "a.png", { type: "image/png" });
    await user.upload(screen.getByTestId("image-input"), file);
    await waitFor(() => expect(api.uploadAttachment).toHaveBeenCalledWith("e1", file));
    const form = screen.getByRole("form", { name: "消息输入" });
    await waitFor(() => expect(within(form).getByRole("button", { name: "发送" })).toBeEnabled());
    await user.click(within(form).getByRole("button", { name: "发送" }));
    await waitFor(() => expect(api.create).toHaveBeenCalledTimes(1));
    expect(api.create.mock.calls[0][1].message).toMatchObject({ text: "", attachments: [attachment] });
    expect(api.deleteAttachment).not.toHaveBeenCalled();
  });
});
