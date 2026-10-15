import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { vi } from "vitest";
import { screenshotAnnotationApi } from "@/lib/api/screenshot-annotations";
import { annotationFrameStyle } from "../annotation-rect";
import { ScreenshotCanvas } from "./screenshot-canvas";

vi.mock("@/lib/api/screenshot-annotations", async (importOriginal) => {
  const mod =
    await importOriginal<typeof import("@/lib/api/screenshot-annotations")>();
  return {
    ...mod,
    screenshotAnnotationApi: {
      ...mod.screenshotAnnotationApi,
      create: vi.fn(),
      batch: vi.fn(),
    },
  };
});

vi.mock("@/lib/hooks/use-issues", () => ({
  useIssues: vi.fn(() => ({ data: { items: [] } })),
}));

function makeScreen(
  id: string,
  overrides: Partial<ScreenshotScreen> = {},
): ScreenshotScreen {
  const uploadedAt = overrides.uploaded_at ?? "2026-10-05T00:00:00Z";
  return {
    id,
    project_id: "p-1",
    screen_key: overrides.screen_key ?? id,
    title: `界面 ${id}`,
    platform: overrides.platform ?? null,
    locale: overrides.locale ?? null,
    theme: overrides.theme ?? null,
    mime_type: "image/png",
    file_size: 1,
    width: 800,
    height: 600,
    uploaded_by: "bobo",
    uploaded_at: uploadedAt,
    created_at: "2026-10-01T00:00:00Z",
    // 与服务端真实返回同形：content_url 带 ?v=<uploaded_at 纳秒值> 修订参数
    content_url:
      overrides.content_url ??
      `/api/screenshot-screens/${id}/content?v=${Date.parse(uploadedAt) * 1000}`,
  };
}

/** 模拟覆盖上传后的刷新数据：uploaded_at 推进，content_url 由它派生变化 */
function overwriteScreen(screen: ScreenshotScreen): ScreenshotScreen {
  const { content_url: _contentUrl, uploaded_at: _uploadedAt, ...rest } =
    screen;
  return makeScreen(screen.id, {
    ...rest,
    uploaded_at: "2026-10-06T00:00:00Z",
  });
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
    screen_key: "s1",
    screen_title: "界面 s1",
    screen_platform: null,
    screen_locale: null,
    screen_theme: null,
    image_url: "",
    image_width: 800,
    image_height: 600,
    outdated: false,
    x: 0.1,
    y: 0.1,
    width: 0.2,
    height: 0.2,
    pixel_rect: null,
    body: "标注文字",
    status: "open",
    crop_url: "/api/screenshot-annotations/a-x/crop",
    created_by: "bobo",
    created_at: "2026-10-05T00:00:00Z",
    updated_at: "2026-10-05T00:00:00Z",
    resolved_at: null,
    ...overrides,
  };
}

const DEFAULT_STATUS_SCOPE = { open: true, resolved: false };

function renderCanvas(
  props: Partial<Parameters<typeof ScreenshotCanvas>[0]> = {},
) {
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  const tree = (
    nextProps: Partial<Parameters<typeof ScreenshotCanvas>[0]>,
  ) => (
    <QueryClientProvider client={queryClient}>
      <ScreenshotCanvas
        projectId="p-1"
        screens={[makeScreen("s1"), makeScreen("s2", { platform: "设置" })]}
        scopeScreenIds={new Set(["s1", "s2"])}
        annotations={[]}
        annotationsReady
        statusScope={DEFAULT_STATUS_SCOPE}
        onStatusScopeChange={vi.fn()}
        scopeFilter={{}}
        openCount={0}
        resetKey="p-1|"
        controlsEnabled
        locateRequest={null}
        onOpenPreview={vi.fn()}
        onLaunchThread={vi.fn()}
        onCopyPrompt={vi.fn()}
        {...props}
        {...nextProps}
      />
    </QueryClientProvider>
  );
  const utils = render(tree({}));
  return {
    ...utils,
    rerenderWith: (nextProps: Partial<Parameters<typeof ScreenshotCanvas>[0]>) =>
      utils.rerender(tree(nextProps)),
  };
}

describe("ScreenshotCanvas", () => {
  // happy-dom 没有布局：给容器一个固定尺寸，视口裁剪才有可见区域
  const originalClientWidth = Object.getOwnPropertyDescriptor(
    HTMLElement.prototype,
    "clientWidth",
  );
  const originalClientHeight = Object.getOwnPropertyDescriptor(
    HTMLElement.prototype,
    "clientHeight",
  );
  beforeAll(() => {
    Object.defineProperty(HTMLElement.prototype, "clientWidth", {
      configurable: true,
      get: () => 1200,
    });
    Object.defineProperty(HTMLElement.prototype, "clientHeight", {
      configurable: true,
      get: () => 800,
    });
  });
  afterAll(() => {
    if (originalClientWidth) {
      Object.defineProperty(HTMLElement.prototype, "clientWidth", originalClientWidth);
    }
    if (originalClientHeight) {
      Object.defineProperty(HTMLElement.prototype, "clientHeight", originalClientHeight);
    }
  });

  it("lays out platform sections and renders a card per screen", () => {
    renderCanvas();

    expect(screen.getByRole("heading", { name: "设置" })).toBeInTheDocument();
    expect(screen.getByRole("heading", { name: "未标注" })).toBeInTheDocument();
    expect(screen.getByTestId("canvas-card-s1")).toBeInTheDocument();
    expect(screen.getByTestId("canvas-card-s2")).toBeInTheDocument();
  });

  it("badges locale/theme and the outdated-open count on the card", () => {
    renderCanvas({
      screens: [
        makeScreen("s1", { locale: "zh-CN", theme: "dark" }),
        makeScreen("s2", { platform: "设置" }),
      ],
      annotations: [makeAnnotation({ id: "a-out", outdated: true })],
    });

    const card = screen.getByTestId("canvas-card-s1");
    expect(within(card).getByText("zh-CN")).toBeInTheDocument();
    expect(within(card).getByText("暗色")).toBeInTheDocument();
    // 覆盖更新后待确认的 open 标注数
    expect(
      within(card).getByTestId("canvas-card-outdated-s1"),
    ).toHaveTextContent("1 条待确认");
  });

  it("uses more than four columns and still renders images when zoomed far out", async () => {
    const user = userEvent.setup();
    const screens = Array.from({ length: 60 }, (_, i) =>
      makeScreen(`m${i}`, {
        uploaded_at: `2026-10-05T00:00:${String(i).padStart(2, "0")}Z`,
      }),
    );
    renderCanvas({ screens });

    const cards = screen.getAllByTestId(/^canvas-card-/);
    const firstTop = (cards[0] as HTMLElement).style.top;
    const firstRow = cards.filter((c) => (c as HTMLElement).style.top === firstTop);
    expect(firstRow.length).toBeGreaterThan(4);

    // 一路缩到 MIN_SCALE=5%（卡片屏幕宽约 16px，远低于旧 48px 降级阈值），
    // 仍要加载图片而不是只画占位
    const zoomOut = screen.getByRole("button", { name: "缩小" });
    const zoomPercent = () =>
      screen.getByTestId("canvas-zoom-percent").textContent;
    for (let i = 0; i < 30 && zoomPercent() !== "5%"; i++) {
      await user.click(zoomOut);
    }
    expect(zoomPercent()).toBe("5%");
    // <img> 分批挂载：深缩下也要等到全部放行，而不是退回占位
    await waitFor(() => {
      for (const card of screen.getAllByTestId(/^canvas-card-/)) {
        expect(card.querySelector("img")).not.toBeNull();
      }
    });

    // 分区标题反向缩放：5% 时屏幕上仍保持可读字号而不是缩没
    const titleWrap = screen
      .getByRole("heading", { name: "未标注" })
      .closest("div")!;
    const counter = Number(
      /scale\(([\d.]+)\)/.exec(titleWrap.style.transform)?.[1],
    );
    expect(counter).toBeGreaterThan(1);
  });

  it("keeps mounted images on a same-scope data refresh", async () => {
    const many = (stamp: number) =>
      Array.from({ length: 60 }, (_, i) =>
        makeScreen(`a${i}`, {
          uploaded_at: `2026-10-0${stamp}T00:00:${String(i).padStart(2, "0")}Z`,
        }),
      );
    const { rerenderWith } = renderCanvas({ screens: many(5) });
    // 等首批之后的批次把 <img> 全部放行
    await waitFor(() => {
      expect(
        document.querySelectorAll("[data-canvas-card] img").length,
      ).toBe(60);
    });

    // 同范围重拉（新数组、同一批 id）不应重置：已加载的图不卸载
    rerenderWith({ screens: many(6) });
    expect(document.querySelectorAll("[data-canvas-card] img").length).toBe(60);
  });

  it("restarts batched image mounting when the scope changes", async () => {
    const many = (prefix: string) =>
      Array.from({ length: 60 }, (_, i) =>
        makeScreen(`${prefix}${i}`, {
          uploaded_at: `2026-10-05T00:00:${String(i).padStart(2, "0")}Z`,
        }),
      );
    const { rerenderWith } = renderCanvas({ screens: many("a") });
    // 等首批之后的批次把 <img> 全部放行
    await waitFor(() => {
      expect(
        document.querySelectorAll("[data-canvas-card] img").length,
      ).toBe(60);
    });

    // 换范围后额度重置：新范围首帧只挂首批 30 张，而不是一次性挂满
    rerenderWith({ screens: many("b"), resetKey: "p-1|platform=ios" });
    expect(document.querySelectorAll("[data-canvas-card] img").length).toBe(30);
  });

  it("keeps batching after a scope change while a release timer is pending", async () => {
    const many = (prefix: string) =>
      Array.from({ length: 60 }, (_, i) =>
        makeScreen(`${prefix}${i}`, {
          uploaded_at: `2026-10-05T00:00:${String(i).padStart(2, "0")}Z`,
        }),
      );
    const { rerenderWith } = renderCanvas({ screens: many("a") });
    // 趁批次计时器在飞（80ms 窗口内）直接切范围：旧计时器作废后
    // 新范围必须照常分批，不能停在首批
    rerenderWith({ screens: many("b"), resetKey: "p-1|platform=ios" });
    expect(
      document.querySelectorAll("[data-canvas-card] img").length,
    ).toBeLessThanOrEqual(30);

    // 新范围的批次照常推进，最终全部放行
    await waitFor(() => {
      expect(
        document.querySelectorAll("[data-canvas-card] img").length,
      ).toBe(60);
    });
    expect(
      screen.getByTestId("canvas-card-b59").querySelector("img"),
    ).not.toBeNull();
  });

  it("swaps the placeholder for the image after load and resets on overwrite", () => {
    const { rerenderWith } = renderCanvas();
    const card = screen.getByTestId("canvas-card-s1");
    expect(card.querySelector("svg.lucide-images")).not.toBeNull();

    fireEvent.load(card.querySelector("img")!);
    expect(card.querySelector("svg.lucide-images")).toBeNull();

    // 覆盖上传换图（content_url 随 uploaded_at 的 ?v= 变化）后重新占位
    rerenderWith({
      screens: [
        overwriteScreen(makeScreen("s1")),
        makeScreen("s2", { platform: "设置" }),
      ],
    });
    expect(
      screen.getByTestId("canvas-card-s1").querySelector("svg.lucide-images"),
    ).not.toBeNull();
  });

  it("has no outer border or radius so it can fill the available space", () => {
    renderCanvas();
    const root = screen.getByTestId("screenshot-canvas");
    expect(root.className).not.toMatch(/\bborder\b/);
    expect(root.className).not.toMatch(/rounded/);
  });

  it("overlays annotations per status scope from the URL filter", () => {
    const { rerenderWith } = renderCanvas({
      annotations: [
        makeAnnotation({ id: "a-open" }),
        makeAnnotation({ id: "a-done", status: "resolved" }),
      ],
    });

    const card = screen.getByTestId("canvas-card-s1");
    expect(within(card).getByTestId("annotation-rect-a-open")).toBeInTheDocument();
    expect(within(card).queryByTestId("annotation-rect-a-done")).toBeNull();

    // 筛选条把 resolved 加入状态范围后已解决标注也叠加
    rerenderWith({ statusScope: { open: true, resolved: true } });
    expect(
      within(card).getByTestId("annotation-rect-a-done"),
    ).toBeInTheDocument();
  });

  it("lists scoped annotations in the panel, open only by default", async () => {
    const onStatusScopeChange = vi.fn();
    const user = userEvent.setup();
    renderCanvas({
      onStatusScopeChange,
      annotations: [
        makeAnnotation({ id: "a-open", body: "未解决的" }),
        makeAnnotation({ id: "a-done", body: "已解决的", status: "resolved" }),
        makeAnnotation({ id: "a-other", screen_id: "gone", body: "别的范围" }),
      ],
    });

    const panel = screen.getByTestId("annotation-panel");
    expect(within(panel).getByText("未解决的")).toBeInTheDocument();
    expect(within(panel).queryByText("已解决的")).toBeNull();
    expect(within(panel).queryByText("别的范围")).toBeNull();

    // 面板「全部」tab 回写状态范围（open + resolved）
    await user.click(within(panel).getByRole("tab", { name: "全部" }));
    expect(onStatusScopeChange).toHaveBeenCalledWith({
      open: true,
      resolved: true,
    });
  });

  it("offers batch confirm/resolve scoped to the current filters", async () => {
    vi.mocked(screenshotAnnotationApi.batch).mockResolvedValue({
      code: 0,
      message: "ok",
      data: { updated: 2 },
    });
    const user = userEvent.setup();
    renderCanvas({
      scopeFilter: { platform: ["ios", "__none__"] },
      openCount: 2,
      annotations: [
        makeAnnotation({ id: "a-out", outdated: true }),
        makeAnnotation({ id: "a-plain" }),
      ],
    });

    const panel = screen.getByTestId("annotation-panel");
    await user.click(within(panel).getByTestId("panel-batch-confirm"));
    await waitFor(() =>
      expect(screenshotAnnotationApi.batch).toHaveBeenCalledWith("p-1", {
        action: "confirm",
        filter: {
          platform: ["ios", "__none__"],
          locale: undefined,
          theme: undefined,
        },
      }),
    );

    await user.click(within(panel).getByTestId("panel-batch-resolve"));
    await waitFor(() =>
      expect(screenshotAnnotationApi.batch).toHaveBeenCalledWith("p-1", {
        action: "resolve",
        filter: {
          platform: ["ios", "__none__"],
          locale: undefined,
          theme: undefined,
        },
      }),
    );
  });

  it("keeps the panel and batch scope at the dimension range while search narrows the canvas", async () => {
    // 搜索把 s2 隐藏出画布：面板列表与批量按钮仍按维度范围（scopeScreenIds）
    // 计，「全部解决」的作用范围与展示数量同源，不会漏掉隐藏截图上的标注
    vi.mocked(screenshotAnnotationApi.batch).mockResolvedValue({
      code: 0,
      message: "ok",
      data: { updated: 2 },
    });
    const user = userEvent.setup();
    renderCanvas({
      screens: [makeScreen("s1")],
      scopeScreenIds: new Set(["s1", "s2"]),
      scopeFilter: { platform: ["ios", "__none__"] },
      openCount: 2,
      annotations: [
        makeAnnotation({ id: "a-visible", screen_id: "s1" }),
        makeAnnotation({ id: "a-hidden", screen_id: "s2" }),
      ],
    });

    // 画布只剩 s1 的卡片
    expect(screen.getByTestId("canvas-card-s1")).toBeInTheDocument();
    expect(screen.queryByTestId("canvas-card-s2")).toBeNull();
    // 画布叠加只画 s1 的标注
    expect(
      within(screen.getByTestId("canvas-card-s1")).getByTestId(
        "annotation-rect-a-visible",
      ),
    ).toBeInTheDocument();

    // 面板两条都在（含搜索隐藏截图上的），计数与批量范围一致
    const panel = screen.getByTestId("annotation-panel");
    expect(
      within(panel).getByTestId("annotation-item-a-visible"),
    ).toBeInTheDocument();
    expect(
      within(panel).getByTestId("annotation-item-a-hidden"),
    ).toBeInTheDocument();

    // 批量解决按维度过滤发出，覆盖面板展示的全集
    await user.click(within(panel).getByTestId("panel-batch-resolve"));
    await waitFor(() =>
      expect(screenshotAnnotationApi.batch).toHaveBeenCalledWith("p-1", {
        action: "resolve",
        filter: {
          platform: ["ios", "__none__"],
          locale: undefined,
          theme: undefined,
        },
      }),
    );
  });

  it("hides the batch row when the scope has no annotations, disabling confirm without outdated", () => {
    // 无标注：批量操作整行不出现
    const { unmount } = renderCanvas();
    const panel = screen.getByTestId("annotation-panel");
    expect(within(panel).queryByTestId("panel-batch-confirm")).toBeNull();
    unmount();

    // 有 open 但无过期：确认保留置灰，标记已解决可用
    renderCanvas({
      annotations: [makeAnnotation({ id: "a-plain" })],
      openCount: 1,
    });
    const panel2 = screen.getByTestId("annotation-panel");
    expect(within(panel2).getByTestId("panel-batch-confirm")).toBeDisabled();
    expect(within(panel2).getByTestId("panel-batch-resolve")).toBeEnabled();
  });

  it("shows an empty hint when the scope has no annotations", () => {
    renderCanvas();
    expect(screen.getByText("暂无标注")).toBeInTheDocument();
  });

  it("locates an annotation on the canvas and highlights it", async () => {
    const user = userEvent.setup();
    renderCanvas({ annotations: [makeAnnotation({ id: "a-open" })] });

    await user.click(screen.getByRole("button", { name: "定位" }));
    expect(screen.getByTestId("annotation-item-a-open")).toHaveClass(
      "ring-1",
    );
    // 定位后面板条目与画布矩形同步闪烁
    expect(screen.getByTestId("annotation-item-a-open")).toHaveClass(
      "animate-pulse",
    );
    expect(screen.getByTestId("annotation-rect-a-open")).toHaveClass(
      "animate-pulse",
    );
  });

  it("locates a deep-linked annotation once data is ready", async () => {
    renderCanvas({
      locateRequest: { annotationId: "a-open", nonce: 1 },
      annotations: [makeAnnotation({ id: "a-open" })],
    });
    await waitFor(() =>
      expect(screen.getByTestId("annotation-item-a-open")).toHaveClass(
        "ring-1",
      ),
    );
  });

  it("keeps annotation chrome at constant screen size while zooming", async () => {
    const user = userEvent.setup();
    renderCanvas({ annotations: [makeAnnotation({ id: "a-open" })] });

    const world = screen.getByTestId("screenshot-canvas").firstElementChild!
      .firstElementChild as HTMLElement;
    const canvasZoom = () =>
      Number(world.style.getPropertyValue("--canvas-zoom"));

    // 皮肤尺寸以 calc(Npx / var(--canvas-zoom)) 表达：矩形几何按比例走，
    // 描边/光圈/角标反算世界尺寸（happy-dom 解析不了 calc+var，直接断言样式产物）
    const style = annotationFrameStyle(makeAnnotation({ id: "a" }));
    expect(style.borderWidth).toBeUndefined();
    expect(String(style.boxShadow)).toContain("var(--canvas-zoom");

    // 描边走 SVG 描边（不占盒模型、不取整设备像素），同样按缩放率反算
    const stroke = screen
      .getByTestId("annotation-rect-a-open")
      .querySelector("svg rect");
    expect(stroke?.getAttribute("style")).toContain("--canvas-zoom");

    // 世界容器写出当前缩放率并与 transform 同曲线过渡：放大后变量变大
    const atFit = canvasZoom();
    await user.click(screen.getByRole("button", { name: "放大" }));
    expect(canvasZoom()).toBeGreaterThan(atFit);
    // 程序化移动带过渡：--canvas-zoom 与 transform 同曲线，动画期间逐帧精确
    expect(world.getAttribute("style")).toContain("--canvas-zoom 320ms");
    await user.click(screen.getByRole("button", { name: "缩小" }));
    expect(canvasZoom()).toBeCloseTo(atFit);
  });

  it("toggles draw mode with the R shortcut but not while typing", async () => {
    const user = userEvent.setup();
    renderCanvas();

    const drawButton = screen.getByRole("button", { name: /框选/ });
    expect(drawButton).toHaveAttribute("aria-pressed", "false");
    await user.keyboard("r");
    expect(drawButton).toHaveAttribute("aria-pressed", "true");
    await user.keyboard("{Escape}");
    expect(drawButton).toHaveAttribute("aria-pressed", "false");
  });

  it("opens the preview from the card hover action", async () => {
    const onOpenPreview = vi.fn();
    const user = userEvent.setup();
    renderCanvas({ onOpenPreview });

    await user.click(
      within(screen.getByTestId("canvas-card-s2")).getByRole("button", {
        name: "打开预览",
      }),
    );
    expect(onOpenPreview).toHaveBeenCalledWith("s2");
  });

  it("draws a rectangle and creates the annotation via the screen API", async () => {
    vi.mocked(screenshotAnnotationApi.create).mockResolvedValue({
      code: 0,
      message: "ok",
      data: makeAnnotation({ id: "a-new" }),
    });
    const user = userEvent.setup();
    renderCanvas();

    await user.keyboard("r");
    const imageArea = screen
      .getByTestId("canvas-card-s1")
      .querySelector("img")!.parentElement!;
    // 图片区在屏幕上 400x300：从 (40,30) 拖到 (240,180) = 比例 0.1,0.1,0.5,0.5
    imageArea.getBoundingClientRect = () =>
      ({
        left: 0,
        top: 0,
        right: 400,
        bottom: 300,
        width: 400,
        height: 300,
        x: 0,
        y: 0,
        toJSON: () => ({}),
      }) as DOMRect;
    fireEvent.pointerDown(imageArea, {
      button: 0,
      clientX: 40,
      clientY: 30,
      pointerId: 1,
    });
    fireEvent.pointerMove(window, { clientX: 240, clientY: 180, pointerId: 1 });
    fireEvent.pointerUp(window, { clientX: 240, clientY: 180, pointerId: 1 });

    const input = await screen.findByRole("textbox", { name: "标注内容" });
    await user.type(input, "间距太小{Enter}");

    await waitFor(() =>
      expect(screenshotAnnotationApi.create).toHaveBeenCalledWith("s1", {
        x: 0.1,
        y: 0.1,
        width: 0.5,
        height: 0.5,
        body: "间距太小",
      }),
    );
  });

  it("ignores drags smaller than 1% of the image", () => {
    renderCanvas();
    fireEvent.keyDown(window, { key: "r" });
    const imageArea = screen
      .getByTestId("canvas-card-s1")
      .querySelector("img")!.parentElement!;
    imageArea.getBoundingClientRect = () =>
      ({ left: 0, top: 0, width: 400, height: 300 }) as DOMRect;
    fireEvent.pointerDown(imageArea, { button: 0, clientX: 40, clientY: 30, pointerId: 1 });
    fireEvent.pointerUp(window, { clientX: 42, clientY: 31, pointerId: 1 });

    expect(screen.queryByRole("textbox", { name: "标注内容" })).toBeNull();
  });

  it("offers the fixed annotation-handling entries with launch disabled at zero open", async () => {
    const onLaunchThread = vi.fn();
    const onCopyPrompt = vi.fn();
    const user = userEvent.setup();
    renderCanvas({ onLaunchThread, onCopyPrompt, openCount: 0 });

    const launch = screen.getByTestId("canvas-launch-thread");
    expect(launch).toBeDisabled();
    await user.click(launch);
    expect(onLaunchThread).not.toHaveBeenCalled();

    await user.click(screen.getByTestId("canvas-copy-prompt"));
    expect(onCopyPrompt).toHaveBeenCalledTimes(1);
  });

  it("enables the launch entry once the scope has open annotations", async () => {
    const onLaunchThread = vi.fn();
    const user = userEvent.setup();
    renderCanvas({ onLaunchThread, openCount: 3 });

    await user.click(screen.getByTestId("canvas-launch-thread"));
    expect(onLaunchThread).toHaveBeenCalledTimes(1);
  });

  it("does not enter space-pan while a button has keyboard focus", () => {
    renderCanvas();
    const container = screen.getByTestId("screenshot-canvas")
      .firstElementChild as HTMLElement;
    const button = screen.getByRole("button", { name: /框选/ });

    fireEvent.keyDown(button, { code: "Space" });
    expect(container.style.cursor).not.toBe("grab");
    fireEvent.keyUp(window, { code: "Space" });

    fireEvent.keyDown(container, { code: "Space" });
    expect(container.style.cursor).toBe("grab");
    fireEvent.keyUp(window, { code: "Space" });
  });

  it("does not zoom on wheel over data-no-pan regions", () => {
    renderCanvas();
    const container = screen.getByTestId("screenshot-canvas")
      .firstElementChild as HTMLElement;
    const world = container.firstElementChild as HTMLElement;
    const before = world.getAttribute("style");

    fireEvent.wheel(screen.getByTestId("canvas-toolbar"), { deltaY: 100 });
    expect(world.getAttribute("style")).toBe(before);

    fireEvent.wheel(container, { deltaY: 100 });
    expect(world.getAttribute("style")).not.toBe(before);
  });

  it("swallows the click that ends a drag, but not a later one after a new pointerdown", () => {
    const onOpenPreview = vi.fn();
    renderCanvas({ onOpenPreview });
    const container = screen.getByTestId("screenshot-canvas")
      .firstElementChild as HTMLElement;
    const previewButton = () =>
      within(screen.getByTestId("canvas-card-s1")).getByRole("button", {
        name: "打开预览",
      });

    // 在空白处拖拽后松手：随之而来的 click（没有新一轮 pointerdown）被吞掉
    fireEvent.pointerDown(container, { button: 0, clientX: 10, clientY: 10, pointerId: 1 });
    fireEvent.pointerMove(window, { clientX: 120, clientY: 120, pointerId: 1 });
    fireEvent.pointerUp(window, { clientX: 120, clientY: 120, pointerId: 1 });
    fireEvent.click(previewButton());
    expect(onOpenPreview).not.toHaveBeenCalled();

    // 下一次点击前先发生了新的 pointerdown：吞点击状态已解除
    fireEvent.pointerDown(container, { button: 0, clientX: 10, clientY: 10, pointerId: 2 });
    fireEvent.pointerMove(window, { clientX: 120, clientY: 120, pointerId: 2 });
    fireEvent.pointerUp(window, { clientX: 120, clientY: 120, pointerId: 2 });
    fireEvent.pointerDown(container, { button: 0, clientX: 10, clientY: 10, pointerId: 3 });
    fireEvent.pointerUp(window, { clientX: 10, clientY: 10, pointerId: 3 });
    fireEvent.click(previewButton());
    expect(onOpenPreview).toHaveBeenCalledWith("s1");
  });

  it("cancels the pending draft when the screen is overwritten", async () => {
    vi.mocked(screenshotAnnotationApi.create).mockClear();
    const utils = renderCanvas();
    fireEvent.keyDown(window, { key: "r" });
    const imageArea = screen
      .getByTestId("canvas-card-s1")
      .querySelector("img")!.parentElement!;
    imageArea.getBoundingClientRect = () =>
      ({ left: 0, top: 0, width: 400, height: 300 }) as DOMRect;
    fireEvent.pointerDown(imageArea, { button: 0, clientX: 40, clientY: 30, pointerId: 1 });
    fireEvent.pointerMove(window, { clientX: 200, clientY: 120, pointerId: 1 });
    fireEvent.pointerUp(window, { clientX: 200, clientY: 120, pointerId: 1 });
    await screen.findByRole("textbox", { name: "标注内容" });

    // 数据刷新把 s1 的文件换成新上传（content_url 随 uploaded_at 的 ?v= 变化）：
    // 画在旧图上的草稿必须作废
    utils.rerenderWith({
      screens: [
        overwriteScreen(makeScreen("s1")),
        makeScreen("s2", { platform: "设置" }),
      ],
    });

    await waitFor(() =>
      expect(
        screen.queryByRole("textbox", { name: "标注内容" }),
      ).toBeNull(),
    );
    expect(screenshotAnnotationApi.create).not.toHaveBeenCalled();
  });
});
