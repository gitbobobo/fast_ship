import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { vi } from "vitest";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { ScreenshotLightbox } from "./screenshot-lightbox";
import { useScreenshotAnnotations } from "@/lib/hooks/use-screenshot-annotations";
import { useDeleteScreenshotScreen } from "@/lib/hooks/use-screenshots";

vi.mock("@/lib/hooks/use-screenshots", async (importOriginal) => {
  const mod =
    await importOriginal<typeof import("@/lib/hooks/use-screenshots")>();
  return {
    ...mod,
    useDeleteScreenshotScreen: vi.fn(),
  };
});

// 标注列表接口不是这些用例关心的内容，默认返回空
vi.mock("@/lib/hooks/use-screenshot-annotations", async (importOriginal) => {
  const mod =
    await importOriginal<
      typeof import("@/lib/hooks/use-screenshot-annotations")
    >();
  return {
    ...mod,
    useScreenshotAnnotations: vi.fn(() => ({ data: [], isFetching: false })),
  };
});

vi.mock("./screenshot-edit-dialog", () => ({
  ScreenshotEditDialog: () => <div data-testid="edit-dialog" />,
}));

function makeScreen(
  overrides: Partial<ScreenshotScreen>,
): ScreenshotScreen {
  return {
    id: "s-x",
    project_id: "p-1",
    screen_key: "home",
    title: "",
    platform: null,
    locale: null,
    theme: null,
    mime_type: "image/png",
    file_size: 1024,
    width: 0,
    height: 0,
    uploaded_by: "bobo",
    uploaded_at: "2026-10-05T06:30:00Z",
    created_at: "2026-10-01T00:00:00Z",
    content_url: "/api/screenshot-screens/s-x/content",
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
    issue_id: null,
    issue_reference: null,
    issue_title: null,
    screen_key: "home",
    screen_title: "首页",
    screen_platform: null,
    screen_locale: null,
    screen_theme: null,
    image_url: "",
    image_width: 0,
    image_height: 0,
    outdated: false,
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

const screens: ScreenshotScreen[] = [
  makeScreen({
    id: "s1",
    screen_key: "home",
    title: "首页",
    platform: "ios",
    locale: "zh-CN",
    theme: "dark",
    content_url: "/api/screenshot-screens/s1/content",
  }),
  makeScreen({
    id: "s2",
    screen_key: "settings",
    content_url: "/api/screenshot-screens/s2/content",
  }),
];

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
    vi.mocked(useDeleteScreenshotScreen).mockReturnValue({
      mutateAsync: vi.fn(),
      isPending: false,
    } as unknown as ReturnType<typeof useDeleteScreenshotScreen>);
  });

  afterEach(() => {
    vi.clearAllMocks();
  });

  it("shows the screen image with dimension badges and file info, no version UI", async () => {
    renderLightbox();

    const img = await screen.findByRole("button", { name: "首页" });
    expect(img.getAttribute("src")).toBe(
      "/api/screenshot-screens/s1/content",
    );
    expect(screen.getByText("ios")).toBeInTheDocument();
    expect(screen.getByText("zh-CN")).toBeInTheDocument();
    expect(screen.getByText("暗色")).toBeInTheDocument();
    // 版本相关 UI 一律不出现
    expect(screen.queryByRole("combobox", { name: "选择版本" })).toBeNull();
    expect(screen.queryByRole("button", { name: "对比" })).toBeNull();
    expect(screen.queryByRole("menuitem", { name: "删除当前版本" })).toBeNull();
  });

  it("navigates screens with ArrowLeft/ArrowRight", async () => {
    const user = userEvent.setup();
    const onNavigate = vi.fn();
    renderLightbox({ onNavigate });

    await screen.findByRole("button", { name: "首页" });
    await user.keyboard("{ArrowRight}");
    expect(onNavigate).toHaveBeenCalledWith("s2");

    onNavigate.mockClear();
    await user.keyboard("{ArrowLeft}");
    // s1 已是第一个，不能继续往前
    expect(onNavigate).not.toHaveBeenCalled();
  });

  it("toggles fit/zoom on click with a dpr-aware width", async () => {
    const user = userEvent.setup();
    renderLightbox();
    const img = (await screen.findByRole("button", {
      name: "首页",
    })) as HTMLImageElement;
    mockImageMetrics(img);

    await user.click(img);
    expect(mainImage().getAttribute("data-zoom")).toBe("zoomed");
    // 放大宽度 = naturalWidth ÷ dpr（happy-dom dpr=1）
    expect(mainImage().style.width).toBe("2400px");

    // 放大态再点回到适配
    mockImageMetrics(mainImage());
    await user.click(mainImage());
    expect(mainImage().getAttribute("data-zoom")).toBe("fit");
  });

  it("exits zoom on the first Escape and closes the dialog on the second", async () => {
    const user = userEvent.setup();
    const onOpenChange = vi.fn();
    renderLightbox({ onOpenChange });
    const img = (await screen.findByRole("button", {
      name: "首页",
    })) as HTMLImageElement;
    mockImageMetrics(img);

    await user.click(img);
    expect(mainImage().getAttribute("data-zoom")).toBe("zoomed");

    await user.keyboard("{Escape}");
    expect(mainImage().getAttribute("data-zoom")).toBe("fit");
    expect(onOpenChange).not.toHaveBeenCalled();

    await user.keyboard("{Escape}");
    // Base UI 会附加事件详情参数，只断言第一个参数
    expect(onOpenChange.mock.calls[0][0]).toBe(false);
  });

  it("opens the edit dialog for the current screen", async () => {
    const user = userEvent.setup();
    renderLightbox();

    await user.click(await screen.findByRole("button", { name: "编辑截图" }));
    expect(screen.getByTestId("edit-dialog")).toBeInTheDocument();
  });

  it("warns about the annotations deleting the screen will remove", async () => {
    vi.mocked(useScreenshotAnnotations).mockReturnValue({
      data: [
        makeAnnotation({ id: "a-1" }),
        makeAnnotation({ id: "a-2", status: "resolved" }),
        makeAnnotation({ id: "a-3", screen_id: "s2" }),
      ],
      isFetching: false,
    } as unknown as ReturnType<typeof useScreenshotAnnotations>);
    const user = userEvent.setup();
    renderLightbox();

    await user.click(await screen.findByRole("button", { name: "更多操作" }));
    await user.click(
      await screen.findByRole("menuitem", { name: "删除截图" }),
    );
    // 本截图两条（含已解决），其中一条未解决；别的截图不计入
    expect(
      await screen.findByText(/将同时删除 2 条标注，其中 1 条未解决/),
    ).toBeInTheDocument();
  });

  it("shows a fallback when the screen is gone", async () => {
    renderLightbox({ screenId: "s-gone" });
    expect(
      await screen.findByText("截图不存在或已被删除"),
    ).toBeInTheDocument();
  });

  it("renders nav zones within the list and does not navigate past the ends", async () => {
    const user = userEvent.setup();
    const onNavigate = vi.fn();
    renderLightbox({ onNavigate, screenId: "s2" });

    await screen.findByRole("button", { name: "settings" });
    // 两侧翻页区始终在（有效 id 范围内）
    expect(
      screen.getByRole("button", { name: "上一个界面" }),
    ).toBeInTheDocument();
    expect(
      screen.getByRole("button", { name: "下一个界面" }),
    ).toBeInTheDocument();
    await user.keyboard("{ArrowRight}");
    expect(onNavigate).not.toHaveBeenCalled();
  });
});
