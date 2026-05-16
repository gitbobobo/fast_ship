import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { useState } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { MarkdownEditor } from "@/components/ui/markdown-editor";
import { useAuthStore } from "@/lib/store/auth-store";
import { useThemeStore } from "@/lib/store/theme-store";

vi.mock("@/lib/store/auth-store", () => ({
  useAuthStore: vi.fn(),
}));

vi.mock("@/lib/store/theme-store", () => ({
  useThemeStore: vi.fn(),
}));

function MarkdownEditorHarness({ onPasteImage }: { onPasteImage: (file: File) => Promise<string> }) {
  const [value, setValue] = useState("更新后的描述");

  return (
    <MarkdownEditor
      value={value}
      onChange={setValue}
      onPasteImage={onPasteImage}
      placeholder="描述"
    />
  );
}

describe("MarkdownEditor", () => {
  const authState = {
    token: null as string | null,
    user: null as User | null,
    setAuth: vi.fn(),
    setUser: vi.fn(),
    logout: vi.fn(),
  };

  beforeEach(() => {
    vi.mocked(useAuthStore).mockImplementation(((selector?: (state: typeof authState) => unknown) =>
      selector ? selector(authState) : authState) as typeof useAuthStore);
    vi.mocked(useThemeStore).mockReturnValue({ resolvedTheme: "light" } as ReturnType<typeof useThemeStore>);
    Object.defineProperty(navigator, "clipboard", {
      configurable: true,
      value: {
        read: vi.fn().mockResolvedValue([]),
      },
    });
  });

  it("handles image paste from a document-level paste event while the editor is focused", async () => {
    const onPasteImage = vi.fn().mockResolvedValue("![clip](/api/issues/assets/asset-1/content)");
    render(<MarkdownEditorHarness onPasteImage={onPasteImage} />);

    const textarea = screen.getByPlaceholderText("描述") as HTMLTextAreaElement;
    textarea.focus();
    textarea.setSelectionRange(textarea.value.length, textarea.value.length);

    const imageFile = new File(["img"], "clip.png", { type: "image/png" });
    fireEvent.paste(document, {
      clipboardData: {
        items: [
          {
            type: imageFile.type,
            getAsFile: () => imageFile,
          },
        ],
      },
    });

    await waitFor(() => expect(onPasteImage).toHaveBeenCalledWith(imageFile));
    await waitFor(() =>
      expect(textarea).toHaveValue("更新后的描述![clip](/api/issues/assets/asset-1/content)\n"),
    );
  });

  it("falls back to navigator.clipboard.read for image paste", async () => {
    const onPasteImage = vi.fn().mockResolvedValue("![clip](/api/issues/assets/asset-1/content)");
    const imageBlob = new Blob(["img"], { type: "image/png" });
    Object.defineProperty(navigator, "clipboard", {
      configurable: true,
      value: {
        read: vi.fn().mockResolvedValue([
          {
            types: ["image/png"],
            getType: vi.fn().mockResolvedValue(imageBlob),
          },
        ]),
      },
    });

    render(<MarkdownEditorHarness onPasteImage={onPasteImage} />);

    const textarea = screen.getByPlaceholderText("描述") as HTMLTextAreaElement;
    textarea.focus();
    textarea.setSelectionRange(textarea.value.length, textarea.value.length);

    fireEvent.paste(document, {
      clipboardData: {
        items: [],
        files: [],
      },
    });

    await waitFor(() => expect(onPasteImage).toHaveBeenCalledTimes(1));
    const uploadedFile = onPasteImage.mock.calls[0][0] as File;
    expect(uploadedFile.type).toBe("image/png");
    await waitFor(() =>
      expect(textarea).toHaveValue("更新后的描述![clip](/api/issues/assets/asset-1/content)\n"),
    );
  });

  describe("mobile viewport", () => {
    const originalMatchMedia = window.matchMedia;

    function mockMobileViewport(mobile: boolean) {
      Object.defineProperty(window, "matchMedia", {
        configurable: true,
        writable: true,
        value: vi.fn().mockImplementation((query: string) => ({
          matches: mobile && query.includes("767"),
          media: query,
          onchange: null,
          addListener: vi.fn(),
          removeListener: vi.fn(),
          addEventListener: vi.fn(),
          removeEventListener: vi.fn(),
          dispatchEvent: vi.fn(),
        })),
      });
    }

    afterEach(() => {
      Object.defineProperty(window, "matchMedia", {
        configurable: true,
        writable: true,
        value: originalMatchMedia,
      });
    });

    it("uses edit mode on mobile and hides live preview button", () => {
      mockMobileViewport(true);
      render(<MarkdownEditor value="hello" onChange={vi.fn()} />);

      // edit 模式下不应渲染预览面板
      const previewPane = document.querySelector(".w-md-editor-preview");
      expect(previewPane).toBeNull();

      // 额外命令栏不应包含 live 按钮（aria-label 包含 "Live"）
      const extraButtons = document.querySelectorAll("[data-testid=\"w-md-editor-toolbar-extra\"] button");
      const liveButton = Array.from(extraButtons).find(
        (btn) => btn.getAttribute("aria-label")?.includes("Live"),
      );
      expect(liveButton).toBeUndefined();
    });

    it("shows live preview on desktop", () => {
      mockMobileViewport(false);
      render(<MarkdownEditor value="hello" onChange={vi.fn()} />);

      // live 模式下应渲染预览面板
      const previewPane = document.querySelector(".w-md-editor-preview");
      expect(previewPane).toBeInTheDocument();
    });
  });
});
