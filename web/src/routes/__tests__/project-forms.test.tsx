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

describe("ProjectAndVersionForms", () => {
  const createProjectMutateAsync = vi.fn();
  const updateProjectMutateAsync = vi.fn();
  const createVersionMutateAsync = vi.fn();

  beforeEach(() => {
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

    await user.type(screen.getByLabelText("项目名称"), "fast-ship");
    await user.type(
      screen.getByLabelText("仓库链接（可选）"),
      "https://github.com/godbobo/fast_ship",
    );
    await user.type(screen.getByLabelText("GitHub Access Token"), "ghp_secret");
    await user.click(screen.getByRole("button", { name: "创建项目" }));

    await waitFor(() =>
      expect(createProjectMutateAsync).toHaveBeenCalledWith({
        name: "fast-ship",
        repository_url: "https://github.com/godbobo/fast_ship",
        github_token: "ghp_secret",
      }),
    );
    expect(mockNavigate).toHaveBeenCalledWith("/issues?project=proj-99");
  });

  it("submits create project form with existing project token", async () => {
    vi.mocked(useProjects).mockReturnValue({
      data: {
        items: [
          {
            id: "proj-existing",
            name: "existing-project",
            github_owner: "existing-owner",
            github_repo: "existing-repo",
          },
        ],
        total: 1,
        page: 1,
        page_size: 20,
      },
      isLoading: false,
    } as unknown as ReturnType<typeof useProjects>);

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

    // Open token source select and choose existing project
    await user.click(screen.getByRole("combobox"));
    await user.click(
      screen.getByRole("option", { name: /existing-project/ }),
    );

    await user.click(screen.getByRole("button", { name: "创建项目" }));

    await waitFor(() =>
      expect(createProjectMutateAsync).toHaveBeenCalledWith({
        name: "fast-ship",
        repository_url: "godbobo/fast_ship",
        source_project_id: "proj-existing",
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
    expect(onOpenChange).toHaveBeenCalledWith(false);
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
