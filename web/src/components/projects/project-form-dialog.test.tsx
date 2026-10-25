import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { ProjectFormDialog } from "@/components/projects/project-form-dialog";
import { renderWithRoute } from "@/test/render";
import {
  useCreateProject,
  useProject,
  useProjects,
  useUpdateProject,
} from "@/lib/hooks/use-projects";
import { useIntegrationOptions } from "@/lib/hooks/use-integration-options";

vi.mock("sonner", () => ({
  toast: {
    success: vi.fn(),
    error: vi.fn(),
  },
}));

vi.mock("@/lib/hooks/use-projects", () => ({
  useProject: vi.fn(),
  useCreateProject: vi.fn(),
  useUpdateProject: vi.fn(),
  useProjects: vi.fn(),
}));

vi.mock("@/lib/hooks/use-integration-options", () => ({
  useIntegrationOptions: vi.fn(),
}));

// 集成选择器选项（契约保证首条恒为内置 github-default）。
const integrationOptionsFixture: IntegrationOption[] = [
  {
    id: "github-default",
    kind: "github",
    name: "内置 GitHub",
    base_url: "https://github.com",
    builtin: true,
  },
  {
    id: "forge-1",
    kind: "forgejo",
    name: "自建 Forgejo",
    base_url: "https://forge.example.com",
    builtin: false,
  },
];

// 无 GitHub 仓库的 internal 项目：repository_url 回填为空，
// 更新 payload 只剩 name/description，便于断言 description 字段本身。
const projectFixture: Project = {
  id: "proj-1",
  name: "fast-ship",
  description: "旧描述",
  github_owner: "",
  github_repo: "",
  repository_url: null,
  code_github_owner: "",
  code_repo_url: null,
  code_github_repo: "",
  public_github_token_id: "",
  code_github_token_id: "",
  code_integration_id: null,
  public_integration_id: null,
  shelved_at: null,
  shelved_until: null,
  is_shelved: false,
  created_at: "2026-10-01T00:00:00Z",
  updated_at: "2026-10-01T00:00:00Z",
};

// 项目夹具变体用 Partial 覆盖生成，默认是无代码仓的 internal 项目。
function makeProject(overrides: Partial<Project> = {}): Project {
  return { ...projectFixture, ...overrides };
}

function renderDialog(mode: "create" | "edit", project: Project = projectFixture) {
  vi.mocked(useProject).mockReturnValue({
    data: project,
    isLoading: false,
  } as unknown as ReturnType<typeof useProject>);
  return renderWithRoute(
    <ProjectFormDialog
      open={true}
      onOpenChange={vi.fn()}
      mode={mode}
      projectId={mode === "edit" ? project.id : undefined}
    />,
    { path: "/projects", initialEntry: "/projects" },
  );
}

// INT-67 回归：编辑保存的描述须随请求发出（含清空时的空串），否则后端无从写入。
describe("ProjectFormDialog", () => {
  const createMutateAsync = vi.fn();
  const updateMutateAsync = vi.fn();
  const refetchIntegrations = vi.fn();

  function mockIntegrationOptions(
    overrides: Partial<ReturnType<typeof useIntegrationOptions>> = {},
  ) {
    vi.mocked(useIntegrationOptions).mockReturnValue({
      data: integrationOptionsFixture,
      isLoading: false,
      isError: false,
      refetch: refetchIntegrations,
      ...overrides,
    } as unknown as ReturnType<typeof useIntegrationOptions>);
  }

  const repoChangeWarning =
    "修改代码仓将自动停用 PR 审查，保存后需重新开启";

  beforeEach(() => {
    vi.clearAllMocks();
    mockIntegrationOptions();
    vi.mocked(useProject).mockReturnValue({
      data: projectFixture,
      isLoading: false,
    } as unknown as ReturnType<typeof useProject>);
    vi.mocked(useCreateProject).mockReturnValue({
      mutateAsync: createMutateAsync,
    } as unknown as ReturnType<typeof useCreateProject>);
    vi.mocked(useUpdateProject).mockReturnValue({
      mutateAsync: updateMutateAsync,
    } as unknown as ReturnType<typeof useUpdateProject>);
    vi.mocked(useProjects).mockReturnValue({
      data: { items: [], total: 0, page: 1, page_size: 20 },
      isLoading: false,
    } as unknown as ReturnType<typeof useProjects>);
  });

  it("编辑模式打开时回填已有描述", async () => {
    renderDialog("edit");

    expect(await screen.findByLabelText("项目描述（可选）")).toHaveValue("旧描述");
  });

  it("编辑提交修改后的描述", async () => {
    const user = userEvent.setup();
    renderDialog("edit");

    const descriptionInput = await screen.findByLabelText("项目描述（可选）");
    await user.clear(descriptionInput);
    await user.type(descriptionInput, "新描述");
    await user.click(screen.getByRole("button", { name: "保存" }));

    await waitFor(() =>
      expect(updateMutateAsync).toHaveBeenCalledWith({
        name: "fast-ship",
        description: "新描述",
      }),
    );
  });

  it("清空描述提交时 payload 携带空串而非省略字段", async () => {
    const user = userEvent.setup();
    renderDialog("edit");

    const descriptionInput = await screen.findByLabelText("项目描述（可选）");
    await user.clear(descriptionInput);
    await user.click(screen.getByRole("button", { name: "保存" }));

    await waitFor(() =>
      expect(updateMutateAsync).toHaveBeenCalledWith({
        name: "fast-ship",
        description: "",
      }),
    );
  });

  it("新建模式提交非空描述", async () => {
    createMutateAsync.mockResolvedValue({ data: { id: "proj-9" } });
    const user = userEvent.setup();
    renderDialog("create");

    await user.type(screen.getByLabelText("项目名称"), "new-proj");
    await user.type(screen.getByLabelText("项目描述（可选）"), "初始描述");
    await user.click(screen.getByRole("button", { name: "创建项目" }));

    await waitFor(() =>
      expect(createMutateAsync).toHaveBeenCalledWith({
        name: "new-proj",
        description: "初始描述",
      }),
    );
  });

  it("编辑回填：forge 绑定按 code_integration_id 选中，owner/repo 剥出主机", async () => {
    renderDialog(
      "edit",
      makeProject({
        code_integration_id: "forge-1",
        code_repo_url: "https://forge.example.com/acme/api",
      }),
    );

    expect(
      await screen.findByRole("combobox", { name: "代码仓集成" }),
    ).toHaveTextContent("自建 Forgejo");
    expect(screen.getByLabelText(/owner \/ repo/)).toHaveValue("acme/api");
  });

  it("编辑回填：github 绑定剥出 owner/repo；未改动保存不携带代码仓字段", async () => {
    const user = userEvent.setup();
    renderDialog(
      "edit",
      makeProject({
        code_integration_id: "github-default",
        code_repo_url: "https://github.com/acme/api",
      }),
    );

    expect(await screen.findByLabelText(/owner \/ repo/)).toHaveValue("acme/api");
    await user.click(screen.getByRole("button", { name: "保存" }));

    await waitFor(() => expect(updateMutateAsync).toHaveBeenCalled());
    expect(updateMutateAsync.mock.calls[0][0]).not.toHaveProperty(
      "code_repository_url",
    );
  });

  it("改动 owner/repo：显示停用提示并合成裸形态 URL（github-default）", async () => {
    const user = userEvent.setup();
    renderDialog(
      "edit",
      makeProject({
        code_integration_id: "github-default",
        code_repo_url: "https://github.com/acme/api",
      }),
    );

    const input = await screen.findByLabelText(/owner \/ repo/);
    expect(screen.queryByText(repoChangeWarning)).not.toBeInTheDocument();
    await user.clear(input);
    await user.type(input, "acme/other");
    expect(screen.getByText(repoChangeWarning)).toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: "保存" }));
    await waitFor(() =>
      expect(updateMutateAsync).toHaveBeenCalledWith(
        expect.objectContaining({ code_repository_url: "acme/other" }),
      ),
    );
  });

  it("forge 绑定改动 owner/repo：合成 {base_url}/{owner}/{repo}", async () => {
    const user = userEvent.setup();
    renderDialog(
      "edit",
      makeProject({
        code_integration_id: "forge-1",
        code_repo_url: "https://forge.example.com/acme/api",
      }),
    );

    const input = await screen.findByLabelText(/owner \/ repo/);
    await user.clear(input);
    await user.type(input, "acme/other");
    await user.click(screen.getByRole("button", { name: "保存" }));

    await waitFor(() =>
      expect(updateMutateAsync).toHaveBeenCalledWith(
        expect.objectContaining({
          code_repository_url: "https://forge.example.com/acme/other",
        }),
      ),
    );
  });

  it("下拉切换集成：github 换 forge 后按新集成合成 URL", async () => {
    const user = userEvent.setup();
    renderDialog(
      "edit",
      makeProject({
        code_integration_id: "github-default",
        code_repo_url: "https://github.com/acme/api",
      }),
    );

    await screen.findByLabelText(/owner \/ repo/);
    await user.click(screen.getByRole("combobox", { name: "代码仓集成" }));
    await user.click(await screen.findByRole("option", { name: /自建 Forgejo/ }));
    await user.click(screen.getByRole("button", { name: "保存" }));

    await waitFor(() =>
      expect(updateMutateAsync).toHaveBeenCalledWith(
        expect.objectContaining({
          code_repository_url: "https://forge.example.com/acme/api",
        }),
      ),
    );
  });

  it("清空 owner/repo 提交 null 回落公开仓库", async () => {
    const user = userEvent.setup();
    renderDialog(
      "edit",
      makeProject({
        code_integration_id: "github-default",
        code_repo_url: "https://github.com/acme/api",
      }),
    );

    await user.clear(await screen.findByLabelText(/owner \/ repo/));
    await user.click(screen.getByRole("button", { name: "保存" }));

    await waitFor(() =>
      expect(updateMutateAsync).toHaveBeenCalledWith(
        expect.objectContaining({ code_repository_url: null }),
      ),
    );
  });

  it("悬空绑定：选择器显示失效占位，未改动保存不携带字段", async () => {
    const user = userEvent.setup();
    renderDialog(
      "edit",
      makeProject({ code_integration_id: "dead-row", code_repo_url: null }),
    );

    expect(
      await screen.findByRole("combobox", { name: "代码仓集成" }),
    ).toHaveTextContent("已失效");
    await user.click(screen.getByRole("button", { name: "保存" }));

    await waitFor(() => expect(updateMutateAsync).toHaveBeenCalled());
    expect(updateMutateAsync.mock.calls[0][0]).not.toHaveProperty(
      "code_repository_url",
    );
  });

  it("悬空绑定：显式勾选清除后提交 null", async () => {
    const user = userEvent.setup();
    renderDialog(
      "edit",
      makeProject({ code_integration_id: "dead-row", code_repo_url: null }),
    );

    await screen.findByLabelText(/owner \/ repo/);
    await user.click(
      screen.getByLabelText("清除失效的代码仓绑定（回落到公开仓库）"),
    );
    await user.click(screen.getByRole("button", { name: "保存" }));

    await waitFor(() =>
      expect(updateMutateAsync).toHaveBeenCalledWith(
        expect.objectContaining({ code_repository_url: null }),
      ),
    );
  });

  it("悬空绑定：输入了 owner/repo 但集成失效时就地报错且不提交", async () => {
    const user = userEvent.setup();
    renderDialog(
      "edit",
      makeProject({ code_integration_id: "dead-row", code_repo_url: null }),
    );

    await user.type(await screen.findByLabelText(/owner \/ repo/), "acme/api");
    await user.click(screen.getByRole("button", { name: "保存" }));

    expect(
      await screen.findByText("所选代码仓集成不可用，请重新选择"),
    ).toBeInTheDocument();
    expect(updateMutateAsync).not.toHaveBeenCalled();
  });

  it("集成列表加载失败：保底内置 GitHub 并提供重试，github 仓仍可保存", async () => {
    mockIntegrationOptions({ data: undefined, isError: true });
    const user = userEvent.setup();
    renderDialog(
      "edit",
      makeProject({
        code_integration_id: "github-default",
        code_repo_url: "https://github.com/acme/api",
      }),
    );

    expect(
      await screen.findByRole("combobox", { name: "代码仓集成" }),
    ).toHaveTextContent("内置 GitHub");
    expect(screen.getByText(/集成列表加载失败/)).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "重试" }));
    expect(refetchIntegrations).toHaveBeenCalled();

    const input = screen.getByLabelText(/owner \/ repo/);
    await user.clear(input);
    await user.type(input, "acme/other");
    await user.click(screen.getByRole("button", { name: "保存" }));
    await waitFor(() =>
      expect(updateMutateAsync).toHaveBeenCalledWith(
        expect.objectContaining({ code_repository_url: "acme/other" }),
      ),
    );
  });

  it("集成列表加载中：绑定 github 的项目不受影响", async () => {
    mockIntegrationOptions({ data: undefined, isLoading: true });
    renderDialog(
      "edit",
      makeProject({
        code_integration_id: "github-default",
        code_repo_url: "https://github.com/acme/api",
      }),
    );

    expect(await screen.findByText("正在加载集成列表...")).toBeInTheDocument();
    expect(
      screen.getByRole("combobox", { name: "代码仓集成" }),
    ).toHaveTextContent("内置 GitHub");
  });

  it("集成列表加载中：forge 绑定未改动时保存不携带代码仓字段（合成不要求选项就绪）", async () => {
    mockIntegrationOptions({ data: undefined, isLoading: true });
    const user = userEvent.setup();
    renderDialog(
      "edit",
      makeProject({
        code_integration_id: "forge-1",
        code_repo_url: "https://forge.example.com/acme/api",
        code_github_owner: "acme",
        code_github_repo: "api",
      }),
    );

    expect(await screen.findByLabelText(/owner \/ repo/)).toHaveValue("acme/api");
    const nameInput = screen.getByLabelText("项目名称");
    await user.clear(nameInput);
    await user.type(nameInput, "renamed");
    await user.click(screen.getByRole("button", { name: "保存" }));

    await waitFor(() => expect(updateMutateAsync).toHaveBeenCalled());
    const payload = updateMutateAsync.mock.calls[0][0];
    expect(payload.name).toBe("renamed");
    expect(payload).not.toHaveProperty("code_repository_url");
  });

  it("无显式代码仓：换 forge 集成但 slug 留空时就地报错且不提交", async () => {
    const user = userEvent.setup();
    renderDialog(
      "edit",
      makeProject({
        github_owner: "acme",
        github_repo: "app",
        repository_url: "https://github.com/acme/app",
        code_integration_id: "github-default",
        code_repo_url: null,
      }),
    );

    await screen.findByRole("combobox", { name: "代码仓集成" });
    await user.click(screen.getByRole("combobox", { name: "代码仓集成" }));
    await user.click(await screen.findByRole("option", { name: /自建 Forgejo/ }));
    await user.click(screen.getByRole("button", { name: "保存" }));

    expect(
      await screen.findByText("更换代码仓集成时需填写 owner/repo"),
    ).toBeInTheDocument();
    expect(updateMutateAsync).not.toHaveBeenCalled();
  });

  it("集成列表加载中：forge 绑定未改动不显示停用提示（不误报仓库变更）", async () => {
    mockIntegrationOptions({ data: undefined, isLoading: true });
    renderDialog(
      "edit",
      makeProject({
        code_integration_id: "forge-1",
        code_repo_url: "https://forge.example.com/acme/api",
        code_github_owner: "acme",
        code_github_repo: "api",
      }),
    );

    await screen.findByLabelText(/owner \/ repo/);
    expect(screen.queryByText(repoChangeWarning)).not.toBeInTheDocument();
  });

  it("集成列表加载中：forge 绑定改动 owner/repo 时就地报错且不提交", async () => {
    mockIntegrationOptions({ data: undefined, isLoading: true });
    const user = userEvent.setup();
    renderDialog(
      "edit",
      makeProject({
        code_integration_id: "forge-1",
        code_repo_url: "https://forge.example.com/acme/api",
        code_github_owner: "acme",
        code_github_repo: "api",
      }),
    );

    const input = await screen.findByLabelText(/owner \/ repo/);
    await user.clear(input);
    await user.type(input, "acme/other");
    await user.click(screen.getByRole("button", { name: "保存" }));

    await waitFor(() =>
      expect(
        screen.getByText(/所选代码仓集成不可用/),
      ).toBeInTheDocument(),
    );
    expect(updateMutateAsync).not.toHaveBeenCalled();
  });

  it("新建项目：不显示停用提示，forge 集成合成完整 URL", async () => {
    const user = userEvent.setup();
    renderDialog("create");

    await user.type(screen.getByLabelText("项目名称"), "new-proj");
    await user.click(screen.getByRole("combobox", { name: "代码仓集成" }));
    await user.click(await screen.findByRole("option", { name: /自建 Forgejo/ }));
    await user.type(screen.getByLabelText(/owner \/ repo/), "acme/api");
    expect(screen.queryByText(repoChangeWarning)).not.toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: "创建项目" }));
    await waitFor(() =>
      expect(createMutateAsync).toHaveBeenCalledWith(
        expect.objectContaining({
          code_repository_url: "https://forge.example.com/acme/api",
        }),
      ),
    );
  });
});
