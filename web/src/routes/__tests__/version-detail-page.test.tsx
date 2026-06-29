import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { vi } from "vitest";
import { toast } from "sonner";
import VersionDetailPage from "@/routes/projects/$id/versions/$vid";
import { renderWithRoute } from "@/test/render";
import {
  useDeleteVersion,
  useShipCheck,
  useShipVersion,
  useUpdateVersion,
  useVersion,
} from "@/lib/hooks/use-versions";
import { useProject, useProjectBranches } from "@/lib/hooks/use-projects";
import { useDeleteArtifact, useUploadArtifact } from "@/lib/hooks/use-artifacts";
import { artifactApi } from "@/lib/api/artifacts";
import { downloadAllArtifacts } from "@/lib/utils/download-artifacts";

vi.mock("sonner", () => ({
  toast: {
    success: vi.fn(),
    error: vi.fn(),
    info: vi.fn(),
  },
}));

vi.mock("@/lib/utils/download-artifacts", () => ({
  downloadAllArtifacts: vi.fn().mockResolvedValue(undefined),
}));

vi.mock("@/lib/hooks/use-versions", () => ({
  useVersion: vi.fn(),
  useUpdateVersion: vi.fn(),
  useDeleteVersion: vi.fn(),
  useShipVersion: vi.fn(),
  useShipCheck: vi.fn(),
}));

vi.mock("@/lib/hooks/use-artifacts", () => ({
  useUploadArtifact: vi.fn(),
  useDeleteArtifact: vi.fn(),
}));

vi.mock("@/lib/hooks/use-projects", () => ({
  useProject: vi.fn(),
  useProjectBranches: vi.fn(),
}));

function makeVersion(overrides: Partial<Version> = {}): Version {
  return {
    id: "ver-1",
    project_id: "proj-1",
    version_number: "v1.2.0",
    status: "pending",
    release_notes: "notes",
    target_commitish: "main",
    github_release_url: null,
    error_log: null,
    ship_status: "in_progress",
    ship_stage: "upload_assets",
    ship_message: "正在上传安装包",
    created_at: "2026-04-06T10:00:00Z",
    shipped_at: null,
    artifacts: [
      {
        id: "artifact-1",
        version_id: "ver-1",
        file_name: "app.apk",
        file_size: 1024,
        file_path: "proj-1/ver-1/app.apk",
        platform: "android",
        uploaded_by: "API Key: CI-Upload",
        uploaded_at: "2026-04-06T10:10:00Z",
      },
    ],
    ...overrides,
  };
}

describe("VersionDetailPage", () => {
  const updateMutateAsync = vi.fn();
  const deleteVersionMutateAsync = vi.fn();
  const shipVersionMutateAsync = vi.fn();
  const shipCheckRefetch = vi.fn();
  const uploadMutateAsync = vi.fn();
  const deleteArtifactMutateAsync = vi.fn();

  beforeEach(() => {
    shipCheckRefetch.mockResolvedValue({
      data: {
        can_ship: true,
        items: [],
      },
    });

    vi.mocked(useVersion).mockReturnValue({
      data: makeVersion(),
      isLoading: false,
      refetch: vi.fn(),
    } as unknown as ReturnType<typeof useVersion>);

    vi.mocked(useUpdateVersion).mockReturnValue({
      mutateAsync: updateMutateAsync,
    } as unknown as ReturnType<typeof useUpdateVersion>);

    vi.mocked(useDeleteVersion).mockReturnValue({
      mutateAsync: deleteVersionMutateAsync,
    } as unknown as ReturnType<typeof useDeleteVersion>);

    vi.mocked(useShipVersion).mockReturnValue({
      mutateAsync: shipVersionMutateAsync,
      isPending: false,
    } as unknown as ReturnType<typeof useShipVersion>);

    vi.mocked(useShipCheck).mockReturnValue({
      data: {
        can_ship: true,
        items: [],
      },
      isLoading: false,
      refetch: shipCheckRefetch,
    } as unknown as ReturnType<typeof useShipCheck>);

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

    vi.mocked(useProject).mockReturnValue({
      data: {
        id: "proj-1",
        user_id: "user-1",
        name: "Alpha App",
        description: "Release automation",
        github_owner: "acme",
        github_repo: "alpha",
        created_at: "2026-04-06T09:00:00Z",
        updated_at: "2026-04-06T09:00:00Z",
      },
      isLoading: false,
    } as unknown as ReturnType<typeof useProject>);

    vi.mocked(useUploadArtifact).mockReturnValue({
      mutateAsync: uploadMutateAsync,
      isPending: false,
    } as unknown as ReturnType<typeof useUploadArtifact>);

    vi.mocked(useDeleteArtifact).mockReturnValue({
      mutateAsync: deleteArtifactMutateAsync,
    } as unknown as ReturnType<typeof useDeleteArtifact>);
  });

  afterEach(() => {
    vi.clearAllMocks();
  });

  it("shows shipping progress and disables ship action while in progress", () => {
    renderWithRoute(<VersionDetailPage />, {
      path: "/projects/:id/versions/:vid",
      initialEntry: "/projects/proj-1/versions/ver-1",
    });

    expect(screen.getByText("发货进度")).toBeInTheDocument();
    expect(screen.getByText("创建 Git Tag")).toBeInTheDocument();
    expect(screen.getByText("上传安装包")).toBeInTheDocument();
    expect(screen.getByText("进行中")).toBeInTheDocument();
    expect(screen.getByText("正在上传安装包")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "发货中" })).toBeDisabled();
  });

  it("renders uploaded_by in artifacts list", () => {
    renderWithRoute(<VersionDetailPage />, {
      path: "/projects/:id/versions/:vid",
      initialEntry: "/projects/proj-1/versions/ver-1",
    });

    expect(screen.getByText("安装包")).toBeInTheDocument();
    expect(screen.getByText("API Key: CI-Upload")).toBeInTheDocument();
  });

  it("renders release notes with markdown semantics", () => {
    vi.mocked(useVersion).mockReturnValue({
      data: makeVersion({
        release_notes: "# What's New\n\n- Plex OAuth\n- Genre 浏览",
      }),
      isLoading: false,
      refetch: vi.fn(),
    } as unknown as ReturnType<typeof useVersion>);

    renderWithRoute(<VersionDetailPage />, {
      path: "/projects/:id/versions/:vid",
      initialEntry: "/projects/proj-1/versions/ver-1",
    });

    expect(
      screen.getByRole("heading", { name: "What's New" }),
    ).toBeInTheDocument();
    expect(screen.getByText("Plex OAuth")).toBeInTheDocument();
    expect(screen.getByText("Genre 浏览")).toBeInTheDocument();
  });

  it("allows editing version number when version is editable", async () => {
    const user = userEvent.setup();

    vi.mocked(useVersion).mockReturnValue({
      data: makeVersion({
        ship_status: "",
        ship_stage: "",
        ship_message: null,
      }),
      isLoading: false,
      refetch: vi.fn(),
    } as unknown as ReturnType<typeof useVersion>);

    renderWithRoute(<VersionDetailPage />, {
      path: "/projects/:id/versions/:vid",
      initialEntry: "/projects/proj-1/versions/ver-1",
    });

    const basicInfoCard = screen.getByText("基本信息").closest('[data-slot="card"]');
    expect(basicInfoCard).toBeTruthy();

    const versionSection = within(basicInfoCard as HTMLElement).getByText("版本号");
    const versionBlock = versionSection.closest(".space-y-1");
    expect(versionBlock).not.toBeNull();

    const versionEditButton = within(versionBlock as HTMLElement).getByRole("button");
    await user.click(versionEditButton);

    const input = screen.getByDisplayValue("v1.2.0");
    await user.clear(input);
    await user.type(input, "v1.3.0");
    await user.click(screen.getByRole("button", { name: "保存" }));

    expect(updateMutateAsync).toHaveBeenCalledWith({ version_number: "v1.3.0" });
  });

  it("shows backend ship checks in confirmation dialog", () => {
    vi.mocked(useVersion).mockReturnValue({
      data: makeVersion({
        ship_status: "",
        ship_stage: "",
        ship_message: null,
      }),
      isLoading: false,
      refetch: vi.fn(),
    } as unknown as ReturnType<typeof useVersion>);

    vi.mocked(useShipCheck).mockReturnValue({
      data: {
        can_ship: false,
        items: [
          {
            key: "release_notes",
            label: "Release 说明",
            ok: false,
            detail: "不能为空",
          },
          {
            key: "github_config",
            label: "GitHub 配置",
            ok: false,
            detail: "无法访问 GitHub 仓库或 Token 无效",
          },
        ],
      },
      isLoading: false,
      refetch: shipCheckRefetch,
    } as unknown as ReturnType<typeof useShipCheck>);

    renderWithRoute(<VersionDetailPage />, {
      path: "/projects/:id/versions/:vid",
      initialEntry: "/projects/proj-1/versions/ver-1",
    });

    fireEvent.click(screen.getByRole("button", { name: "发货" }));

    const dialogHeading = screen.getByRole("heading", { name: "确认发货" });
    const dialog = dialogHeading.closest('[data-slot="dialog-content"]');

    expect(dialogHeading).toBeInTheDocument();
    expect(dialog).not.toBeNull();
    expect(
      within(dialog as HTMLElement).getByText("Release 说明"),
    ).toBeInTheDocument();
    expect(within(dialog as HTMLElement).getByText("不能为空")).toBeInTheDocument();
    expect(
      within(dialog as HTMLElement).getByText("GitHub 配置"),
    ).toBeInTheDocument();
    expect(
      within(dialog as HTMLElement).getByText("无法访问 GitHub 仓库或 Token 无效"),
    ).toBeInTheDocument();
    expect(
      within(dialog as HTMLElement).getByRole("button", { name: "确认发货" }),
    ).toBeDisabled();
  });

  it("shows upload progress and prevents duplicate submit while uploading", async () => {
    vi.mocked(useVersion).mockReturnValue({
      data: makeVersion({
        ship_status: "",
        ship_stage: "",
        ship_message: null,
      }),
      isLoading: false,
      refetch: vi.fn(),
    } as unknown as ReturnType<typeof useVersion>);

    vi.mocked(useUploadArtifact).mockReturnValue({
      mutateAsync: vi.fn(async ({ onProgress }) => {
        onProgress?.(42);
        onProgress?.(100);
      }),
      isPending: false,
    } as unknown as ReturnType<typeof useUploadArtifact>);

    renderWithRoute(<VersionDetailPage />, {
      path: "/projects/:id/versions/:vid",
      initialEntry: "/projects/proj-1/versions/ver-1",
    });

    const uploadInput = document.querySelector(
      'input[type="file"]',
    ) as HTMLInputElement;
    const file = new File(["apk"], "android.apk", {
      type: "application/vnd.android.package-archive",
    });

    fireEvent.change(uploadInput, { target: { files: [file] } });

    await waitFor(() =>
      expect(screen.getByText("上传完成，共 1 个文件")).toBeInTheDocument(),
    );
    expect(screen.getByText("100%")).toBeInTheDocument();
    expect(screen.getByText("android.apk")).toBeInTheDocument();
  });

  it("opens failure dialog with backend error after ship fails", async () => {
    const refetchVersion = vi.fn().mockResolvedValue({
      data: makeVersion({
        ship_status: "failed",
        ship_stage: "create_release",
        ship_message: "创建 Release 失败",
        error_log: "创建 Release 失败: tag already exists",
      }),
    });

    vi.mocked(useVersion).mockReturnValue({
      data: makeVersion({
        ship_status: "",
        ship_stage: "",
        ship_message: null,
      }),
      isLoading: false,
      refetch: refetchVersion,
    } as unknown as ReturnType<typeof useVersion>);

    shipVersionMutateAsync.mockRejectedValueOnce(new Error("ship failed"));

    renderWithRoute(<VersionDetailPage />, {
      path: "/projects/:id/versions/:vid",
      initialEntry: "/projects/proj-1/versions/ver-1",
    });

    fireEvent.click(screen.getByRole("button", { name: "发货" }));
    fireEvent.click(screen.getByRole("button", { name: "确认发货" }));

    await waitFor(() =>
      expect(screen.getByRole("heading", { name: "发货失败" })).toBeInTheDocument(),
    );
    expect(
      screen.getByText("创建 Release 失败: tag already exists"),
    ).toBeInTheDocument();
    expect(
      screen.getByText("GitHub 发货流程未完成，版本状态保持为待发货。"),
    ).toBeInTheDocument();
  });

  it("does not show failure dialog when ship request errors but backend is still running", async () => {
    const refetchVersion = vi.fn().mockResolvedValue({
      data: makeVersion({
        ship_status: "in_progress",
        ship_stage: "upload_assets",
        ship_message: "正在上传安装包",
        error_log: null,
      }),
    });

    vi.mocked(useVersion).mockReturnValue({
      data: makeVersion({
        ship_status: "",
        ship_stage: "",
        ship_message: null,
      }),
      isLoading: false,
      refetch: refetchVersion,
    } as unknown as ReturnType<typeof useVersion>);

    shipVersionMutateAsync.mockRejectedValueOnce(new Error("request timeout"));

    renderWithRoute(<VersionDetailPage />, {
      path: "/projects/:id/versions/:vid",
      initialEntry: "/projects/proj-1/versions/ver-1",
    });

    fireEvent.click(screen.getByRole("button", { name: "发货" }));
    fireEvent.click(screen.getByRole("button", { name: "确认发货" }));

    await waitFor(() =>
      expect(refetchVersion).toHaveBeenCalled(),
    );
    expect(
      screen.queryByRole("heading", { name: "发货失败" }),
    ).not.toBeInTheDocument();
    expect(toast.success).toHaveBeenCalledWith("发货已开始，正在同步最新进度");
  });

  it("shows download all button when artifacts exist", () => {
    vi.mocked(useVersion).mockReturnValue({
      data: makeVersion({
        ship_status: "",
        ship_stage: "",
        ship_message: null,
      }),
      isLoading: false,
      refetch: vi.fn(),
    } as unknown as ReturnType<typeof useVersion>);

    renderWithRoute(<VersionDetailPage />, {
      path: "/projects/:id/versions/:vid",
      initialEntry: "/projects/proj-1/versions/ver-1",
    });

    expect(
      screen.getByRole("button", { name: "下载全部 1 个安装包" }),
    ).toBeInTheDocument();
  });

  it("hides download all button when there are no artifacts", () => {
    vi.mocked(useVersion).mockReturnValue({
      data: makeVersion({
        artifacts: [],
        ship_status: "",
        ship_stage: "",
        ship_message: null,
      }),
      isLoading: false,
      refetch: vi.fn(),
    } as unknown as ReturnType<typeof useVersion>);

    renderWithRoute(<VersionDetailPage />, {
      path: "/projects/:id/versions/:vid",
      initialEntry: "/projects/proj-1/versions/ver-1",
    });

    expect(
      screen.queryByRole("button", { name: /下载全部 \d+ 个安装包/ }),
    ).not.toBeInTheDocument();
  });

  it("shows download all button for shipped read-only versions", () => {
    vi.mocked(useVersion).mockReturnValue({
      data: makeVersion({
        status: "shipped",
        ship_status: "completed",
        ship_stage: "",
        ship_message: null,
        shipped_at: "2026-04-06T12:00:00Z",
      }),
      isLoading: false,
      refetch: vi.fn(),
    } as unknown as ReturnType<typeof useVersion>);

    renderWithRoute(<VersionDetailPage />, {
      path: "/projects/:id/versions/:vid",
      initialEntry: "/projects/proj-1/versions/ver-1",
    });

    expect(
      screen.getByRole("button", { name: "下载全部 1 个安装包" }),
    ).toBeInTheDocument();
    expect(
      screen.queryByRole("button", { name: "上传文件" }),
    ).not.toBeInTheDocument();
  });

  it("disables download all while uploading", async () => {
    vi.mocked(useVersion).mockReturnValue({
      data: makeVersion({
        ship_status: "",
        ship_stage: "",
        ship_message: null,
      }),
      isLoading: false,
      refetch: vi.fn(),
    } as unknown as ReturnType<typeof useVersion>);

    vi.mocked(useUploadArtifact).mockReturnValue({
      mutateAsync: vi.fn(() => new Promise(() => {})),
      isPending: false,
    } as unknown as ReturnType<typeof useUploadArtifact>);

    renderWithRoute(<VersionDetailPage />, {
      path: "/projects/:id/versions/:vid",
      initialEntry: "/projects/proj-1/versions/ver-1",
    });

    const uploadInput = document.querySelector(
      'input[type="file"]',
    ) as HTMLInputElement;
    const file = new File(["apk"], "android.apk", {
      type: "application/vnd.android.package-archive",
    });

    fireEvent.change(uploadInput, { target: { files: [file] } });

    await waitFor(() =>
      expect(screen.getByRole("button", { name: "上传中..." })).toBeDisabled(),
    );
    expect(
      screen.getByRole("button", { name: "下载全部 1 个安装包" }),
    ).toBeDisabled();
  });

  it("disables download all while bulk download is in progress", async () => {
    const user = userEvent.setup();
    let resolveDownload: (() => void) | undefined;

    vi.mocked(downloadAllArtifacts).mockImplementation(
      () =>
        new Promise<void>((resolve) => {
          resolveDownload = resolve;
        }),
    );

    vi.mocked(useVersion).mockReturnValue({
      data: makeVersion({
        ship_status: "",
        ship_stage: "",
        ship_message: null,
      }),
      isLoading: false,
      refetch: vi.fn(),
    } as unknown as ReturnType<typeof useVersion>);

    renderWithRoute(<VersionDetailPage />, {
      path: "/projects/:id/versions/:vid",
      initialEntry: "/projects/proj-1/versions/ver-1",
    });

    await user.click(
      screen.getByRole("button", { name: "下载全部 1 个安装包" }),
    );

    expect(
      screen.getByRole("button", { name: "下载全部 1 个安装包" }),
    ).toBeDisabled();

    resolveDownload?.();
    await waitFor(() =>
      expect(
        screen.getByRole("button", { name: "下载全部 1 个安装包" }),
      ).not.toBeDisabled(),
    );
  });

  it("aborts in-flight download when unmounted", async () => {
    const user = userEvent.setup();
    let capturedSignal: AbortSignal | undefined;

    vi.mocked(downloadAllArtifacts).mockImplementation((_urls, options) => {
      capturedSignal = options?.signal;
      return new Promise(() => {});
    });

    vi.mocked(useVersion).mockReturnValue({
      data: makeVersion({
        ship_status: "",
        ship_stage: "",
        ship_message: null,
      }),
      isLoading: false,
      refetch: vi.fn(),
    } as unknown as ReturnType<typeof useVersion>);

    const { unmount } = renderWithRoute(<VersionDetailPage />, {
      path: "/projects/:id/versions/:vid",
      initialEntry: "/projects/proj-1/versions/ver-1",
    });

    await user.click(
      screen.getByRole("button", { name: "下载全部 1 个安装包" }),
    );
    unmount();

    expect(capturedSignal?.aborted).toBe(true);
  });

  it("starts download all for every artifact when clicked", async () => {
    const user = userEvent.setup();

    vi.mocked(useVersion).mockReturnValue({
      data: makeVersion({
        ship_status: "",
        ship_stage: "",
        ship_message: null,
        artifacts: [
          {
            id: "artifact-1",
            version_id: "ver-1",
            file_name: "app.apk",
            file_size: 1024,
            file_path: "proj-1/ver-1/app.apk",
            platform: "android",
            uploaded_by: "API Key: CI-Upload",
            uploaded_at: "2026-04-06T10:10:00Z",
          },
          {
            id: "artifact-2",
            version_id: "ver-1",
            file_name: "app.ipa",
            file_size: 2048,
            file_path: "proj-1/ver-1/app.ipa",
            platform: "ios",
            uploaded_by: "shipbobo",
            uploaded_at: "2026-04-06T10:11:00Z",
          },
        ],
      }),
      isLoading: false,
      refetch: vi.fn(),
    } as unknown as ReturnType<typeof useVersion>);

    renderWithRoute(<VersionDetailPage />, {
      path: "/projects/:id/versions/:vid",
      initialEntry: "/projects/proj-1/versions/ver-1",
    });

    await user.click(
      screen.getByRole("button", { name: "下载全部 2 个安装包" }),
    );

    expect(toast.info).toHaveBeenCalledWith(
      "正在依次下载全部 2 个安装包。若浏览器提示拦截多文件下载，请点击允许；被拦截时可再次点击补发。",
    );
    expect(downloadAllArtifacts).toHaveBeenCalledWith(
      [
        artifactApi.downloadUrl("artifact-1"),
        artifactApi.downloadUrl("artifact-2"),
      ],
      expect.objectContaining({ signal: expect.any(AbortSignal) }),
    );
  });
});
