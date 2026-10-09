import { fireEvent, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { vi } from "vitest";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { ScreenshotLightbox } from "./screenshot-lightbox";
import { useScreenshotAnnotations } from "@/lib/hooks/use-screenshot-annotations";
import {
  useDeleteScreenshotScreen,
  useDeleteScreenshotVersion,
  useScreenshotScreen,
} from "@/lib/hooks/use-screenshots";

// screenshotScreenDetailQueryOptions 是真函数（prefetch 要用），只 mock 三个 hook
vi.mock("@/lib/hooks/use-screenshots", async (importOriginal) => {
  const mod =
    await importOriginal<typeof import("@/lib/hooks/use-screenshots")>();
  return {
    ...mod,
    useScreenshotScreen: vi.fn(),
    useDeleteScreenshotScreen: vi.fn(),
    useDeleteScreenshotVersion: vi.fn(),
  };
});

// 详情接口交给 QueryClient.prefetchQuery 触发的 queryFn 调用，挂起即可
vi.mock("@/lib/api/screenshots", async (importOriginal) => {
  const mod = await importOriginal<typeof import("@/lib/api/screenshots")>();
  return {
    ...mod,
    screenshotApi: {
      ...mod.screenshotApi,
      get: vi.fn(() => new Promise(() => {})),
    },
  };
});

// 标注列表接口不是这些用例关心的内容，固定返回空
vi.mock("@/lib/hooks/use-screenshot-annotations", async (importOriginal) => {
  const mod =
    await importOriginal<
      typeof import("@/lib/hooks/use-screenshot-annotations")
    >();
  return {
    ...mod,
    useScreenshotAnnotations: vi.fn(() => ({ data: [], isSuccess: true })),
  };
});

vi.mock("./screenshot-edit-dialog", () => ({
  ScreenshotEditDialog: () => null,
}));

function makeVersion(
  overrides: Partial<ScreenshotVersion>,
): ScreenshotVersion {
  return {
    id: "v-x",
    screen_id: "s1",
    note: "",
    file_name: "shot.png",
    file_size: 1024,
    mime_type: "image/png",
    uploaded_by: "bobo",
    uploaded_at: "2026-10-05T06:30:00Z",
    content_url: "/api/screenshot-versions/v-x/content",
    width: 0,
    height: 0,
    ...overrides,
  };
}

function makeAnnotation(
  overrides: Partial<ScreenshotAnnotation>,
): ScreenshotAnnotation {
  return {
    id: "a-x",
    project_id: "p-1",
    screen_id: "s1",
    version_id: "v-new",
    issue_id: null,
    issue_reference: null,
    issue_title: null,
    screen_key: "home",
    screen_title: "首页",
    screen_group: "核心",
    image_url: "",
    image_width: 0,
    image_height: 0,
    is_latest_version: true,
    x: 0.1,
    y: 0.1,
    width: 0.2,
    height: 0.2,
    pixel_rect: null,
    body: "按钮对不齐",
    status: "open",
    crop_url: "",
    created_by: "bobo",
    created_at: "2026-10-05T06:30:00Z",
    updated_at: "2026-10-05T06:30:00Z",
    resolved_at: null,
    ...overrides,
  };
}

function makeScreen(
  overrides: Partial<ScreenshotScreenListItem>,
): ScreenshotScreenListItem {
  return {
    id: "s-x",
    project_id: "p-1",
    screen_key: "home",
    title: "",
    group: "",
    version_count: 1,
    last_uploaded_at: "2026-10-05T00:00:00Z",
    created_at: "2026-10-01T00:00:00Z",
    latest_version: null,
    ...overrides,
  };
}

const screens: ScreenshotScreenListItem[] = [
  makeScreen({ id: "s1", screen_key: "home", title: "首页", version_count: 2 }),
  makeScreen({ id: "s2", screen_key: "settings", version_count: 1 }),
];

const detail: ScreenshotScreenDetail = {
  id: "s1",
  project_id: "p-1",
  screen_key: "home",
  title: "首页",
  group: "核心",
  version_count: 2,
  last_uploaded_at: "2026-10-05T06:30:00Z",
  created_at: "2026-10-01T00:00:00Z",
  versions: [
    makeVersion({
      id: "v-new",
      note: "改版后",
      uploaded_at: "2026-10-05T06:30:00Z",
      content_url: "/api/screenshot-versions/v-new/content",
    }),
    makeVersion({
      id: "v-old",
      note: "改版前",
      uploaded_at: "2026-10-01T06:30:00Z",
      content_url: "/api/screenshot-versions/v-old/content",
    }),
  ],
};

const detail2: ScreenshotScreenDetail = {
  ...detail,
  id: "s2",
  screen_key: "settings",
  title: "",
  group: "",
  version_count: 1,
  versions: [
    makeVersion({
      id: "v-s2",
      screen_id: "s2",
      content_url: "/api/screenshot-versions/v-s2/content",
    }),
  ],
};

function mockScreenQuery(
  result: Pick<
    ReturnType<typeof useScreenshotScreen>,
    "data" | "isLoading" | "isFetching" | "isError"
  >,
) {
  vi.mocked(useScreenshotScreen).mockReturnValue(
    result as ReturnType<typeof useScreenshotScreen>,
  );
}

function renderLightbox(
  props: Partial<Parameters<typeof ScreenshotLightbox>[0]> = {},
) {
  const onNavigate = props.onNavigate ?? vi.fn();
  const onOpenChange = props.onOpenChange ?? vi.fn();
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  const tree = (p: typeof props) => (
    <QueryClientProvider client={queryClient}>
      <ScreenshotLightbox
        open
        onOpenChange={onOpenChange}
        projectId="p-1"
        screens={screens}
        screenId="s1"
        onNavigate={onNavigate}
        {...p}
      />
    </QueryClientProvider>
  );
  const utils = render(tree(props));
  return {
    ...utils,
    onNavigate,
    onOpenChange,
    queryClient,
    rerenderLightbox: (p: typeof props) => utils.rerender(tree(p)),
  };
}

function mainImage(): HTMLImageElement {
  const img = document.querySelector<HTMLImageElement>("img[data-zoom]");
  if (!img) throw new Error("lightbox main image not found");
  return img;
}

// happy-dom 无布局：放大需要真实的 naturalWidth 与适配尺寸
function mockImageMetrics(
  img: HTMLImageElement,
  { natural = 2400, fitW = 800, fitH = 600 } = {},
) {
  Object.defineProperty(img, "naturalWidth", {
    value: natural,
    configurable: true,
  });
  img.getBoundingClientRect = () =>
    ({
      left: 100,
      top: 50,
      right: 100 + fitW,
      bottom: 50 + fitH,
      width: fitW,
      height: fitH,
      x: 100,
      y: 50,
      toJSON: () => ({}),
    }) as DOMRect;
}

describe("ScreenshotLightbox", () => {
  beforeEach(() => {
    mockScreenQuery({
      data: detail,
      isLoading: false,
      isFetching: false,
      isError: false,
    });
    vi.mocked(useDeleteScreenshotScreen).mockReturnValue({
      mutateAsync: vi.fn(),
      isPending: false,
    } as unknown as ReturnType<typeof useDeleteScreenshotScreen>);
    vi.mocked(useDeleteScreenshotVersion).mockReturnValue({
      mutateAsync: vi.fn(),
      isPending: false,
    } as unknown as ReturnType<typeof useDeleteScreenshotVersion>);
  });

  afterEach(() => {
    vi.clearAllMocks();
  });

  it("selects initialVersionId when opened from an annotation on an older version", async () => {
    renderLightbox({ initialVersionId: "v-old", focusAnnotationId: "a-1" });

    const trigger = await screen.findByRole("combobox", {
      name: "选择版本",
    });
    expect(trigger).toHaveTextContent("改版前");
    expect(trigger).not.toHaveTextContent("最新 · 改版后");
  });

  it("falls back to the latest version when initialVersionId is unknown", async () => {
    renderLightbox({ initialVersionId: "v-gone" });

    const trigger = await screen.findByRole("combobox", {
      name: "选择版本",
    });
    expect(trigger).toHaveTextContent("最新 · 改版后");
  });

  it("warns about open annotations that deleting a version or screen will remove", async () => {
    vi.mocked(useScreenshotAnnotations).mockReturnValue({
      data: [
        makeAnnotation({ id: "a-1", version_id: "v-new" }),
        makeAnnotation({ id: "a-2", version_id: "v-old" }),
        makeAnnotation({ id: "a-3", version_id: "v-old", status: "resolved" }),
      ],
    } as unknown as ReturnType<typeof useScreenshotAnnotations>);
    const user = userEvent.setup();
    renderLightbox();
    await screen.findByRole("button", { name: "shot.png" });

    await user.click(screen.getByRole("button", { name: "更多操作" }));
    await user.click(
      await screen.findByRole("menuitem", { name: "删除当前版本" }),
    );
    // 当前为最新版本，只有 a-1 挂在其上
    expect(
      await screen.findByText(/将同时删除 1 条未解决标注/),
    ).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "取消" }));

    await user.click(screen.getByRole("button", { name: "更多操作" }));
    await user.click(
      await screen.findByRole("menuitem", { name: "删除界面" }),
    );
    // 整个界面：a-1 + a-2 两条未解决
    expect(
      await screen.findByText(/将同时删除 2 条未解决标注/),
    ).toBeInTheDocument();
  });

  it("version select trigger shows label text, not the uuid", async () => {
    renderLightbox();

    const trigger = await screen.findByRole("combobox", {
      name: "选择版本",
    });
    // 默认选中最新版本：应显示「最新 · 改版后 · 时间」，而不是 version id
    expect(trigger).toHaveTextContent("最新 · 改版后");
    expect(trigger).not.toHaveTextContent("v-new");
  });

  it("navigates screens with ArrowLeft/ArrowRight", async () => {
    const user = userEvent.setup();
    const onNavigate = vi.fn();
    renderLightbox({ onNavigate });

    await user.keyboard("{ArrowRight}");
    expect(onNavigate).toHaveBeenCalledWith("s2");

    onNavigate.mockClear();
    await user.keyboard("{ArrowLeft}");
    // s1 已是第一个，不能继续往前
    expect(onNavigate).not.toHaveBeenCalled();
  });

  it("navigates with arrow keys in compare mode", async () => {
    const user = userEvent.setup();
    const onNavigate = vi.fn();
    renderLightbox({ onNavigate });

    await user.click(await screen.findByRole("button", { name: "对比" }));
    await user.keyboard("{ArrowRight}");
    expect(onNavigate).toHaveBeenCalledWith("s2");
  });

  it("keeps compare mode after navigating to another screen", async () => {
    const user = userEvent.setup();
    const { rerenderLightbox } = renderLightbox();

    await user.click(await screen.findByRole("button", { name: "对比" }));

    // 父组件翻页后传入新 screenId，新详情已就绪
    mockScreenQuery({
      data: detail2,
      isLoading: false,
      isFetching: false,
      isError: false,
    });
    rerenderLightbox({ screenId: "s2" });

    expect(
      await screen.findByRole("button", { name: "退出对比" }),
    ).toBeInTheDocument();
    // 左右两侧各一个版本选择器，仍处在对比模式
    expect(
      screen.getAllByRole("combobox", { name: "选择版本" }),
    ).toHaveLength(2);
  });

  it("renders nav zones even while screen detail is loading", async () => {
    mockScreenQuery({
      data: undefined,
      isLoading: true,
      isFetching: true,
      isError: false,
    });
    renderLightbox();

    expect(
      await screen.findByRole("button", { name: "上一个界面" }),
    ).toBeInTheDocument();
    expect(
      screen.getByRole("button", { name: "下一个界面" }),
    ).toBeInTheDocument();
  });

  it("keeps the previous image with a light loading hint while next screen loads", async () => {
    const { rerenderLightbox } = renderLightbox();
    await screen.findByRole("button", { name: "shot.png" });
    expect(mainImage().src).toContain("v-new");

    // 翻页：新界面详情未返回，仍展示上一张图并叠轻量加载提示
    mockScreenQuery({
      data: undefined,
      isLoading: true,
      isFetching: true,
      isError: false,
    });
    rerenderLightbox({ screenId: "s2" });

    expect(mainImage().src).toContain("v-new");
    expect(await screen.findByText("加载中")).toBeInTheDocument();
    // 旧图回退窗口不混搭旧数据：无文件信息行、无版本缩略图条
    expect(screen.queryByText(/shot\.png/)).toBeNull();
    expect(screen.queryByRole("button", { name: /改版前/ })).toBeNull();
    expect(screen.queryByRole("button", { name: /改版后/ })).toBeNull();
  });

  it("does not fall back to the previous screen when the request fails", async () => {
    const { rerenderLightbox } = renderLightbox();
    await screen.findByRole("button", { name: "shot.png" });

    // 翻到 s2 请求失败：不回退旧图，显示空态（toast+关弹窗由组件 effect 处理）
    mockScreenQuery({
      data: undefined,
      isLoading: false,
      isFetching: false,
      isError: true,
    });
    rerenderLightbox({ screenId: "s2" });

    expect(document.querySelector("img[data-zoom]")).toBeNull();
    expect(screen.getByText("暂无截图版本")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /改版前/ })).toBeNull();
  });

  it("disables screen-level destructive actions while detail is loading", async () => {
    const user = userEvent.setup();
    const { rerenderLightbox } = renderLightbox();
    await screen.findByRole("button", { name: "shot.png" });

    mockScreenQuery({
      data: undefined,
      isLoading: true,
      isFetching: true,
      isError: false,
    });
    rerenderLightbox({ screenId: "s2" });

    await user.click(screen.getByRole("button", { name: "更多操作" }));
    const deleteItem = await screen.findByRole("menuitem", {
      name: "删除界面",
    });
    expect(deleteItem).toHaveAttribute("aria-disabled", "true");
    expect(
      screen.getByRole("menuitem", { name: "删除当前版本" }),
    ).toHaveAttribute("aria-disabled", "true");
  });

  it("dims and disables the nav zone at list ends", async () => {
    mockScreenQuery({
      data: detail2,
      isLoading: false,
      isFetching: false,
      isError: false,
    });
    const { rerenderLightbox } = renderLightbox();

    // s1 是第一个：左侧不可点
    expect(
      await screen.findByRole("button", { name: "上一个界面" }),
    ).toBeDisabled();
    expect(
      screen.getByRole("button", { name: "下一个界面" }),
    ).toBeEnabled();

    rerenderLightbox({ screenId: "s2" });

    // s2 是最后一个：右侧不可点
    expect(
      screen.getByRole("button", { name: "上一个界面" }),
    ).toBeEnabled();
    expect(
      screen.getByRole("button", { name: "下一个界面" }),
    ).toBeDisabled();
  });

  it("prefetches the adjacent screen detail", async () => {
    const prefetch = vi.spyOn(QueryClient.prototype, "prefetchQuery");
    renderLightbox();

    await screen.findByRole("button", { name: "shot.png" });
    expect(prefetch).toHaveBeenCalledWith(
      expect.objectContaining({
        queryKey: ["screenshots", "detail", "s2"],
      }),
    );
    prefetch.mockRestore();
  });

  it("toggles zoom on image click: fit -> zoomed -> fit", async () => {
    const user = userEvent.setup();
    renderLightbox();

    await screen.findByRole("button", { name: "shot.png" });
    const img = mainImage();
    expect(img).toHaveClass("cursor-zoom-in");
    mockImageMetrics(img);

    await user.click(img);

    const zoomed = mainImage();
    expect(zoomed).toHaveClass("cursor-zoom-out");
    // 放大宽度 = max(原始像素 ÷ dpr, 适配宽度 × 2) = max(2400, 1600)
    expect(zoomed).toHaveStyle({ width: "2400px" });

    await user.click(zoomed);
    expect(mainImage()).toHaveClass("cursor-zoom-in");
  });

  it("Escape exits zoom first, then closes the lightbox", async () => {
    const user = userEvent.setup();
    const onOpenChange = vi.fn();
    renderLightbox({ onOpenChange });

    await screen.findByRole("button", { name: "shot.png" });
    mockImageMetrics(mainImage());
    await user.click(mainImage());
    expect(mainImage()).toHaveClass("cursor-zoom-out");

    // 第一次 Esc：只退出放大，不关弹窗
    fireEvent.keyDown(mainImage(), { key: "Escape" });
    expect(mainImage()).toHaveClass("cursor-zoom-in");
    expect(onOpenChange).not.toHaveBeenCalled();

    // 第二次 Esc：交给 Dialog 自己关闭
    fireEvent.keyDown(mainImage(), { key: "Escape" });
    expect(onOpenChange).toHaveBeenCalledWith(false, expect.anything());
  });

  it("treats slow drags with large total displacement as drag, not click", async () => {
    renderLightbox();
    await screen.findByRole("button", { name: "shot.png" });
    const img = mainImage();
    mockImageMetrics(img);

    // 每步只有 3px，但累计 9px > 4px 阈值：松手后的 click 不应切换放大
    fireEvent.pointerDown(img, { clientX: 100, clientY: 100 });
    fireEvent.pointerMove(img, { clientX: 103, clientY: 100 });
    fireEvent.pointerMove(img, { clientX: 106, clientY: 100 });
    fireEvent.pointerMove(img, { clientX: 109, clientY: 100 });
    fireEvent.pointerUp(img, { clientX: 109, clientY: 100 });
    fireEvent.click(img);

    expect(mainImage()).toHaveClass("cursor-zoom-in");
  });

  it("clicking a nav zone while loading still navigates", async () => {
    const user = userEvent.setup();
    const onNavigate = vi.fn();
    mockScreenQuery({
      data: undefined,
      isLoading: true,
      isFetching: true,
      isError: false,
    });
    renderLightbox({ onNavigate });

    await user.click(
      await screen.findByRole("button", { name: "下一个界面" }),
    );
    expect(onNavigate).toHaveBeenCalledWith("s2");
  });

  it("does not show the previous image after reopening on another screen", async () => {
    const { rerenderLightbox } = renderLightbox();
    await screen.findByRole("button", { name: "shot.png" });
    expect(mainImage().src).toContain("v-new");

    // 关闭后重新打开到未缓存的 s2：应显示骨架屏，不残留 s1 的图
    rerenderLightbox({ open: false, screenId: null });
    mockScreenQuery({
      data: undefined,
      isLoading: true,
      isFetching: true,
      isError: false,
    });
    rerenderLightbox({ open: true, screenId: "s2" });

    expect(document.querySelector("img[data-zoom]")).toBeNull();
    expect(screen.getByText("settings")).toBeInTheDocument();
    expect(await screen.findByText("加载中")).toBeInTheDocument();
  });

  it("keeps the user's selected version while the next screen loads", async () => {
    const user = userEvent.setup();
    const { rerenderLightbox } = renderLightbox();

    // 在 s1 选中次新版本
    await user.click(
      await screen.findByRole("button", { name: /改版前/ }),
    );
    expect(mainImage().src).toContain("v-old");

    // 翻到未缓存的 s2：加载期间仍显示 s1 选中的旧版本，不跳回最新
    mockScreenQuery({
      data: undefined,
      isLoading: true,
      isFetching: true,
      isError: false,
    });
    rerenderLightbox({ screenId: "s2" });
    expect(mainImage().src).toContain("v-old");

    // 新详情到达后回退到默认（最新）版本
    mockScreenQuery({
      data: detail2,
      isLoading: false,
      isFetching: false,
      isError: false,
    });
    rerenderLightbox({ screenId: "s2" });
    expect(mainImage().src).toContain("v-s2");
  });
});
