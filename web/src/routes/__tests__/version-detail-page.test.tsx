import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import { vi } from "vitest";
import VersionDetailPage from "@/routes/projects/$id/versions/$vid";
import { renderWithRoute } from "@/test/render";
import {
  useDeleteVersion,
  useShipCheck,
  useShipVersion,
  useUpdateVersion,
  useVersion,
} from "@/lib/hooks/use-versions";
import { useDeleteArtifact, useUploadArtifact } from "@/lib/hooks/use-artifacts";

vi.mock("sonner", () => ({
  toast: {
    success: vi.fn(),
    error: vi.fn(),
  },
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
    expect(screen.getByRole("button", { name: "发货中..." })).toBeDisabled();
  });

  it("renders uploaded_by in artifacts table", () => {
    renderWithRoute(<VersionDetailPage />, {
      path: "/projects/:id/versions/:vid",
      initialEntry: "/projects/proj-1/versions/ver-1",
    });

    expect(screen.getByText("上传者")).toBeInTheDocument();
    expect(screen.getByText("API Key: CI-Upload")).toBeInTheDocument();
  });

  it("allows editing version number when version is editable", () => {
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

    const versionRow = screen.getByText("版本号：").closest("div");
    expect(versionRow).not.toBeNull();
    fireEvent.click(within(versionRow as HTMLElement).getByRole("button"));

    const input = screen.getByDisplayValue("v1.2.0");
    fireEvent.change(input, { target: { value: "v1.3.0" } });
    fireEvent.click(screen.getByRole("button", { name: "保存" }));

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

    fireEvent.click(screen.getByRole("button", { name: "发货到 GitHub" }));

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

    fireEvent.click(screen.getByRole("button", { name: "发货到 GitHub" }));
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
});
