import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import { toast } from "sonner";
import { InternalIssueForm } from "@/components/issues/internal-issue-form";
import { useAISettings, useGenerateTitle } from "@/lib/hooks/use-ai";

vi.mock("sonner", () => ({
  toast: { error: vi.fn() },
}));

vi.mock("@/lib/hooks/use-ai", () => ({
  useAISettings: vi.fn(() => ({
    data: { configured: true, api_host: "https://api.minimaxi.com", model: "MiniMax-M2.5" },
  })),
  useGenerateTitle: vi.fn(() => ({
    mutate: vi.fn(),
    isPending: false,
  })),
}));

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
          workflow_status: "",
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
        workflow_status: "",
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
          workflow_status: "",
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
          workflow_status: "",
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
          workflow_status: "",
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
        workflow_status: "",
        source: "github",
      }),
    );
  });

  it("disables generate title button when body is less than 10 characters", () => {
    const onSubmit = vi.fn().mockResolvedValue(undefined);
    render(
      <InternalIssueForm
        defaultValues={{ title: "", body: "短内容", workflow_status: "", source: "internal" }}
        onCancel={vi.fn()}
        onSubmit={onSubmit}
        submitLabel="创建问题"
      />,
    );

    expect(screen.getByRole("button", { name: "请先填写正文内容（至少 10 个字符）" })).toBeDisabled();
  });

  it("enables generate title button when body has 10+ characters", () => {
    const onSubmit = vi.fn().mockResolvedValue(undefined);
    render(
      <InternalIssueForm
        defaultValues={{
          title: "",
          body: "这是一段足够长的正文内容描述",
          workflow_status: "",
          source: "internal",
        }}
        onCancel={vi.fn()}
        onSubmit={onSubmit}
        submitLabel="创建问题"
      />,
    );

    expect(screen.getByRole("button", { name: "AI 生成标题" })).toBeEnabled();
  });

  it("disables generate title button when AI is not configured", () => {
    vi.mocked(useAISettings).mockImplementation(() => ({
      data: { configured: false, api_host: "", model: "" },
    } as ReturnType<typeof useAISettings>));

    const onSubmit = vi.fn().mockResolvedValue(undefined);
    render(
      <InternalIssueForm
        defaultValues={{
          title: "",
          body: "这是一段足够长的正文内容描述",
          workflow_status: "",
          source: "internal",
        }}
        onCancel={vi.fn()}
        onSubmit={onSubmit}
        submitLabel="创建问题"
      />,
    );

    expect(screen.getByRole("button", { name: "请先在设置中配置 AI" })).toBeDisabled();

    vi.mocked(useAISettings).mockRestore();
  });

  it("calls generate title mutation on click and sets title on success", async () => {
    const user = userEvent.setup();
    const mockMutate = vi.fn((_body: string, options?: { onSuccess?: (data: { title: string }) => void }) => {
      options?.onSuccess?.({ title: "修复登录白屏问题" });
    });
    vi.mocked(useGenerateTitle).mockImplementation(() => ({
      mutate: mockMutate,
      isPending: false,
    } as ReturnType<typeof useGenerateTitle>));

    const onSubmit = vi.fn().mockResolvedValue(undefined);
    render(
      <InternalIssueForm
        defaultValues={{
          title: "",
          body: "这是一段足够长的正文内容描述",
          workflow_status: "",
          source: "internal",
        }}
        onCancel={vi.fn()}
        onSubmit={onSubmit}
        submitLabel="创建问题"
      />,
    );

    await user.click(screen.getByRole("button", { name: "AI 生成标题" }));
    expect(mockMutate).toHaveBeenCalledWith("这是一段足够长的正文内容描述", expect.any(Object));
    expect(screen.getByPlaceholderText("例如：设置页在切换主题后闪退")).toHaveValue("修复登录白屏问题");

    vi.mocked(useGenerateTitle).mockRestore();
  });

  it("shows loading spinner when mutation is pending", () => {
    vi.mocked(useGenerateTitle).mockImplementation(() => ({
      mutate: vi.fn(),
      isPending: true,
    } as ReturnType<typeof useGenerateTitle>));

    const onSubmit = vi.fn().mockResolvedValue(undefined);
    render(
      <InternalIssueForm
        defaultValues={{
          title: "",
          body: "这是一段足够长的正文内容描述",
          workflow_status: "",
          source: "internal",
        }}
        onCancel={vi.fn()}
        onSubmit={onSubmit}
        submitLabel="创建问题"
      />,
    );

    const button = screen.getByRole("button", { name: "AI 生成标题" });
    expect(button.querySelector(".animate-spin")).toBeInTheDocument();

    vi.mocked(useGenerateTitle).mockRestore();
  });

  it("shows toast error when mutation fails", async () => {
    const user = userEvent.setup();
    const mockMutate = vi.fn((_body: string, options?: { onError?: () => void }) => {
      options?.onError?.();
    });
    vi.mocked(useGenerateTitle).mockImplementation(() => ({
      mutate: mockMutate,
      isPending: false,
    } as ReturnType<typeof useGenerateTitle>));

    const onSubmit = vi.fn().mockResolvedValue(undefined);
    render(
      <InternalIssueForm
        defaultValues={{
          title: "",
          body: "这是一段足够长的正文内容描述",
          workflow_status: "",
          source: "internal",
        }}
        onCancel={vi.fn()}
        onSubmit={onSubmit}
        submitLabel="创建问题"
      />,
    );

    await user.click(screen.getByRole("button", { name: "AI 生成标题" }));
    expect(toast.error).toHaveBeenCalledWith("生成标题失败，请稍后重试");

    vi.mocked(useGenerateTitle).mockRestore();
  });
});
