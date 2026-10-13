import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import EnvironmentsPage from "@/routes/settings/environments";
import { signInAs, teardownAuth } from "@/test/auth";
import { renderWithRoute } from "@/test/render";
import { makeEnvironment } from "@/test/threads";
import {
  useCreateEnvironment,
  useDeleteEnvironment,
  useEnvironments,
  useUpdateEnvironment,
} from "@/lib/hooks/use-threads";
import type { Environment } from "@/lib/api/threads";

vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: vi.fn() } }));
vi.mock("@/lib/hooks/use-threads", () => ({
  useEnvironments: vi.fn(),
  useCreateEnvironment: vi.fn(),
  useUpdateEnvironment: vi.fn(),
  useDeleteEnvironment: vi.fn(),
}));

const create = vi.fn();
const update = vi.fn();
const remove = vi.fn();

const refetch = vi.fn();

function envs(items: Environment[] | undefined, over: Record<string, unknown> = {}) {
  vi.mocked(useEnvironments).mockReturnValue({
    data: items,
    isLoading: false,
    isError: false,
    isFetching: false,
    refetch,
    ...over,
  } as unknown as ReturnType<typeof useEnvironments>);
}

const base: Environment = makeEnvironment();

function renderPage() {
  return renderWithRoute(<EnvironmentsPage />, {
    path: "/settings/environments",
    initialEntry: "/settings/environments",
  });
}

describe("EnvironmentsPage", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    signInAs("u1");
    create.mockResolvedValue({});
    update.mockResolvedValue({});
    remove.mockResolvedValue({});
    vi.mocked(useCreateEnvironment).mockReturnValue({
      mutateAsync: create,
      isPending: false,
    } as unknown as ReturnType<typeof useCreateEnvironment>);
    vi.mocked(useUpdateEnvironment).mockReturnValue({
      mutateAsync: update,
      isPending: false,
    } as unknown as ReturnType<typeof useUpdateEnvironment>);
    vi.mocked(useDeleteEnvironment).mockReturnValue({
      mutateAsync: remove,
    } as unknown as ReturnType<typeof useDeleteEnvironment>);
  });

  afterEach(teardownAuth);

  it("shows the empty state and adds an environment from a pairing link", async () => {
    const user = userEvent.setup();
    envs([]);
    renderPage();
    expect(screen.getByText("暂无环境")).toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: "添加环境" }));
    await user.click(screen.getByRole("button", { name: "保存" }));
    expect(screen.getByRole("alert")).toHaveTextContent("请填写环境名称");

    await user.type(screen.getByLabelText("环境名称"), "工作电脑");
    await user.click(screen.getByRole("button", { name: "保存" }));
    expect(screen.getByRole("alert")).toHaveTextContent("请粘贴配对链接");

    await user.type(screen.getByLabelText("配对链接"), "https://t3.example/pair#token=abc");
    await user.click(screen.getByRole("button", { name: "保存" }));
    await waitFor(() =>
      expect(create).toHaveBeenCalledWith({
        name: "工作电脑",
        pairing_url: "https://t3.example/pair#token=abc",
      }),
    );
  });

  it("shows a load failure with retry instead of the empty state", async () => {
    const user = userEvent.setup();
    envs(undefined, { isError: true });
    renderPage();
    expect(screen.getByRole("alert")).toHaveTextContent("环境加载失败");
    expect(screen.queryByText("暂无环境")).toBeNull();
    await user.click(screen.getByRole("button", { name: "重试" }));
    expect(refetch).toHaveBeenCalledTimes(1);
  });

  it("keeps the stale list and warns when a refresh fails", async () => {
    const user = userEvent.setup();
    envs([base], { isError: true });
    renderPage();
    expect(screen.getByText("Mac")).toBeInTheDocument();
    expect(screen.getByRole("alert")).toHaveTextContent("刷新失败");
    await user.click(screen.getByRole("button", { name: "重试" }));
    expect(refetch).toHaveBeenCalledTimes(1);
  });

  it("lists status labels and never renders tokens", () => {
    envs([
      base,
      { ...base, id: "e2", name: "Linux", status: "unreachable", error: "timeout" },
      { ...base, id: "e3", name: "Office", status: "repair" },
    ]);
    const { container } = renderPage();
    expect(screen.getByText("正常")).toBeInTheDocument();
    expect(screen.getByText("不可达")).toBeInTheDocument();
    expect(screen.getByText("需重新配对")).toBeInTheDocument();
    expect(screen.getByText("timeout")).toBeInTheDocument();
    // 设置导航含「GitHub Tokens」；这里只断言配对凭证本身不落页。
    expect(screen.getAllByRole("link", { name: "GitHub Tokens" }).length).toBeGreaterThan(0);
    expect(container.textContent).not.toMatch(/token=/i);
  });

  it("shows the two access levels with full scopes, and no local permission editing", async () => {
    const user = userEvent.setup();
    envs([
      base,
      makeEnvironment({
        id: "e2",
        name: "Linux",
        access: "read-only",
        scopes: ["orchestration:read"],
      }),
    ]);
    renderPage();
    expect(screen.getByText("可操作")).toBeInTheDocument();
    expect(screen.getByText("只读")).toBeInTheDocument();
    expect(screen.getByText("重新配对后可操作")).toBeInTheDocument();

    const [first] = screen.getAllByText("查看 scopes");
    await user.click(first);
    expect(screen.getByText("orchestration:operate")).toBeInTheDocument();
    expect(screen.queryByRole("checkbox")).toBeNull();
    expect(screen.queryByRole("switch")).toBeNull();
  });

  it("renames, re-pairs and deletes", async () => {
    const user = userEvent.setup();
    envs([base]);
    renderPage();

    await user.click(screen.getByRole("button", { name: "修改名称 Mac" }));
    const name = screen.getByLabelText("环境名称");
    await user.clear(name);
    await user.type(name, "MacBook");
    await user.click(screen.getByRole("button", { name: "保存" }));
    await waitFor(() => expect(update).toHaveBeenCalledWith({ id: "e1", name: "MacBook" }));

    await user.click(screen.getByRole("button", { name: "重新配对 Mac" }));
    await user.type(screen.getByLabelText("配对链接"), "https://t3.example/new");
    await user.click(screen.getByRole("button", { name: "保存" }));
    await waitFor(() =>
      expect(update).toHaveBeenCalledWith({ id: "e1", pairing_url: "https://t3.example/new" }),
    );

    await user.click(screen.getByRole("button", { name: "删除环境 Mac" }));
    await user.click(await screen.findByRole("button", { name: "确认删除" }));
    await waitFor(() => expect(remove).toHaveBeenCalledWith("e1"));
  });
});
