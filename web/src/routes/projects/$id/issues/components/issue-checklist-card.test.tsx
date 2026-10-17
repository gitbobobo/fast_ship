import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import {
  IssueChecklistCard,
  IssueChecklistSuggestionsDialog,
  useIssueChecklistController,
  type IssueChecklistControllerOptions,
} from "./issue-checklist-card";
import { renderWithRoute } from "@/test/render";
import { toast } from "sonner";

vi.mock("sonner", () => ({
  toast: {
    success: vi.fn(),
    error: vi.fn(),
  },
}));

const items: IssueChecklistControllerOptions["items"] = [
  { localId: "chk-1", id: "chk-1", title: "复现问题", isCompleted: true },
  { localId: "chk-2", id: "chk-2", title: "修复崩溃", isCompleted: false },
];

/** 测试挂载点：controller 只创建一次，卡片可渲染多份（模拟桌面 aside + 移动 Sheet），
 *  建议弹框与 route 一样只渲染一次。 */
function ChecklistHarness({
  cardCount = 1,
  ...options
}: IssueChecklistControllerOptions & { cardCount?: number }) {
  const controller = useIssueChecklistController(options);
  return (
    <>
      {Array.from({ length: cardCount }, (_, index) => (
        <IssueChecklistCard key={index} controller={controller} />
      ))}
      <IssueChecklistSuggestionsDialog controller={controller} />
    </>
  );
}

type HarnessProps = Parameters<typeof ChecklistHarness>[0];

function renderHarness(overrides: Partial<HarnessProps> = {}) {
  const props: HarnessProps = {
    items,
    updatedAt: "2026-04-12T09:00:00Z",
    isSaving: false,
    onSave: vi.fn().mockResolvedValue(true),
    requestSuggestions: vi.fn().mockResolvedValue({ items: [] }),
    onConfigureAi: vi.fn(),
    ...overrides,
  };
  renderWithRoute(<ChecklistHarness {...props} />);
  return props;
}

describe("IssueChecklistCard", () => {
  beforeEach(() => {
    vi.mocked(toast.success).mockClear();
    vi.mocked(toast.error).mockClear();
  });

  it("renders the persisted checklist in read-only mode", () => {
    renderHarness({});

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
    const { onSave } = renderHarness({});

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
    renderHarness({ onSave: failingSave });

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
    const { onSave } = renderHarness({});

    await user.click(screen.getByRole("button", { name: "编辑" }));

    const saveButton = screen.getByRole("button", { name: "保存" });
    expect(saveButton).toBeDisabled();

    await user.click(saveButton);
    expect(onSave).not.toHaveBeenCalled();
  });

  it("blocks saving when a title is empty and stays in edit mode", async () => {
    const user = userEvent.setup();
    const { onSave } = renderHarness({});

    await user.click(screen.getByRole("button", { name: "编辑" }));
    await user.click(screen.getByRole("button", { name: "添加项" }));

    await user.click(screen.getByRole("button", { name: "保存" }));

    expect(toast.error).toHaveBeenCalledWith("请先填写所有任务清单标题");
    expect(onSave).not.toHaveBeenCalled();
    expect(screen.getByPlaceholderText("任务 3")).toBeInTheDocument();
  });

  it("saves the edited draft and exits edit mode", async () => {
    const user = userEvent.setup();
    const { onSave } = renderHarness({});

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
    renderHarness({});

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
    const { onConfigureAi } = renderHarness({
      requestSuggestions: vi.fn().mockRejectedValue({ response: { status: 404 } }),
    });

    await user.click(screen.getByRole("button", { name: "智能识别任务清单建议" }));

    expect(await screen.findByText("尚未配置 AI")).toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: "前往 AI 配置" }));
    expect(onConfigureAi).toHaveBeenCalledTimes(1);
  });

  it("shows the generic error state for non-404 failures", async () => {
    const user = userEvent.setup();
    renderHarness({
      requestSuggestions: vi.fn().mockRejectedValue(new Error("boom")),
    });

    await user.click(screen.getByRole("button", { name: "智能识别任务清单建议" }));

    expect(await screen.findByText("识别失败")).toBeInTheDocument();
  });

  it("appends selected suggestions through onSave and closes the dialog", async () => {
    const user = userEvent.setup();
    const { onSave } = renderHarness({
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
    const { onSave } = renderHarness({
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
    const { onSave } = renderHarness({
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

  // 回归：桌面 aside 与移动 Sheet 各渲染一份卡片，但 draft/编辑态必须只有一份
  it("shares draft and editing state across two card instances", async () => {
    const user = userEvent.setup();
    const { onSave } = renderHarness({ cardCount: 2 });

    // 卡片 A 勾选只读项，卡片 B 同步显示乐观更新
    await user.click(
      screen.getAllByRole("button", { name: "标记完成任务清单 2" })[0],
    );
    await waitFor(() =>
      expect(
        screen.getAllByRole("button", { name: "取消完成任务清单 2" }),
      ).toHaveLength(2),
    );
    expect(onSave).toHaveBeenCalledTimes(1);

    // 卡片 A 进入编辑态，卡片 B 也进入编辑态
    await user.click(screen.getAllByRole("button", { name: "编辑" })[0]);
    expect(screen.getAllByRole("button", { name: "保存" })).toHaveLength(2);
    expect(screen.queryAllByRole("button", { name: "编辑" })).toHaveLength(0);

    // 卡片 A 的草稿改动，卡片 B 显示同一份 draft
    const inputs = screen.getAllByDisplayValue("修复崩溃");
    expect(inputs).toHaveLength(2);
    await user.clear(inputs[0]);
    await user.type(inputs[0], "修复崩溃并回归");
    expect(screen.getAllByDisplayValue("修复崩溃并回归")).toHaveLength(2);
  });

  // 回归：建议弹框由 route 只渲染一次，两个卡片入口不会产生第二个弹框
  it("keeps a single suggestions dialog across two card instances", async () => {
    const user = userEvent.setup();
    renderHarness({
      cardCount: 2,
      requestSuggestions: vi.fn().mockResolvedValue({
        items: [{ title: "补充复现路径" }],
      }),
    });

    const sparkleButtons = screen.getAllByRole("button", {
      name: "智能识别任务清单建议",
    });
    await user.click(sparkleButtons[0]);
    expect(await screen.findByText("补充复现路径")).toBeInTheDocument();

    // 从另一个卡片入口再触发一次识别，也不应出现第二个弹框
    await user.click(sparkleButtons[1]);
    await waitFor(() =>
      expect(screen.getAllByText("补充复现路径")).toHaveLength(1),
    );
    expect(screen.getAllByText("清单建议")).toHaveLength(1);
    expect(
      screen.getAllByRole("button", { name: "追加到任务清单" }),
    ).toHaveLength(1);
  });
});
