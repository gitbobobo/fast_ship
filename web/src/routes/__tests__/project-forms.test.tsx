import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { vi } from "vitest";
import NewProjectPage from "@/routes/projects/new";
import EditProjectPage from "@/routes/projects/$id/edit";
import NewVersionPage from "@/routes/projects/$id/versions/new";
import { renderWithRoute } from "@/test/render";
import {
  useCreateProject,
  useProject,
  useUpdateProject,
} from "@/lib/hooks/use-projects";
import { useCreateVersion } from "@/lib/hooks/use-versions";

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
  useUpdateProject: vi.fn(),
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

    renderWithRoute(<NewProjectPage />, {
      path: "/projects/new",
      initialEntry: "/projects/new",
    });

    await user.type(screen.getByLabelText("项目名称"), "fast-ship");
    await user.type(screen.getByLabelText("项目描述（可选）"), "release manager");
    await user.type(screen.getByLabelText("GitHub Owner"), "godbobo");
    await user.type(screen.getByLabelText("GitHub Repo"), "fast_ship");
    await user.type(screen.getByLabelText("GitHub Access Token"), "ghp_secret");
    await user.click(screen.getByRole("button", { name: "创建项目" }));

    await waitFor(() =>
      expect(createProjectMutateAsync).toHaveBeenCalledWith({
        name: "fast-ship",
        description: "release manager",
        github_owner: "godbobo",
        github_repo: "fast_ship",
        github_token: "ghp_secret",
      }),
    );
    expect(mockNavigate).toHaveBeenCalledWith("/projects/proj-99");
  });

  it("submits edit project form without sending github token when left empty", async () => {
    const user = userEvent.setup();

    renderWithRoute(<EditProjectPage />, {
      path: "/projects/:id/edit",
      initialEntry: "/projects/proj-1/edit",
    });

    const nameInput = screen.getByLabelText("项目名称");
    const descriptionInput = screen.getByLabelText("项目描述（可选）");
    const ownerInput = screen.getByLabelText("GitHub Owner");
    const repoInput = screen.getByLabelText("GitHub Repo");

    expect(nameInput).toHaveValue("fast-ship");
    expect(descriptionInput).toHaveValue("old desc");
    expect(ownerInput).toHaveValue("old-owner");
    expect(repoInput).toHaveValue("old-repo");

    await user.clear(descriptionInput);
    await user.type(descriptionInput, "new desc");
    await user.clear(ownerInput);
    await user.type(ownerInput, "new-owner");
    await user.clear(repoInput);
    await user.type(repoInput, "new-repo");
    await user.click(screen.getByRole("button", { name: "保存" }));

    await waitFor(() =>
      expect(updateProjectMutateAsync).toHaveBeenCalledWith({
        name: "fast-ship",
        description: "new desc",
        github_owner: "new-owner",
        github_repo: "new-repo",
      }),
    );
    expect(mockNavigate).toHaveBeenCalledWith("/projects/proj-1");
  });

  it("shows owner-aware github token guidance for fine-grained tokens", async () => {
    const user = userEvent.setup();

    renderWithRoute(<EditProjectPage />, {
      path: "/projects/:id/edit",
      initialEntry: "/projects/proj-1/edit",
    });

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

    renderWithRoute(<NewVersionPage />, {
      path: "/projects/:id/versions/new",
      initialEntry: "/projects/proj-1/versions/new",
    });

    await user.type(screen.getByLabelText("版本号"), "v1.2.3");
    await user.type(screen.getByLabelText("目标分支 / Commit（可选）"), "main");
    await user.type(screen.getByLabelText("Release 说明（可选）"), "release notes");
    await user.click(screen.getByRole("button", { name: "创建版本" }));

    await waitFor(() =>
      expect(createVersionMutateAsync).toHaveBeenCalledWith({
        version_number: "v1.2.3",
        target_commitish: "main",
        release_notes: "release notes",
      }),
    );
    expect(mockNavigate).toHaveBeenCalledWith("/projects/proj-1/versions/ver-99");
  });
});
