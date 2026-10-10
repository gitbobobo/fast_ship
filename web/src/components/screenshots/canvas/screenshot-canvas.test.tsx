import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { vi } from "vitest";
import { screenshotAnnotationApi } from "@/lib/api/screenshot-annotations";
import { ScreenshotCanvas } from "./screenshot-canvas";

vi.mock("@/lib/api/screenshot-annotations", async (importOriginal) => {
  const mod =
    await importOriginal<typeof import("@/lib/api/screenshot-annotations")>();
  return {
    ...mod,
    screenshotAnnotationApi: {
      ...mod.screenshotAnnotationApi,
      create: vi.fn(),
    },
  };
});

vi.mock("@/lib/hooks/use-issues", () => ({
  useIssues: vi.fn(() => ({ data: { items: [] } })),
}));

function makeVersion(id: string, screenId: string): ScreenshotVersion {
  return {
    id,
    screen_id: screenId,
    note: "",
    file_name: `${id}.png`,
    file_size: 1,
    mime_type: "image/png",
    uploaded_by: "bobo",
    uploaded_at: "2026-10-05T00:00:00Z",
    content_url: `/api/screenshot-versions/${id}/content`,
    width: 800,
    height: 600,
  };
}

function makeScreen(
  id: string,
  overrides: Partial<ScreenshotScreenListItem> = {},
): ScreenshotScreenListItem {
  return {
    id,
    project_id: "p-1",
    screen_key: id,
    title: `界面 ${id}`,
    group: "",
    version_count: 2,
    last_uploaded_at: "2026-10-05T00:00:00Z",
    created_at: "2026-10-01T00:00:00Z",
    latest_version: makeVersion(`${id}-v2`, id),
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
    version_id: "s1-v2",
    issue_id: null,
    issue_reference: null,
    issue_title: null,
    screen_key: "s1",
    screen_title: "界面 s1",
    screen_group: "",
    image_url: "",
    image_width: 800,
    image_height: 600,
    is_latest_version: true,
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
        screens={[makeScreen("s1"), makeScreen("s2", { group: "设置" })]}
        annotations={[]}
        annotationsReady
        resetKey="p-1|__all__"
        controlsEnabled
        locateRequest={null}
        onOpenPreview={vi.fn()}
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

  it("lays out group titles and renders a card per screen", () => {
    renderCanvas();

    expect(screen.getByRole("heading", { name: "设置" })).toBeInTheDocument();
    expect(screen.getByRole("heading", { name: "未分组" })).toBeInTheDocument();
    expect(screen.getByTestId("canvas-card-s1")).toBeInTheDocument();
    expect(screen.getByTestId("canvas-card-s2")).toBeInTheDocument();
  });

  it("uses more than four columns and still renders images when zoomed far out", async () => {
    const user = userEvent.setup();
    const screens = Array.from({ length: 60 }, (_, i) =>
      makeScreen(`m${i}`, {
        last_uploaded_at: `2026-10-05T00:00:${String(i).padStart(2, "0")}Z`,
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
  });

  it("swaps the placeholder for the image after load and resets on version change", () => {
    const { rerenderWith } = renderCanvas();
    const card = screen.getByTestId("canvas-card-s1");
    expect(card.querySelector("svg.lucide-images")).not.toBeNull();

    fireEvent.load(card.querySelector("img")!);
    expect(card.querySelector("svg.lucide-images")).toBeNull();

    rerenderWith({
      screens: [
        makeScreen("s1", { latest_version: makeVersion("s1-v3", "s1") }),
        makeScreen("s2", { group: "设置" }),
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

  it("overlays open annotations on the latest version and hides resolved by default", async () => {
    const user = userEvent.setup();
    renderCanvas({
      annotations: [
        makeAnnotation({ id: "a-open" }),
        makeAnnotation({ id: "a-done", status: "resolved" }),
        // 挂在旧版本上的不叠加在画布卡片上
        makeAnnotation({ id: "a-old", version_id: "s1-v1" }),
      ],
    });

    const card = screen.getByTestId("canvas-card-s1");
    expect(within(card).getByTestId("annotation-rect-a-open")).toBeInTheDocument();
    expect(within(card).queryByTestId("annotation-rect-a-done")).toBeNull();
    expect(within(card).queryByTestId("annotation-rect-a-old")).toBeNull();
    // 上一版有未解决标注的提示
    expect(within(card).getByText("上一版有 1 条未解决")).toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: /已解决/, pressed: false }));
    expect(within(card).getByTestId("annotation-rect-a-done")).toBeInTheDocument();
  });

  it("lists scoped annotations in the panel, open only by default", async () => {
    const user = userEvent.setup();
    renderCanvas({
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

    await user.click(within(panel).getByRole("tab", { name: "全部" }));
    expect(within(panel).getByText("已解决的")).toBeInTheDocument();
  });

  it("shows an empty hint when the scope has no annotations", () => {
    renderCanvas();
    expect(screen.getByText("暂无标注")).toBeInTheDocument();
  });

  it("locates an annotation on an older version through the preview dialog", async () => {
    const onOpenPreview = vi.fn();
    const user = userEvent.setup();
    renderCanvas({
      onOpenPreview,
      annotations: [makeAnnotation({ id: "a-old", version_id: "s1-v1" })],
    });

    await user.click(screen.getByRole("button", { name: "定位" }));
    expect(onOpenPreview).toHaveBeenCalledWith("s1", "s1-v1", "a-old");
  });

  it("locates a latest-version annotation on the canvas and highlights it", async () => {
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

  it("draws a rectangle on the latest version and creates the annotation via the API", async () => {
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
      expect(screenshotAnnotationApi.create).toHaveBeenCalledWith("s1-v2", {
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

  it("switches the panel filter to all when locating a resolved annotation", async () => {
    const utils = renderCanvas({
      annotations: [
        makeAnnotation({ id: "a-done", body: "已解决的", status: "resolved" }),
      ],
    });

    const panel = screen.getByTestId("annotation-panel");
    expect(within(panel).queryByText("已解决的")).toBeNull();

    utils.rerenderWith({ locateRequest: { annotationId: "a-done", nonce: 1 } });
    await waitFor(() =>
      expect(within(panel).getByText("已解决的")).toBeInTheDocument(),
    );
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
    const before = world.style.transform;

    fireEvent.wheel(screen.getByTestId("canvas-toolbar"), { deltaY: 100 });
    expect(world.style.transform).toBe(before);

    fireEvent.wheel(container, { deltaY: 100 });
    expect(world.style.transform).not.toBe(before);
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

  it("cancels the pending draft when the screen's latest version changes", async () => {
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

    // 数据刷新把 s1 的最新版本换成 v3：画在 v2 上的草稿必须作废
    utils.rerenderWith({
      screens: [
        makeScreen("s1", { latest_version: makeVersion("s1-v3", "s1") }),
        makeScreen("s2", { group: "设置" }),
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
