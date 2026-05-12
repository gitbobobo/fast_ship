import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
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
          source: "internal",
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
        source: "internal",
      }),
    );
  });

  it("renders source selector when showSourceSelector is true", async () => {
    const onSubmit = vi.fn().mockResolvedValue(undefined);
    render(
      <InternalIssueForm
        defaultValues={{
          title: "",
          body: "",
          workflow_status: "todo",
          source: "internal",
        }}
        showSourceSelector
        onCancel={vi.fn()}
        onSubmit={onSubmit}
        submitLabel="创建问题"
      />,
    );

    expect(screen.getByRole("radio", { name: "内部问题" })).toBeInTheDocument();
    expect(screen.getByRole("radio", { name: "GitHub 问题" })).toBeInTheDocument();
  });

  it("hides workflow status when github source is selected", async () => {
    const user = userEvent.setup();
    const onSubmit = vi.fn().mockResolvedValue(undefined);
    render(
      <InternalIssueForm
        defaultValues={{
          title: "",
          body: "",
          workflow_status: "todo",
          source: "internal",
        }}
        showSourceSelector
        showWorkflowStatus
        onCancel={vi.fn()}
        onSubmit={onSubmit}
        submitLabel="创建问题"
      />,
    );

    expect(screen.getByText("内部状态")).toBeInTheDocument();

    await user.click(screen.getByRole("radio", { name: "GitHub 问题" }));

    expect(screen.queryByText("内部状态")).not.toBeInTheDocument();
  });

  it("submits with correct source value", async () => {
    const user = userEvent.setup();
    const onSubmit = vi.fn().mockResolvedValue(undefined);
    render(
      <InternalIssueForm
        defaultValues={{
          title: "",
          body: "",
          workflow_status: "todo",
          source: "internal",
        }}
        showSourceSelector
        onCancel={vi.fn()}
        onSubmit={onSubmit}
        submitLabel="创建问题"
      />,
    );

    await user.type(screen.getByPlaceholderText("例如：设置页在切换主题后闪退"), "GitHub 问题标题");
    await user.click(screen.getByRole("radio", { name: "GitHub 问题" }));
    await user.click(screen.getByRole("button", { name: "创建问题" }));

    await waitFor(() =>
      expect(onSubmit).toHaveBeenCalledWith({
        title: "GitHub 问题标题",
        body: "",
        workflow_status: "todo",
        source: "github",
      }),
    );
  });
});
