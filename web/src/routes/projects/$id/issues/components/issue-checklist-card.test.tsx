import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { IssueChecklistCard, type IssueChecklistDraftItem } from "./issue-checklist-card";
import { renderWithRoute } from "@/test/render";
import { toast } from "sonner";

vi.mock("sonner", () => ({
  toast: {
    success: vi.fn(),
    error: vi.fn(),
  },
}));

const items: IssueChecklistDraftItem[] = [
  { localId: "chk-1", id: "chk-1", title: "复现问题", isCompleted: true },
  { localId: "chk-2", id: "chk-2", title: "修复崩溃", isCompleted: false },
];

type CardProps = Parameters<typeof IssueChecklistCard>[0];

function renderCard(overrides: Partial<CardProps>) {
  const props: CardProps = {
    items,
    updatedAt: "2026-04-12T09:00:00Z",
    isSaving: false,
    onSave: vi.fn().mockResolvedValue(true),
    requestSuggestions: vi.fn().mockResolvedValue({ items: [] }),
    onConfigureAi: vi.fn(),
    ...overrides,
  };
  renderWithRoute(<IssueChecklistCard {...props} />);
  return props;
}

describe("IssueChecklistCard", () => {
  beforeEach(() => {
    vi.mocked(toast.success).mockClear();
    vi.mocked(toast.error).mockClear();
  });

  it("renders the persisted checklist in read-only mode", () => {
    renderCard({});

    expect(screen.getByText("复现问题")).toBeInTheDocument();
    expect(screen.getByText("修复崩溃")).toBeInTheDocument();
    expect(screen.getByText("1/2 项完成")).toBeInTheDocument();
    expect(screen.getByText("50%")).toBeInTheDocument();
    expect(screen.getByText(/更新于/)).toBeInTheDocument();
    expect(
      screen.getByRole("button", { name: "标记完成任务清单 2" }),
    ).toBeInTheDocument();
    expect(
      screen.getByRole("button", { name: "取消完成任务清单 1" }),
    ).toBeInTheDocument();
  });

  it("persists a read-only toggle through onSave", async () => {
    const user = userEvent.setup();
    const { onSave } = renderCard({});

    await user.click(screen.getByRole("button", { name: "标记完成任务清单 2" }));

    await waitFor(() =>
      expect(onSave).toHaveBeenCalledWith(
        [
          { id: "chk-1", title: "复现问题", is_completed: true },
          { id: "chk-2", title: "修复崩溃", is_completed: true },
        ],
        undefined,
      ),
    );
  });

  it("rolls the toggle back when onSave fails", async () => {
    const user = userEvent.setup();
    const failingSave = vi.fn().mockResolvedValue(false);
    renderCard({ onSave: failingSave });

    // 乐观勾选后按钮会变成「取消完成」，失败回滚后应恢复
    await user.click(screen.getByRole("button", { name: "标记完成任务清单 2" }));

    await waitFor(() => expect(failingSave).toHaveBeenCalledTimes(1));
    await waitFor(() =>
      expect(
        screen.getByRole("button", { name: "标记完成任务清单 2" }),
      ).toBeInTheDocument(),
    );
    // 数据写入失败的 toast 由路由的 onSave 负责，模块不再发一次
    expect(toast.error).not.toHaveBeenCalled();
  });

  it("disables save in edit mode until the draft is dirty", async () => {
    const user = userEvent.setup();
    const { onSave } = renderCard({});

    await user.click(screen.getByRole("button", { name: "编辑" }));

    const saveButton = screen.getByRole("button", { name: "保存" });
    expect(saveButton).toBeDisabled();

    await user.click(saveButton);
    expect(onSave).not.toHaveBeenCalled();
  });

  it("blocks saving when a title is empty and stays in edit mode", async () => {
    const user = userEvent.setup();
    const { onSave } = renderCard({});

    await user.click(screen.getByRole("button", { name: "编辑" }));
    await user.click(screen.getByRole("button", { name: "添加项" }));

    await user.click(screen.getByRole("button", { name: "保存" }));

    expect(toast.error).toHaveBeenCalledWith("请先填写所有任务清单标题");
    expect(onSave).not.toHaveBeenCalled();
    expect(screen.getByPlaceholderText("任务 3")).toBeInTheDocument();
  });

  it("saves the edited draft and exits edit mode", async () => {
    const user = userEvent.setup();
    const { onSave } = renderCard({});

    await user.click(screen.getByRole("button", { name: "编辑" }));
    const titleInput = screen.getByDisplayValue("修复崩溃");
    await user.clear(titleInput);
    await user.type(titleInput, "修复崩溃并回归");
    await user.click(screen.getByRole("button", { name: "保存" }));

    await waitFor(() =>
      expect(onSave).toHaveBeenCalledWith(
        [
          { id: "chk-1", title: "复现问题", is_completed: true },
          { id: "chk-2", title: "修复崩溃并回归", is_completed: false },
        ],
        { successMessage: "任务清单已保存" },
      ),
    );
    // 保存成功退出编辑态
    await waitFor(() =>
      expect(screen.getByRole("button", { name: "编辑" })).toBeInTheDocument(),
    );
  });

  it("cancels edit mode and restores the persisted items", async () => {
    const user = userEvent.setup();
    renderCard({});

    await user.click(screen.getByRole("button", { name: "编辑" }));
    const titleInput = screen.getByDisplayValue("修复崩溃");
    await user.clear(titleInput);
    await user.type(titleInput, "未保存的草稿");
    await user.click(screen.getByRole("button", { name: "取消编辑" }));

    expect(screen.getByText("修复崩溃")).toBeInTheDocument();
    expect(screen.queryByDisplayValue("未保存的草稿")).not.toBeInTheDocument();
  });

  it("shows the missing-settings state when suggestions return 404", async () => {
    const user = userEvent.setup();
    const { onConfigureAi } = renderCard({
      requestSuggestions: vi.fn().mockRejectedValue({ response: { status: 404 } }),
    });

    await user.click(screen.getByRole("button", { name: "智能识别任务清单建议" }));

    expect(await screen.findByText("尚未配置 AI")).toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: "前往 AI 配置" }));
    expect(onConfigureAi).toHaveBeenCalledTimes(1);
  });

  it("shows the generic error state for non-404 failures", async () => {
    const user = userEvent.setup();
    renderCard({
      requestSuggestions: vi.fn().mockRejectedValue(new Error("boom")),
    });

    await user.click(screen.getByRole("button", { name: "智能识别任务清单建议" }));

    expect(await screen.findByText("识别失败")).toBeInTheDocument();
  });

  it("appends selected suggestions through onSave and closes the dialog", async () => {
    const user = userEvent.setup();
    const { onSave } = renderCard({
      requestSuggestions: vi.fn().mockResolvedValue({
        items: [{ title: "补充复现路径" }, { title: "确认影响版本" }],
      }),
    });

    await user.click(screen.getByRole("button", { name: "智能识别任务清单建议" }));
    expect(await screen.findByText("补充复现路径")).toBeInTheDocument();
    expect(screen.getByText("2/2 已选")).toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: "追加到任务清单" }));

    await waitFor(() =>
      expect(onSave).toHaveBeenCalledWith(
        [
          { id: "chk-1", title: "复现问题", is_completed: true },
          { id: "chk-2", title: "修复崩溃", is_completed: false },
          { id: undefined, title: "补充复现路径", is_completed: false },
          { id: undefined, title: "确认影响版本", is_completed: false },
        ],
        { successMessage: "已补充到任务清单" },
      ),
    );
    await waitFor(() =>
      expect(screen.queryByText("清单建议")).not.toBeInTheDocument(),
    );
  });

  it("rolls appended suggestions back and keeps the dialog open when onSave fails", async () => {
    const user = userEvent.setup();
    const { onSave } = renderCard({
      onSave: vi.fn().mockResolvedValue(false),
      requestSuggestions: vi.fn().mockResolvedValue({
        items: [{ title: "补充复现路径" }],
      }),
    });

    await user.click(screen.getByRole("button", { name: "智能识别任务清单建议" }));
    expect(await screen.findByText("补充复现路径")).toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: "追加到任务清单" }));

    await waitFor(() => expect(onSave).toHaveBeenCalledTimes(1));
    // 保存失败：弹框保持打开，可重新勾选追加
    expect(screen.getByText("清单建议")).toBeInTheDocument();
  });

  it("disables appending when every suggestion is deselected", async () => {
    const user = userEvent.setup();
    const { onSave } = renderCard({
      requestSuggestions: vi.fn().mockResolvedValue({
        items: [{ title: "补充复现路径" }],
      }),
    });

    await user.click(screen.getByRole("button", { name: "智能识别任务清单建议" }));
    expect(await screen.findByText("补充复现路径")).toBeInTheDocument();

    await user.click(screen.getByText("补充复现路径"));
    expect(screen.getByText("0/1 已选")).toBeInTheDocument();
    expect(
      screen.getByRole("button", { name: "追加到任务清单" }),
    ).toBeDisabled();
    expect(onSave).not.toHaveBeenCalled();
  });
});
