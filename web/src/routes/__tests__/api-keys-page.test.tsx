import { fireEvent, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { vi } from "vitest";
import ApiKeysPage from "@/routes/settings/api-keys";
import { renderWithRoute } from "@/test/render";
import {
  useApiKeys,
  useCreateApiKey,
  useDeleteApiKey,
} from "@/lib/hooks/use-api-keys";
import { toast } from "sonner";

vi.mock("sonner", () => ({
  toast: {
    success: vi.fn(),
    error: vi.fn(),
  },
}));

vi.mock("@/lib/hooks/use-api-keys", () => ({
  useApiKeys: vi.fn(),
  useCreateApiKey: vi.fn(),
  useDeleteApiKey: vi.fn(),
}));

describe("ApiKeysPage", () => {
  const createMutateAsync = vi.fn();
  const deleteMutateAsync = vi.fn();
  const writeText = vi.fn();

  beforeEach(() => {
    writeText.mockResolvedValue(undefined);

    Object.defineProperty(navigator, "clipboard", {
      configurable: true,
      value: { writeText },
    });

    vi.mocked(useApiKeys).mockReturnValue({
      data: [],
      isLoading: false,
    } as unknown as ReturnType<typeof useApiKeys>);

    vi.mocked(useCreateApiKey).mockReturnValue({
      mutateAsync: createMutateAsync,
    } as unknown as ReturnType<typeof useCreateApiKey>);

    vi.mocked(useDeleteApiKey).mockReturnValue({
      mutateAsync: deleteMutateAsync,
    } as unknown as ReturnType<typeof useDeleteApiKey>);
  });

  afterEach(() => {
    vi.clearAllMocks();
  });

  it("renders the empty api key state", () => {
    renderWithRoute(<ApiKeysPage />, {
      path: "/settings/api-keys",
      initialEntry: "/settings/api-keys",
    });

    expect(screen.getByText("API Key 管理")).toBeInTheDocument();
    expect(screen.getByText("暂无 API Key")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "创建 API Key" })).toBeInTheDocument();
  });

  it("creates an api key and copies the full key", async () => {
    createMutateAsync.mockResolvedValue({
      data: { key: "fsk_secret_key_123" },
    });

    const user = userEvent.setup();

    renderWithRoute(<ApiKeysPage />, {
      path: "/settings/api-keys",
      initialEntry: "/settings/api-keys",
    });

    await user.click(screen.getByRole("button", { name: "创建 API Key" }));
    await user.type(screen.getByLabelText("备注名称"), "CI-Android");
    await user.click(screen.getByRole("button", { name: "创建" }));

    await waitFor(() =>
      expect(screen.getByText("API Key 创建成功")).toBeInTheDocument(),
    );
    expect(createMutateAsync).toHaveBeenCalledWith({ name: "CI-Android" });
    expect(screen.getByText("fsk_secret_key_123")).toBeInTheDocument();

    fireEvent.click(screen.getByRole("button", { name: "复制 API Key" }));
    await waitFor(() =>
      expect(toast.success).toHaveBeenCalledWith("已复制到剪贴板"),
    );
  });

  it("deletes an existing api key after confirmation", async () => {
    vi.mocked(useApiKeys).mockReturnValue({
      data: [
        {
          id: "key-1",
          name: "CI-Android",
          key_prefix: "ABCDEFGH",
          created_at: "2026-04-06T10:00:00Z",
          last_used_at: null,
        },
      ],
      isLoading: false,
    } as unknown as ReturnType<typeof useApiKeys>);

    const user = userEvent.setup();

    renderWithRoute(<ApiKeysPage />, {
      path: "/settings/api-keys",
      initialEntry: "/settings/api-keys",
    });

    await user.click(screen.getByRole("button", { name: "删除 API Key CI-Android" }));
    fireEvent.click(screen.getByRole("button", { name: "确认删除" }));

    await waitFor(() =>
      expect(deleteMutateAsync).toHaveBeenCalledWith("key-1"),
    );
  });
});
