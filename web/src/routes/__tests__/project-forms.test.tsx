import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { vi } from "vitest";
import { ProjectFormDialog } from "@/components/projects/project-form-dialog";
import { VersionFormDialog } from "@/components/versions/version-form-dialog";
import { renderWithRoute } from "@/test/render";
import {
  useCreateProject,
  useProject,
  useProjectBranches,
  useUpdateProject,
  useProjects,
} from "@/lib/hooks/use-projects";
import { useCreateVersion } from "@/lib/hooks/use-versions";
import { toast } from "sonner";

const { mockNavigate } = vi.hoisted(() => ({
  mockNavigate: vi.fn(),
}));

vi.mock("react-router", async (importOriginal) => {
  const actual = await importOriginal<typeof import("react-router")>();
  return {
    ...actual,
    useNavigate: () => mockNavigate,
  };
});

vi.mock("sonner", () => ({
  toast: {
    success: vi.fn(),
    error: vi.fn(),
  },
}));

vi.mock("@/lib/hooks/use-projects", () => ({
  useProject: vi.fn(),
  useCreateProject: vi.fn(),
  useProjectBranches: vi.fn(),
  useUpdateProject: vi.fn(),
  useProjects: vi.fn(),
}));

vi.mock("@/lib/hooks/use-versions", () => ({
  useCreateVersion: vi.fn(),
}));

vi.mock("@/lib/hooks/use-github-tokens", () => ({
  useGithubTokens: vi.fn(),
  useCreateGithubToken: vi.fn(() => ({
    mutateAsync: vi.fn(),
    isPending: false,
  })),
}));

const { useGithubTokens } = await import("@/lib/hooks/use-github-tokens");

describe("ProjectAndVersionForms", () => {
  const createProjectMutateAsync = vi.fn();
  const updateProjectMutateAsync = vi.fn();
  const createVersionMutateAsync = vi.fn();

  beforeEach(() => {
    vi.mocked(useGithubTokens).mockReturnValue({
      data: [
        {
          id: "tok-public",
          title: "发布 Token",
          token_prefix: "ghp_publ",
          detection_status: "ok",
          detection_token_type: "classic",
          detection_scopes: ["repo"],
          detection_login: "octocat",
          detection_detail: "",
          detected_at: "2026-04-06T10:00:00Z",
          referenced_projects: [],
          created_at: "2026-04-06T10:00:00Z",
          updated_at: "2026-04-06T10:00:00Z",
        },
        {
          id: "tok-code",
          title: "代码仓库 Token",
          token_prefix: "ghp_code",
          detection_status: "unchecked",
          detection_token_type: "unknown",
          detection_scopes: [],
          detection_login: "",
          detection_detail: "",
          detected_at: null,
          referenced_projects: [],
          created_at: "2026-04-06T10:00:00Z",
          updated_at: "2026-04-06T10:00:00Z",
        },
      ],
      isLoading: false,
    } as unknown as ReturnType<typeof useGithubTokens>);

    vi.mocked(useCreateProject).mockReturnValue({
      mutateAsync: createProjectMutateAsync,
    } as unknown as ReturnType<typeof useCreateProject>);

    vi.mocked(useProject).mockReturnValue({
      data: {
        id: "proj-1",
        user_id: "user-1",
        name: "fast-ship",
        description: "old desc",
        github_owner: "old-owner",
        github_repo: "old-repo",
        code_github_owner: "code-owner",
        code_github_repo: "code-repo",
        created_at: "2026-04-06T10:00:00Z",
        updated_at: "2026-04-06T10:00:00Z",
      },
      isLoading: false,
    } as unknown as ReturnType<typeof useProject>);

    vi.mocked(useUpdateProject).mockReturnValue({
      mutateAsync: updateProjectMutateAsync,
    } as unknown as ReturnType<typeof useUpdateProject>);

    vi.mocked(useProjects).mockReturnValue({
      data: {
        items: [],
        total: 0,
        page: 1,
        page_size: 20,
      },
      isLoading: false,
    } as unknown as ReturnType<typeof useProjects>);

    vi.mocked(useProjectBranches).mockReturnValue({
      data: {
        branches: [
          { name: "main", sha: "abc123", default: true },
          { name: "release/1.0", sha: "def456", default: false },
        ],
        default_branch: "main",
      },
      isLoading: false,
      isError: false,
      refetch: vi.fn(),
    } as unknown as ReturnType<typeof useProjectBranches>);

    vi.mocked(useCreateVersion).mockReturnValue({
      mutateAsync: createVersionMutateAsync,
    } as unknown as ReturnType<typeof useCreateVersion>);
  });

  afterEach(() => {
    vi.clearAllMocks();
  });

  it("submits create project form and navigates to the new project", async () => {
    createProjectMutateAsync.mockResolvedValue({
      data: { id: "proj-99" },
    });

    const user = userEvent.setup();
    const onOpenChange = vi.fn();

    renderWithRoute(
      <ProjectFormDialog open={true} onOpenChange={onOpenChange} mode="create" />,
      { path: "/projects", initialEntry: "/projects" },
    );

    // 仓库与 Token 相互独立：只填仓库不选 Token 也能创建。
    await user.type(screen.getByLabelText("项目名称"), "fast-ship");
    await user.type(
      screen.getByLabelText("仓库链接（可选）"),
      "https://github.com/godbobo/fast_ship",
    );
    await user.click(screen.getByRole("button", { name: "创建项目" }));

    await waitFor(() =>
      expect(createProjectMutateAsync).toHaveBeenCalledWith({
        name: "fast-ship",
        repository_url: "https://github.com/godbobo/fast_ship",
      }),
    );
    expect(mockNavigate).toHaveBeenCalledWith("/issues?project=proj-99");
  });

  it("submits create project form with a token picked from the library", async () => {
    createProjectMutateAsync.mockResolvedValue({
      data: { id: "proj-99" },
    });

    const user = userEvent.setup();
    const onOpenChange = vi.fn();

    renderWithRoute(
      <ProjectFormDialog open={true} onOpenChange={onOpenChange} mode="create" />,
      { path: "/projects", initialEntry: "/projects" },
    );

    await user.type(screen.getByLabelText("项目名称"), "fast-ship");
    await user.type(
      screen.getByLabelText("仓库链接（可选）"),
      "godbobo/fast_ship",
    );

    // 公开仓库 Token 下拉在前：选项来自 Token 库而非其他项目。
    await user.click(screen.getAllByRole("combobox")[0]);
    await user.click(screen.getByRole("option", { name: /发布 Token/ }));

    await user.click(screen.getByRole("button", { name: "创建项目" }));

    await waitFor(() =>
      expect(createProjectMutateAsync).toHaveBeenCalledWith({
        name: "fast-ship",
        repository_url: "godbobo/fast_ship",
        public_github_token_id: "tok-public",
      }),
    );
    expect(mockNavigate).toHaveBeenCalledWith("/issues?project=proj-99");
  });

  it("submits edit project form without sending token when left empty", async () => {
    const user = userEvent.setup();
    const onOpenChange = vi.fn();

    renderWithRoute(
      <ProjectFormDialog
        open={true}
        onOpenChange={onOpenChange}
        mode="edit"
        projectId="proj-1"
      />,
      { path: "/projects", initialEntry: "/projects" },
    );

    const nameInput = screen.getByLabelText("项目名称");
    const repositoryUrlInput = screen.getByLabelText("仓库链接（可选）");

    expect(nameInput).toHaveValue("fast-ship");
    expect(repositoryUrlInput).toHaveValue(
      "https://github.com/old-owner/old-repo",
    );

    await user.clear(repositoryUrlInput);
    await user.type(repositoryUrlInput, "new-owner/new-repo");
    await user.click(screen.getByRole("button", { name: "保存" }));

    await waitFor(() =>
      expect(updateProjectMutateAsync).toHaveBeenCalledWith({
        name: "fast-ship",
        description: "old desc",
        repository_url: "new-owner/new-repo",
      }),
    );
    // 未改动的引用字段不进 payload（三态缺省保留现值）
    const payload = updateProjectMutateAsync.mock.calls[0][0];
    expect(payload.public_github_token_id).toBeUndefined();
    expect(payload.code_github_token_id).toBeUndefined();
    expect(onOpenChange).toHaveBeenCalledWith(false);
  });

  it("submits create project form with a separate code repository", async () => {
    createProjectMutateAsync.mockResolvedValue({
      data: { id: "proj-99" },
    });

    const user = userEvent.setup();

    renderWithRoute(
      <ProjectFormDialog open={true} onOpenChange={vi.fn()} mode="create" />,
      { path: "/projects", initialEntry: "/projects" },
    );

    await user.type(screen.getByLabelText("项目名称"), "musiver");
    await user.type(
      screen.getByLabelText("仓库链接（可选）"),
      "liuyincs/musiver",
    );
    await user.type(
      screen.getByLabelText("代码仓库（可选，默认同公开仓库）"),
      "gitbobobo/musiver",
    );
    await user.click(screen.getByRole("button", { name: "创建项目" }));

    await waitFor(() =>
      expect(createProjectMutateAsync).toHaveBeenCalledWith({
        name: "musiver",
        repository_url: "liuyincs/musiver",
        code_repository_url: "gitbobobo/musiver",
      }),
    );
  });

  it("edit form prefills the code repository and clearing it sends null", async () => {
    const user = userEvent.setup();

    renderWithRoute(
      <ProjectFormDialog
        open={true}
        onOpenChange={vi.fn()}
        mode="edit"
        projectId="proj-1"
      />,
      { path: "/projects", initialEntry: "/projects" },
    );

    const input = screen.getByLabelText("代码仓库（可选，默认同公开仓库）");
    expect(input).toHaveValue("https://github.com/code-owner/code-repo");

    await user.clear(input);
    await user.click(screen.getByRole("button", { name: "保存" }));

    await waitFor(() => expect(updateProjectMutateAsync).toHaveBeenCalled());
    expect(
      updateProjectMutateAsync.mock.calls[0][0].code_repository_url,
    ).toBeNull();
  });

  it("edit form sends unparseable code repository to the server instead of dropping it", async () => {
    const user = userEvent.setup();

    renderWithRoute(
      <ProjectFormDialog
        open={true}
        onOpenChange={vi.fn()}
        mode="edit"
        projectId="proj-1"
      />,
      { path: "/projects", initialEntry: "/projects" },
    );

    // 宽松解析会得到空 owner/repo：不能当成「未改动」而省略 key（否则服务端
    // 的格式校验被绕过，输入被静默丢弃），必须原样发出去交给服务端报 40001。
    const input = screen.getByLabelText("代码仓库（可选，默认同公开仓库）");
    await user.clear(input);
    await user.type(input, "https://github.com/");
    await user.click(screen.getByRole("button", { name: "保存" }));

    await waitFor(() => expect(updateProjectMutateAsync).toHaveBeenCalled());
    expect(
      updateProjectMutateAsync.mock.calls[0][0].code_repository_url,
    ).toBe("https://github.com/");
  });

  it("edit form leaves code repository out of payload when untouched", async () => {
    const user = userEvent.setup();

    renderWithRoute(
      <ProjectFormDialog
        open={true}
        onOpenChange={vi.fn()}
        mode="edit"
        projectId="proj-1"
      />,
      { path: "/projects", initialEntry: "/projects" },
    );

    await user.click(screen.getByRole("button", { name: "保存" }));

    await waitFor(() => expect(updateProjectMutateAsync).toHaveBeenCalled());
    expect(
      updateProjectMutateAsync.mock.calls[0][0],
    ).not.toHaveProperty("code_repository_url");
  });

  it("edit form treats a rewritten-but-equal code repository as unchanged", async () => {
    const user = userEvent.setup();

    renderWithRoute(
      <ProjectFormDialog
        open={true}
        onOpenChange={vi.fn()}
        mode="edit"
        projectId="proj-1"
      />,
      { path: "/projects", initialEntry: "/projects" },
    );

    // 解析后 owner/repo 相同（大小写、写法不同）不算变更，不产生多余提交。
    const input = screen.getByLabelText("代码仓库（可选，默认同公开仓库）");
    await user.clear(input);
    await user.type(input, "CODE-OWNER/Code-Repo");
    await user.click(screen.getByRole("button", { name: "保存" }));

    await waitFor(() => expect(updateProjectMutateAsync).toHaveBeenCalled());
    expect(
      updateProjectMutateAsync.mock.calls[0][0],
    ).not.toHaveProperty("code_repository_url");
  });

  it("shows owner-aware github token guidance for fine-grained tokens", async () => {
    const user = userEvent.setup();
    const onOpenChange = vi.fn();

    renderWithRoute(
      <ProjectFormDialog
        open={true}
        onOpenChange={onOpenChange}
        mode="edit"
        projectId="proj-1"
      />,
      { path: "/projects", initialEntry: "/projects" },
    );

    await user.click(screen.getByRole("button", { name: "如何获取？" }));

    expect(screen.getAllByText(/Resource owner/).length).toBeGreaterThan(0);
    expect(screen.getByText(/old-owner\/old-repo/)).toBeInTheDocument();
    expect(
      screen.getByText(/Resource not accessible by personal access token/),
    ).toBeInTheDocument();

    const fineGrainedLink = screen.getByRole("link", {
      name: "按当前 Owner 预填 Fine-grained Token",
    });
    expect(fineGrainedLink).toHaveAttribute(
      "href",
      expect.stringContaining("target_name=old-owner"),
    );
    expect(fineGrainedLink).toHaveAttribute(
      "href",
      expect.stringContaining("contents=write"),
    );
  });

  it("submits create version form and navigates to version detail", async () => {
    createVersionMutateAsync.mockResolvedValue({
      data: { id: "ver-99" },
    });

    const user = userEvent.setup();
    const onOpenChange = vi.fn();

    renderWithRoute(
      <VersionFormDialog
        open={true}
        onOpenChange={onOpenChange}
        projectId="proj-1"
      />,
      { path: "/versions", initialEntry: "/versions" },
    );

    await user.type(screen.getByLabelText("版本号"), "v1.2.3");
    await user.type(screen.getByLabelText("Release 说明（可选）"), "release notes");
    await user.click(screen.getByRole("button", { name: "创建版本" }));

    await waitFor(() =>
      expect(createVersionMutateAsync).toHaveBeenCalledWith({
        version_number: "v1.2.3",
        target_commitish: "main",
        release_notes: "release notes",
      }),
    );
    expect(onOpenChange).toHaveBeenCalledWith(false);
    expect(mockNavigate).toHaveBeenCalledWith("/projects/proj-1/versions/ver-99");
  });

  it("keeps dialog open when create version fails", async () => {
    createVersionMutateAsync.mockRejectedValue(new Error("conflict"));

    const user = userEvent.setup();
    const onOpenChange = vi.fn();

    renderWithRoute(
      <VersionFormDialog
        open={true}
        onOpenChange={onOpenChange}
        projectId="proj-1"
      />,
      { path: "/versions", initialEntry: "/versions" },
    );

    await user.type(screen.getByLabelText("版本号"), "v1.2.3");
    await user.click(screen.getByRole("button", { name: "创建版本" }));

    await waitFor(() =>
      expect(toast.error).toHaveBeenCalledWith("创建失败，版本号可能已存在"),
    );
    expect(onOpenChange).not.toHaveBeenCalled();
    expect(mockNavigate).not.toHaveBeenCalled();
  });

  it("closes dialog on cancel without submitting", async () => {
    const user = userEvent.setup();
    const onOpenChange = vi.fn();

    renderWithRoute(
      <VersionFormDialog
        open={true}
        onOpenChange={onOpenChange}
        projectId="proj-1"
      />,
      { path: "/versions", initialEntry: "/versions" },
    );

    await user.type(screen.getByLabelText("版本号"), "v1.2.3");
    await user.click(screen.getByRole("button", { name: "取消" }));

    expect(onOpenChange).toHaveBeenCalledWith(false);
    expect(createVersionMutateAsync).not.toHaveBeenCalled();
    expect(mockNavigate).not.toHaveBeenCalled();
  });
});
