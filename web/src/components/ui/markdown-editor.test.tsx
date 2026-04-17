import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { useState } from "react";
import { beforeEach, describe, expect, it, vi } from "vitest";
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
});
