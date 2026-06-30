import { screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { vi } from "vitest";
import VersionsPage from "@/routes/versions/index";
import { renderWithRoute } from "@/test/render";
import { useProjects } from "@/lib/hooks/use-projects";
import { useVersions } from "@/lib/hooks/use-versions";

const mockVersionFormDialog = vi.fn((_props: unknown) => null);

vi.mock("@/lib/hooks/use-projects", () => ({
  useProjects: vi.fn(),
}));

vi.mock("@/lib/hooks/use-versions", () => ({
  useVersions: vi.fn(),
}));

vi.mock("@/components/versions/version-form-dialog", () => ({
  VersionFormDialog: (props: unknown) => mockVersionFormDialog(props),
}));

describe("VersionsPage", () => {
  beforeEach(() => {
    vi.mocked(useProjects).mockReturnValue({
      data: {
        items: [
          {
            id: "proj-1",
            user_id: "user-1",
            name: "Alpha App",
            description: "release pipeline",
            github_owner: "acme",
            github_repo: "alpha",
            latest_version: null,
            created_at: "2026-04-06T09:00:00Z",
            updated_at: "2026-04-06T09:00:00Z",
          },
        ],
        total: 1,
        page: 1,
        page_size: 100,
      },
      isLoading: false,
    } as unknown as ReturnType<typeof useProjects>);

    vi.mocked(useVersions).mockReturnValue({
      data: {
        items: [
          {
            id: "ver-1",
            project_id: "proj-1",
            version_number: "v1.2.0",
            status: "pending",
            release_notes: null,
            target_commitish: null,
            github_release_url: null,
            error_log: null,
            ship_status: "",
            ship_stage: "",
            ship_message: null,
            created_at: "2026-04-06T10:00:00Z",
            shipped_at: null,
            artifacts: [],
          },
        ],
        total: 1,
        page: 1,
        page_size: 100,
      },
      isLoading: false,
    } as unknown as ReturnType<typeof useVersions>);
  });

  afterEach(() => {
    vi.clearAllMocks();
  });

  it("uses the first project as the active selection without flashing the empty-project state", () => {
    renderWithRoute(<VersionsPage />, {
      path: "/versions",
      initialEntry: "/versions",
    });

    expect(useVersions).toHaveBeenCalledWith("proj-1");
    expect(screen.queryByText("暂无项目")).not.toBeInTheDocument();
    expect(screen.getByText("v1.2.0")).toBeInTheDocument();
    screen.getByRole("button", { name: /创建版本/i });
    expect(
      screen.queryByRole("link", { name: /创建版本/i }),
    ).not.toBeInTheDocument();
  });

  it("opens create version dialog with the active project id", async () => {
    const user = userEvent.setup();

    renderWithRoute(<VersionsPage />, {
      path: "/versions",
      initialEntry: "/versions",
    });

    await user.click(screen.getByRole("button", { name: /创建版本/i }));

    expect(mockVersionFormDialog).toHaveBeenCalledWith(
      expect.objectContaining({
        open: true,
        projectId: "proj-1",
      }),
    );
  });

  it("shows the empty-project state only when there are truly no projects", () => {
    vi.mocked(useProjects).mockReturnValue({
      data: {
        items: [],
        total: 0,
        page: 1,
        page_size: 100,
      },
      isLoading: false,
    } as unknown as ReturnType<typeof useProjects>);

    vi.mocked(useVersions).mockReturnValue({
      data: undefined,
      isLoading: false,
    } as unknown as ReturnType<typeof useVersions>);

    renderWithRoute(<VersionsPage />, {
      path: "/versions",
      initialEntry: "/versions",
    });

    expect(screen.getAllByText("暂无项目")).toHaveLength(2);
  });
});
