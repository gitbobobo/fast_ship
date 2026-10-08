import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { vi } from "vitest";
import { ScreenshotLightbox } from "./screenshot-lightbox";
import {
  useDeleteScreenshotScreen,
  useDeleteScreenshotVersion,
  useScreenshotScreen,
} from "@/lib/hooks/use-screenshots";

vi.mock("@/lib/hooks/use-screenshots", () => ({
  useScreenshotScreen: vi.fn(),
  useDeleteScreenshotScreen: vi.fn(),
  useDeleteScreenshotVersion: vi.fn(),
}));

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

function renderLightbox(props: Partial<Parameters<typeof ScreenshotLightbox>[0]> = {}) {
  const onNavigate = props.onNavigate ?? vi.fn();
  const onOpenChange = props.onOpenChange ?? vi.fn();
  render(
    <ScreenshotLightbox
      open
      onOpenChange={onOpenChange}
      projectId="p-1"
      screens={screens}
      screenId="s1"
      onNavigate={onNavigate}
      {...props}
    />,
  );
  return { onNavigate, onOpenChange };
}

describe("ScreenshotLightbox", () => {
  beforeEach(() => {
    vi.mocked(useScreenshotScreen).mockReturnValue({
      data: detail,
      isLoading: false,
      isError: false,
    } as unknown as ReturnType<typeof useScreenshotScreen>);
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
});
