import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import { InternalIssueForm } from "@/components/issues/internal-issue-form";

vi.mock("@/components/ui/markdown-editor", () => ({
  MarkdownEditor: ({
    value = "",
    onChange,
    onPasteImage,
    placeholder,
  }: {
    value?: string;
    onChange?: (value: string) => void;
    onPasteImage?: (file: File) => Promise<string>;
    placeholder?: string;
  }) => (
    <div>
      <textarea
        aria-label="描述"
        value={value}
        placeholder={placeholder}
        onChange={(event) => onChange?.(event.target.value)}
      />
      <button
        type="button"
        onClick={() => {
          if (!onPasteImage) {
            return;
          }

          const file = new File(["img"], "clip.png", { type: "image/png" });
          void onPasteImage(file)
            .then((markdown) => {
              onChange?.(`${value}${markdown}\n`);
            })
            .catch(() => undefined);
        }}
      >
        粘贴图片
      </button>
    </div>
  ),
}));

describe("InternalIssueForm", () => {
  it("waits for pasted image uploads before submitting the latest body", async () => {
    let resolveUpload: ((markdown: string) => void) | undefined;
    const onPasteImage = vi.fn(
      () =>
        new Promise<string>((resolve) => {
          resolveUpload = resolve;
        }),
    );
    const onSubmit = vi.fn().mockResolvedValue(undefined);
    const { container } = render(
      <InternalIssueForm
        defaultValues={{
          title: "补充发布检查",
          body: "已有描述\n",
          workflow_status: "todo",
        }}
        onCancel={vi.fn()}
        onPasteImage={onPasteImage}
        onSubmit={onSubmit}
        submitLabel="创建问题"
      />,
    );

    fireEvent.click(screen.getByRole("button", { name: "粘贴图片" }));

    await waitFor(() => expect(onPasteImage).toHaveBeenCalledTimes(1));
    const submitButton = screen.getByRole("button", { name: "保存中..." });
    expect(submitButton).toBeDisabled();

    const form = container.querySelector("form");
    if (!form) {
      throw new Error("form not found");
    }

    fireEvent.submit(form);
    expect(onSubmit).not.toHaveBeenCalled();

    resolveUpload?.("![clip](/api/issues/assets/asset-1/content)");

    await waitFor(() =>
      expect(onSubmit).toHaveBeenCalledWith({
        title: "补充发布检查",
        body: "已有描述\n![clip](/api/issues/assets/asset-1/content)\n",
        workflow_status: "todo",
      }),
    );
  });
});
