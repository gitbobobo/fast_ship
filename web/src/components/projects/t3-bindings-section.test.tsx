import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { Environment } from "@/lib/api/threads";
import type {
  ProjectT3Binding,
  T3EnvironmentProjects,
} from "@/lib/api/t3-bindings";
import { T3BindingsSection } from "@/components/projects/t3-bindings-section";
import { makeEnvironment } from "@/test/threads";
import { withTestQueryClient } from "@/test/render";

const {
  environmentsRef,
  discoveryRef,
  bindingsRef,
  bindMock,
  unbindMock,
  toastSuccessMock,
  toastErrorMock,
} = vi.hoisted(() => ({
  environmentsRef: {
    current: {
      data: undefined as Environment[] | undefined,
      isLoading: false,
      isError: false,
    },
  },
  discoveryRef: {
    current: {
      data: undefined as T3EnvironmentProjects[] | undefined,
      isLoading: false,
      isError: false,
    },
  },
  bindingsRef: {
    current: {
      data: undefined as ProjectT3Binding[] | undefined,
      isLoading: false,
      isError: false,
    },
  },
  bindMock: vi.fn(),
  unbindMock: vi.fn(),
  toastSuccessMock: vi.fn(),
  toastErrorMock: vi.fn(),
}));

vi.mock("sonner", () => ({
  toast: { success: toastSuccessMock, error: toastErrorMock },
}));

vi.mock("@/lib/hooks/use-threads", () => ({
  useEnvironments: () => environmentsRef.current,
}));

// bind/unbind 用真实 useMutation，保持 isPending 与按钮置灰的真实节奏。
vi.mock("@/lib/hooks/use-t3-bindings", async () => {
  const { useMutation } = await import("@tanstack/react-query");
  return {
    useT3EnvironmentProjects: () => discoveryRef.current,
    useProjectT3Bindings: () => bindingsRef.current,
    useBindT3Project: () =>
      useMutation({
        mutationFn: (v: { environmentId: string; t3ProjectId: string }) =>
          bindMock(v),
      }),
    useUnbindT3Project: () =>
      useMutation({ mutationFn: (envId: string) => unbindMock(envId) }),
  };
});

function makeBinding(
  environmentId: string,
  over: Partial<ProjectT3Binding> = {},
): ProjectT3Binding {
  return {
    project_id: "proj-1",
    environment_id: environmentId,
    environment_name: environmentId,
    environment_status: "ok",
    environment_access: "operate",
    t3_project_id: `t3-${environmentId}`,
    t3_project_title: "Alpha",
    workspace_root: "/w/alpha",
    ...over,
  };
}

function discovered(
  environmentId: string,
  over: Partial<T3EnvironmentProjects> = {},
): T3EnvironmentProjects {
  return {
    environment_id: environmentId,
    name: environmentId,
    status: "ok",
    access: "operate",
    projects: [
      { id: `t3-${environmentId}`, title: "Alpha", workspace_root: "/w/alpha" },
      { id: `other-${environmentId}`, title: "Beta", workspace_root: "/w/beta" },
    ],
    ...over,
  };
}

function renderSection() {
  return render(
    withTestQueryClient(
      <MemoryRouter>
        <T3BindingsSection projectId="proj-1" />
      </MemoryRouter>,
    ),
  );
}

describe("T3BindingsSection", () => {
  beforeEach(() => {
    environmentsRef.current = {
      data: [makeEnvironment({ id: "e1", name: "Mac" })],
      isLoading: false,
      isError: false,
    };
    discoveryRef.current = {
      data: [discovered("e1")],
      isLoading: false,
      isError: false,
    };
    bindingsRef.current = {
      data: [makeBinding("e1")],
      isLoading: false,
      isError: false,
    };
    bindMock.mockReset();
    bindMock.mockResolvedValue({});
    unbindMock.mockReset();
    unbindMock.mockResolvedValue({});
    toastSuccessMock.mockReset();
    toastErrorMock.mockReset();
  });

  it("shows the pairing empty state when no environments exist", () => {
    environmentsRef.current = { data: [], isLoading: false, isError: false };
    bindingsRef.current = { data: [], isLoading: false, isError: false };

    renderSection();

    expect(screen.getByText(/还没有连接 T3 环境/)).toBeInTheDocument();
    expect(
      screen.getByRole("link", { name: "去设置配对" }),
    ).toHaveAttribute("href", "/settings/environments");
  });

  it("binds a discovered project through the row select", async () => {
    bindingsRef.current = { data: [], isLoading: false, isError: false };
    const user = userEvent.setup();
    renderSection();

    const trigger = screen.getByRole("combobox", { name: "Mac 的 T3 项目" });
    expect(trigger).toHaveTextContent("不绑定");

    await user.click(trigger);
    await user.click(await screen.findByRole("option", { name: "Beta" }));

    await waitFor(() =>
      expect(bindMock).toHaveBeenCalledWith({
        environmentId: "e1",
        t3ProjectId: "other-e1",
      }),
    );
    await waitFor(() =>
      expect(toastSuccessMock).toHaveBeenCalledWith("已绑定"),
    );
  });

  it("unbinds through the row action", async () => {
    const user = userEvent.setup();
    renderSection();

    const trigger = screen.getByRole("combobox", { name: "Mac 的 T3 项目" });
    expect(trigger).toHaveTextContent("Alpha");

    await user.click(screen.getByRole("button", { name: "解绑 Mac" }));

    await waitFor(() => expect(unbindMock).toHaveBeenCalledWith("e1"));
    await waitFor(() =>
      expect(toastSuccessMock).toHaveBeenCalledWith("已解绑"),
    );
  });

  it("marks a stale binding in red and keeps it selectable as an option", async () => {
    // 绑定的 T3 项目已不在发现结果里 → 标红，但旧值仍留作可选项供修复/解绑
    bindingsRef.current = {
      data: [makeBinding("e1", { t3_project_id: "gone", t3_project_title: "Old" })],
      isLoading: false,
      isError: false,
    };
    const user = userEvent.setup();
    renderSection();

    expect(screen.getByRole("alert")).toHaveTextContent(
      "绑定已失效：绑定的 T3 项目已不存在",
    );
    const trigger = screen.getByRole("combobox", { name: "Mac 的 T3 项目" });
    expect(trigger).toHaveClass("text-destructive");
    expect(trigger).not.toBeDisabled();

    await user.click(trigger);
    expect(
      await screen.findByRole("option", { name: /Old（已失效）/ }),
    ).toBeInTheDocument();
  });

  it("disables the row when the environment is unreachable and explains the stale binding", () => {
    environmentsRef.current = {
      data: [makeEnvironment({ id: "e1", name: "Mac", status: "unreachable" })],
      isLoading: false,
      isError: false,
    };
    discoveryRef.current = {
      data: [discovered("e1", { projects: [] })],
      isLoading: false,
      isError: false,
    };

    renderSection();

    expect(screen.getByRole("alert")).toHaveTextContent(
      "绑定已失效：环境不可达",
    );
    expect(
      screen.getByRole("combobox", { name: "Mac 的 T3 项目" }),
    ).toBeDisabled();
    // 失效行仍可解绑
    expect(
      screen.getByRole("button", { name: "解绑 Mac" }),
    ).not.toBeDisabled();
  });

  it("shows the global discovery failure alert and disables selects", () => {
    discoveryRef.current = {
      data: undefined,
      isLoading: false,
      isError: true,
    };

    renderSection();

    expect(screen.getByRole("alert")).toHaveTextContent(
      "T3 项目发现失败，仅展示已保存的绑定",
    );
    expect(
      screen.getByRole("combobox", { name: "Mac 的 T3 项目" }),
    ).toBeDisabled();
  });

  it("disables a row whose environment failed discovery", () => {
    discoveryRef.current = {
      data: [discovered("e1", { error: "daemon 超时", projects: [] })],
      isLoading: false,
      isError: false,
    };

    renderSection();

    expect(screen.getByRole("alert")).toHaveTextContent(
      "绑定已失效：环境发现失败",
    );
    expect(
      screen.getByRole("combobox", { name: "Mac 的 T3 项目" }),
    ).toBeDisabled();
  });
});
