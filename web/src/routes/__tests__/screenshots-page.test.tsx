import { screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { vi } from "vitest";
import ScreenshotsPage from "@/routes/screenshots/index";
import { renderWithRoute } from "@/test/render";
import { useProjects } from "@/lib/hooks/use-projects";
import { useScreenshotScreens } from "@/lib/hooks/use-screenshots";

const mockUploadDialog = vi.fn((_props: unknown) => null);
const mockLightbox = vi.fn((_props: unknown) => null);

vi.mock("@/lib/hooks/use-projects", () => ({
  useProjects: vi.fn(),
}));

vi.mock("@/lib/hooks/use-screenshots", () => ({
  useScreenshotScreens: vi.fn(),
}));

vi.mock("@/components/screenshots/upload-screenshots-dialog", () => ({
  UploadScreenshotsDialog: (props: unknown) => mockUploadDialog(props),
}));

vi.mock("@/components/screenshots/screenshot-lightbox", () => ({
  ScreenshotLightbox: (props: unknown) => mockLightbox(props),
}));

function makeScreen(
  overrides: Partial<ScreenshotScreenListItem>,
): ScreenshotScreenListItem {
  return {
    id: "s-x",
    project_id: "proj-1",
    screen_key: "key",
    title: "",
    group: "",
    version_count: 1,
    last_uploaded_at: "2026-10-01T00:00:00Z",
    created_at: "2026-10-01T00:00:00Z",
    latest_version: null,
    ...overrides,
  };
}

const screensFixture: ScreenshotScreenListItem[] = [
  makeScreen({
    id: "s1",
    screen_key: "home",
    title: "首页",
    group: "核心",
    version_count: 3,
    last_uploaded_at: "2026-10-05T00:00:00Z",
  }),
  makeScreen({
    id: "s2",
    screen_key: "settings",
    group: "核心",
    version_count: 1,
    last_uploaded_at: "2026-10-02T00:00:00Z",
  }),
  makeScreen({
    id: "s3",
    screen_key: "kanban",
    title: "看板",
    group: "辅助",
    version_count: 5,
    last_uploaded_at: "2026-10-07T00:00:00Z",
  }),
  makeScreen({
    id: "s4",
    screen_key: "misc",
    group: "",
    version_count: 2,
    last_uploaded_at: "2026-10-03T00:00:00Z",
  }),
];

function mockProjects(items: Project[] = [
  {
    id: "proj-1",
    user_id: "user-1",
    name: "Alpha App",
    description: "",
    github_owner: "acme",
    github_repo: "alpha",
    has_github_token: false,
    has_github_pr_token: false,
    latest_version: null,
    created_at: "2026-04-06T09:00:00Z",
    updated_at: "2026-04-06T09:00:00Z",
  },
]) {
  vi.mocked(useProjects).mockReturnValue({
    data: { items, total: items.length, page: 1, page_size: 100 },
    isLoading: false,
  } as unknown as ReturnType<typeof useProjects>);
}

function mockScreens(items: ScreenshotScreenListItem[]) {
  vi.mocked(useScreenshotScreens).mockReturnValue({
    data: { items },
    isLoading: false,
  } as unknown as ReturnType<typeof useScreenshotScreens>);
}

describe("ScreenshotsPage", () => {
  beforeEach(() => {
    mockProjects();
    mockScreens(screensFixture);
  });

  afterEach(() => {
    vi.clearAllMocks();
  });

  it("renders group tabs in order: 全部 → groups by latest upload desc → 未分组", () => {
    renderWithRoute(<ScreenshotsPage />, {
      path: "/screenshots",
      initialEntry: "/screenshots",
    });

    const tabs = screen.getAllByRole("tab").map((t) => t.textContent);
    expect(tabs).toEqual(["全部", "辅助", "核心", "未分组"]);
  });

  it("renders screen cards with display name and version count", () => {
    renderWithRoute(<ScreenshotsPage />, {
      path: "/screenshots",
      initialEntry: "/screenshots",
    });

    expect(screen.getByText("首页")).toBeInTheDocument();
    // title 为空时回退显示 screen_key
    expect(screen.getByText("settings")).toBeInTheDocument();
    expect(screen.getByText("5 个版本")).toBeInTheDocument();
  });

  it("filters cards by group tab and ungrouped tab", async () => {
    const user = userEvent.setup();
    renderWithRoute(<ScreenshotsPage />, {
      path: "/screenshots",
      initialEntry: "/screenshots",
    });

    await user.click(screen.getByRole("tab", { name: "核心" }));
    expect(screen.getByText("首页")).toBeInTheDocument();
    expect(screen.getByText("settings")).toBeInTheDocument();
    expect(screen.queryByText("看板")).not.toBeInTheDocument();
    expect(screen.queryByText("misc")).not.toBeInTheDocument();

    await user.click(screen.getByRole("tab", { name: "未分组" }));
    expect(screen.queryByText("首页")).not.toBeInTheDocument();
    expect(screen.getByText("misc")).toBeInTheDocument();
  });

  it("filters cards by search query within the current tab", async () => {
    const user = userEvent.setup();
    renderWithRoute(<ScreenshotsPage />, {
      path: "/screenshots",
      initialEntry: "/screenshots",
    });

    await user.type(
      screen.getByPlaceholderText("搜索名称或界面标识"),
      "kan",
    );
    expect(screen.getByText("看板")).toBeInTheDocument();
    expect(screen.queryByText("首页")).not.toBeInTheDocument();
  });

  it("opens the lightbox on the clicked card with the filtered list", async () => {
    const user = userEvent.setup();
    renderWithRoute(<ScreenshotsPage />, {
      path: "/screenshots",
      initialEntry: "/screenshots",
    });

    await user.click(screen.getByTestId("screenshot-card-s3"));

    expect(mockLightbox).toHaveBeenLastCalledWith(
      expect.objectContaining({
        open: true,
        screenId: "s3",
        projectId: "proj-1",
      }),
    );
    const lastCall = mockLightbox.mock.calls.at(-1)?.[0] as {
      screens: ScreenshotScreenListItem[];
    };
    expect(lastCall.screens.map((s) => s.id)).toEqual([
      "s1",
      "s2",
      "s3",
      "s4",
    ]);
  });

  it("shows the empty-project state when there are no projects", () => {
    mockProjects([]);
    mockScreens([]);

    renderWithRoute(<ScreenshotsPage />, {
      path: "/screenshots",
      initialEntry: "/screenshots",
    });

    expect(screen.getAllByText("暂无项目")).toHaveLength(2);
    expect(
      screen.queryByRole("button", { name: /上传截图/i }),
    ).not.toBeInTheDocument();
  });

  it("shows the empty-screen state for a project without screenshots", () => {
    mockScreens([]);

    renderWithRoute(<ScreenshotsPage />, {
      path: "/screenshots",
      initialEntry: "/screenshots",
    });

    expect(
      screen.getByText(/该项目暂无截图/),
    ).toBeInTheDocument();
    expect(screen.queryByRole("tab")).not.toBeInTheDocument();
  });
});
