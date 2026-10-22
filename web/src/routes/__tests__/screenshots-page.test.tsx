import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { vi } from "vitest";
import { MemoryRouter, Route, Routes } from "react-router";
import ScreenshotsPage from "@/routes/screenshots/index";
import { useProjects } from "@/lib/hooks/use-projects";
import { useScreenshotScreens } from "@/lib/hooks/use-screenshots";
import { useScreenshotAnnotations } from "@/lib/hooks/use-screenshot-annotations";

const mockUploadDialog = vi.fn((_props: unknown) => null);
const mockLightbox = vi.fn((_props: unknown) => null);
const mockCanvas = vi.fn((_props: unknown) => <div data-testid="canvas-stub" />);
const mockLaunchDialog = vi.fn((_props: unknown) => null);

vi.mock("@/lib/hooks/use-projects", () => ({
  useProjects: vi.fn(),
}));

vi.mock("@/lib/hooks/use-screenshots", () => ({
  useScreenshotScreens: vi.fn(),
}));

// 标注列表接口不是这些用例关心的内容，固定返回空
vi.mock("@/lib/hooks/use-screenshot-annotations", async (importOriginal) => {
  const mod =
    await importOriginal<
      typeof import("@/lib/hooks/use-screenshot-annotations")
    >();
  return {
    ...mod,
    useScreenshotAnnotations: vi.fn(() => ({
      data: [],
      isSuccess: true,
      isFetching: false,
    })),
  };
});

vi.mock("@/components/screenshots/upload-screenshots-dialog", () => ({
  UploadScreenshotsDialog: (props: unknown) => mockUploadDialog(props),
}));

vi.mock("@/components/screenshots/canvas/screenshot-canvas", () => ({
  ScreenshotCanvas: (props: unknown) => mockCanvas(props),
}));

vi.mock("@/components/screenshots/screenshot-lightbox", () => ({
  ScreenshotLightbox: (props: unknown) => mockLightbox(props),
}));

vi.mock("@/components/screenshots/launch-annotation-thread-dialog", () => ({
  LaunchAnnotationThreadDialog: (props: unknown) => mockLaunchDialog(props),
}));

vi.mock("@/lib/hooks/use-notifications", () => ({
  NOTIFICATION_UNREAD_POLL_MS: 30000,
  useNotifications: vi.fn(() => ({ data: [] })),
  useUnreadNotificationCount: vi.fn(() => ({ data: 0 })),
  useMarkNotificationRead: vi.fn(() => ({ mutate: vi.fn(), mutateAsync: vi.fn() })),
  useMarkAllNotificationsRead: vi.fn(() => ({ mutate: vi.fn(), mutateAsync: vi.fn() })),
}));

function makeScreen(
  overrides: Partial<ScreenshotScreen>,
): ScreenshotScreen {
  return {
    id: "s-x",
    project_id: "proj-1",
    screen_key: "key",
    title: "",
    platform: null,
    locale: null,
    theme: null,
    mime_type: "image/png",
    file_size: 1,
    width: 0,
    height: 0,
    uploaded_by: "bobo",
    uploaded_at: "2026-10-01T00:00:00Z",
    created_at: "2026-10-01T00:00:00Z",
    content_url: `/api/screenshot-screens/${overrides.id ?? "s-x"}/content`,
    ...overrides,
  };
}

const screensFixture: ScreenshotScreen[] = [
  makeScreen({
    id: "s1",
    screen_key: "home",
    title: "首页",
    platform: "ios",
    uploaded_at: "2026-10-05T00:00:00Z",
  }),
  makeScreen({
    id: "s2",
    screen_key: "settings",
    platform: "ios",
    uploaded_at: "2026-10-02T00:00:00Z",
  }),
  makeScreen({
    id: "s3",
    screen_key: "kanban",
    title: "看板",
    platform: "web",
    uploaded_at: "2026-10-07T00:00:00Z",
  }),
  makeScreen({
    id: "s4",
    screen_key: "misc",
    platform: null,
    uploaded_at: "2026-10-03T00:00:00Z",
  }),
];

function mockProjects(items: Project[] = [
  {
    id: "proj-1",
    name: "Alpha App",
    description: "",
    github_owner: "acme",
    github_repo: "alpha",
    code_github_owner: "",
    code_github_repo: "",
    public_github_token_id: "",
    code_github_token_id: "",
    created_at: "2026-04-06T09:00:00Z",
    updated_at: "2026-04-06T09:00:00Z",
  },
]) {
  vi.mocked(useProjects).mockReturnValue({
    data: { items, total: items.length, page: 1, page_size: 100 },
    isLoading: false,
  } as unknown as ReturnType<typeof useProjects>);
}

function mockScreens(items: ScreenshotScreen[]) {
  vi.mocked(useScreenshotScreens).mockReturnValue({
    data: { items },
    isLoading: false,
  } as unknown as ReturnType<typeof useScreenshotScreens>);
}

function renderPage(initialEntry = "/screenshots") {
  return render(
    <MemoryRouter initialEntries={[initialEntry]}>
      <Routes>
        <Route path="/screenshots" element={<ScreenshotsPage />} />
      </Routes>
    </MemoryRouter>,
  );
}

function canvasProps() {
  return mockCanvas.mock.calls.at(-1)?.[0] as {
    screens: ScreenshotScreen[];
    resetKey: string;
    locateRequest: { annotationId: string; nonce: number } | null;
    openCount: number;
    scopeFilter: Record<string, string[] | undefined>;
    statusScope: { open: boolean; resolved: boolean };
    onLaunchThread: () => void;
  };
}

describe("ScreenshotsPage", () => {
  beforeEach(() => {
    mockProjects();
    mockScreens(screensFixture);
  });

  afterEach(() => {
    vi.clearAllMocks();
  });

  it("goes straight to the canvas: no list view or view toggle", () => {
    renderPage();

    expect(screen.getByTestId("canvas-stub")).toBeInTheDocument();
    // 列表时代的控件不再出现
    expect(screen.queryByRole("tab")).not.toBeInTheDocument();
    expect(
      screen.queryByRole("button", { name: /列表/ }),
    ).not.toBeInTheDocument();
    expect(
      screen.queryByRole("button", { name: /画布/ }),
    ).not.toBeInTheDocument();
  });

  it("ignores the legacy ?view= param and still lands on the canvas", () => {
    const first = renderPage("/screenshots?view=list");
    expect(screen.getByTestId("canvas-stub")).toBeInTheDocument();
    first.unmount();

    renderPage("/screenshots?view=canvas");
    expect(screen.getByTestId("canvas-stub")).toBeInTheDocument();
  });

  it("defaults the status filter to open without writing it into the URL", async () => {
    renderPage();

    // 默认只看未解决在解码层生效：URL 无 status 参数也拿到 open 范围，
    // 与刷新行为一致；不允许清空到空集（空集 = 隐藏全部标注的歧义）
    await waitFor(() => {
      expect(canvasProps().statusScope).toEqual({ open: true, resolved: false });
    });
    expect(canvasProps().resetKey).not.toContain("status=");

    // 状态多选清空后回落默认 open，而不是 open/resolved 全不选
    const user = userEvent.setup();
    await waitFor(() =>
      expect(screen.getByTestId("filter-状态")).toBeInTheDocument(),
    );
    await user.click(screen.getByTestId("filter-状态"));
    await user.click(await screen.findByRole("menuitemcheckbox", { name: /open/ }));
    await waitFor(() => {
      expect(canvasProps().statusScope).toEqual({ open: true, resolved: false });
    });
  });

  it("keeps both statuses when the filter selects open and resolved", async () => {
    renderPage("/screenshots?status=open,resolved");

    await waitFor(() => {
      expect(canvasProps().statusScope).toEqual({ open: true, resolved: true });
    });
  });

  it("matches URL locale=en-us against stored en-US for canvas and batch scope", async () => {
    mockScreens([
      makeScreen({
        id: "en",
        screen_key: "home",
        locale: "en-US",
        uploaded_at: "2026-10-05T00:00:00Z",
      }),
      makeScreen({
        id: "zh",
        screen_key: "settings",
        locale: "zh-CN",
        uploaded_at: "2026-10-04T00:00:00Z",
      }),
    ]);
    renderPage("/screenshots?locale=en-us");

    await waitFor(() => {
      expect(canvasProps().screens.map((s) => s.id)).toEqual(["en"]);
    });
    // 画布、面板、批量 payload 同源：过滤值已按服务端口径折成 en-US
    expect(canvasProps().scopeFilter.locale).toEqual(["en-US"]);
  });

  it("matches URL platform=WEB against stored web via lowercase folding", async () => {
    mockScreens([
      makeScreen({
        id: "web",
        screen_key: "home",
        platform: "web",
        uploaded_at: "2026-10-05T00:00:00Z",
      }),
      makeScreen({
        id: "ios",
        screen_key: "settings",
        platform: "ios",
        uploaded_at: "2026-10-04T00:00:00Z",
      }),
    ]);
    renderPage("/screenshots?platform=WEB");

    await waitFor(() => {
      expect(canvasProps().screens.map((s) => s.id)).toEqual(["web"]);
    });
    // 画布、面板、批量 payload 同源：过滤值已按服务端口径折成小写
    expect(canvasProps().scopeFilter.platform).toEqual(["web"]);
  });

  it("matches URL theme=__NONE__ against untagged screens like the server", async () => {
    mockScreens([
      makeScreen({
        id: "tagged",
        screen_key: "home",
        theme: "dark",
        uploaded_at: "2026-10-05T00:00:00Z",
      }),
      makeScreen({
        id: "untagged",
        screen_key: "settings",
        uploaded_at: "2026-10-04T00:00:00Z",
      }),
    ]);
    renderPage("/screenshots?theme=__NONE__");

    await waitFor(() => {
      expect(canvasProps().screens.map((s) => s.id)).toEqual(["untagged"]);
    });
    // 画布、面板、批量 payload 同源：哨兵按服务端 EqualFold 口径归位
    expect(canvasProps().scopeFilter.theme).toEqual(["__none__"]);
  });

  it("filters the canvas by dimension selections and encodes them in the URL", async () => {
    const user = userEvent.setup();
    renderPage();

    await waitFor(() =>
      expect(screen.getByTestId("filter-平台")).toBeInTheDocument(),
    );
    await user.click(screen.getByTestId("filter-平台"));
    await user.click(await screen.findByRole("menuitemcheckbox", { name: /ios/ }));

    await waitFor(() => {
      expect(canvasProps().resetKey).toContain("platform=ios");
    });
    expect(canvasProps().screens.map((s) => s.id)).toEqual(["s1", "s2"]);

    // 多值（含未标注桶）：逗号编码进同一参数（URLSearchParams 会转义逗号）
    await user.click(await screen.findByRole("menuitemcheckbox", { name: /未标注/ }));
    await waitFor(() => {
      expect(decodeURIComponent(canvasProps().resetKey)).toContain(
        "platform=ios,__none__",
      );
    });
    expect(canvasProps().screens.map((s) => s.id)).toEqual(["s1", "s2", "s4"]);
  });

  it("narrows the canvas to screenshots matching the search box", async () => {
    const user = userEvent.setup();
    renderPage();

    const searchBox = screen.getByTestId("screenshot-search");
    // 标题命中
    await user.type(searchBox, "看板");
    await waitFor(() => {
      expect(canvasProps().screens.map((s) => s.id)).toEqual(["s3"]);
    });

    // screen_key 大小写不敏感命中
    await user.clear(searchBox);
    await user.type(searchBox, "SETT");
    await waitFor(() => {
      expect(canvasProps().screens.map((s) => s.id)).toEqual(["s2"]);
    });
  });

  it("stacks the search box on top of the dimension filters", async () => {
    const user = userEvent.setup();
    renderPage();

    await waitFor(() =>
      expect(screen.getByTestId("filter-平台")).toBeInTheDocument(),
    );
    await user.click(screen.getByTestId("filter-平台"));
    await user.click(await screen.findByRole("menuitemcheckbox", { name: /ios/ }));
    await waitFor(() => {
      expect(canvasProps().screens.map((s) => s.id)).toEqual(["s1", "s2"]);
    });

    // 平台=ios 范围内再搜 screen_key，只剩交集
    await user.type(screen.getByTestId("screenshot-search"), "home");
    await waitFor(() => {
      expect(canvasProps().screens.map((s) => s.id)).toEqual(["s1"]);
    });
  });

  it("shows the search empty state when nothing matches", async () => {
    const user = userEvent.setup();
    renderPage();

    await user.type(screen.getByTestId("screenshot-search"), "不存在的界面");
    await waitFor(() => {
      expect(
        screen.getByText("没有匹配的截图，调整搜索关键词后再试"),
      ).toBeInTheDocument();
    });
    expect(screen.queryByTestId("canvas-stub")).not.toBeInTheDocument();
  });

  it("shows the empty state with a center upload button for a project without screenshots", async () => {
    mockScreens([]);
    const user = userEvent.setup();
    renderPage();

    const uploadButtons = await screen.findAllByRole("button", {
      name: /上传截图/,
    });
    // 顶栏一个 + 空状态画布中央一个
    expect(uploadButtons.length).toBe(2);
    expect(screen.queryByTestId("canvas-stub")).not.toBeInTheDocument();

    await user.click(uploadButtons[1]);
    await waitFor(() => {
      expect(
        (mockUploadDialog.mock.calls.at(-1)?.[0] as { open: boolean }).open,
      ).toBe(true);
    });
  });

  it("waits for the in-flight fetch before honoring ?annotation= deep links", async () => {
    const annotation = {
      id: "a-1",
      screen_id: "s1",
      status: "open",
    } as ScreenshotAnnotation;
    const queryState = {
      current: {
        data: [] as ScreenshotAnnotation[],
        isSuccess: true,
        isFetching: true,
      },
    };
    vi.mocked(useScreenshotAnnotations).mockImplementation(
      () =>
        queryState.current as unknown as ReturnType<
          typeof useScreenshotAnnotations
        >,
    );

    const utils = renderPage("/screenshots?view=canvas&annotation=a-1");

    await waitFor(() =>
      expect(screen.getByTestId("canvas-stub")).toBeInTheDocument(),
    );
    // 拉取未落地时不判存在性、不发定位请求
    expect(canvasProps().locateRequest).toBeNull();

    queryState.current = {
      data: [annotation],
      isSuccess: true,
      isFetching: false,
    };
    utils.rerender(
      <MemoryRouter initialEntries={["/screenshots?view=canvas&annotation=a-1"]}>
        <Routes>
          <Route path="/screenshots" element={<ScreenshotsPage />} />
        </Routes>
      </MemoryRouter>,
    );

    await waitFor(() => {
      expect(canvasProps().locateRequest?.annotationId).toBe("a-1");
    });
  });

  it("relaxes dimension filters that hide the deep-linked annotation's screen", async () => {
    const user = userEvent.setup();
    renderPage();

    // 先筛出 web 平台（s1 被滤掉）
    await waitFor(() =>
      expect(screen.getByTestId("filter-平台")).toBeInTheDocument(),
    );
    await user.click(screen.getByTestId("filter-平台"));
    await user.click(await screen.findByRole("menuitemcheckbox", { name: /web/ }));
    await waitFor(() => {
      expect(canvasProps().screens.map((s) => s.id)).toEqual(["s3"]);
    });

    // 深链指向 s1 上的标注：自动放宽平台过滤并定位
    vi.mocked(useScreenshotAnnotations).mockReturnValue({
      data: [
        {
          id: "a-1",
          screen_id: "s1",
          status: "open",
        } as ScreenshotAnnotation,
      ],
      isSuccess: true,
      isFetching: false,
    } as unknown as ReturnType<typeof useScreenshotAnnotations>);

    renderPage("/screenshots?platform=web&annotation=a-1");
    await waitFor(() => {
      expect(canvasProps().locateRequest?.annotationId).toBe("a-1");
    });
    // 放宽后画布恢复全量
    expect(canvasProps().screens.map((s) => s.id).sort()).toEqual([
      "s1",
      "s2",
      "s3",
      "s4",
    ]);
  });

  it("relaxes every hiding dimension and the status filter in one URL write", async () => {
    // 深链指向 s4（全维度未标注）上的已解决标注：platform=web + locale + theme
    // + status 四个放宽合并进同一次写入——逐项多次 setSearchParams 会互相覆盖，
    // 最后一次放宽赢，其余维度仍把目标挡在外面
    vi.mocked(useScreenshotAnnotations).mockReturnValue({
      data: [
        {
          id: "a-1",
          screen_id: "s4",
          status: "resolved",
        } as ScreenshotAnnotation,
      ],
      isSuccess: true,
      isFetching: false,
    } as unknown as ReturnType<typeof useScreenshotAnnotations>);
    renderPage(
      "/screenshots?platform=web&locale=fr-FR&theme=dark&status=open&annotation=a-1",
    );

    await waitFor(() => {
      expect(canvasProps().locateRequest?.annotationId).toBe("a-1");
    });
    const props = canvasProps();
    // 所有遮挡维度都放宽，画布恢复全量
    expect(props.screens.map((s) => s.id).sort()).toEqual([
      "s1",
      "s2",
      "s3",
      "s4",
    ]);
    // 目标状态并入筛选范围：resolved 也在面板范围内
    expect(props.statusScope).toEqual({ open: true, resolved: true });
  });

  it("relaxes only the status filter for a resolved deep link inside current dimensions", async () => {
    // s3 是 web 平台：维度已命中，唯一遮挡是默认 status=open
    vi.mocked(useScreenshotAnnotations).mockReturnValue({
      data: [
        {
          id: "a-3",
          screen_id: "s3",
          status: "resolved",
        } as ScreenshotAnnotation,
      ],
      isSuccess: true,
      isFetching: false,
    } as unknown as ReturnType<typeof useScreenshotAnnotations>);
    renderPage("/screenshots?platform=web&annotation=a-3");

    await waitFor(() => {
      expect(canvasProps().locateRequest?.annotationId).toBe("a-3");
    });
    const props = canvasProps();
    expect(props.screens.map((s) => s.id)).toEqual(["s3"]);
    expect(props.statusScope).toEqual({ open: true, resolved: true });
  });

  it("passes the prompt scope counts and opens the launch dialog from the canvas", async () => {
    vi.mocked(useScreenshotAnnotations).mockReturnValue({
      data: [
        { id: "a-1", screen_id: "s1", status: "open" } as ScreenshotAnnotation,
        {
          id: "a-2",
          screen_id: "s2",
          status: "resolved",
        } as ScreenshotAnnotation,
      ],
      isSuccess: true,
      isFetching: false,
    } as unknown as ReturnType<typeof useScreenshotAnnotations>);
    const user = userEvent.setup();
    renderPage();

    await waitFor(() => {
      expect(canvasProps().openCount).toBe(1);
    });
    expect(canvasProps().scopeFilter).toEqual({
      platform: [],
      locale: [],
      theme: [],
    });

    await user.click(screen.getByTestId("canvas-stub"));
    void user;
    // 通过画布 props 触发启动入口
    canvasProps().onLaunchThread();
    await waitFor(() => {
      expect(
        (mockLaunchDialog.mock.calls.at(-1)?.[0] as { open: boolean }).open,
      ).toBe(true);
    });
    const launchProps = mockLaunchDialog.mock.calls.at(-1)?.[0] as {
      openCount: number;
      promptText: string;
    };
    expect(launchProps.openCount).toBe(1);
    // 提示词携带项目 ID 与过滤口径（空过滤 = 仅 status=open）
    expect(launchProps.promptText).toContain("项目ID：proj-1");
    expect(launchProps.promptText).toContain("status=open");
  });

  it("disables the launch scope when the status filter is resolved-only", async () => {
    vi.mocked(useScreenshotAnnotations).mockReturnValue({
      data: [
        { id: "a-1", screen_id: "s1", status: "open" } as ScreenshotAnnotation,
        {
          id: "a-2",
          screen_id: "s2",
          status: "resolved",
        } as ScreenshotAnnotation,
      ],
      isSuccess: true,
      isFetching: false,
    } as unknown as ReturnType<typeof useScreenshotAnnotations>);
    renderPage("/screenshots?status=resolved");

    // resolved-only 时范围内的 open 为 0：入口置灰，提示词不指向范围外标注
    await waitFor(() => {
      expect(canvasProps().openCount).toBe(0);
    });
    const launchProps = mockLaunchDialog.mock.calls.at(-1)?.[0] as
      | { openCount: number }
      | undefined;
    if (launchProps) {
      expect(launchProps.openCount).toBe(0);
    }
  });
});
