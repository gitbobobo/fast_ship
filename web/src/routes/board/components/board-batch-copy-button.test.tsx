import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { BoardBatchCopyButton } from "./board-batch-copy-button";

const { copyWithToastMock, setIssuePrompts, getIssuePrompts } = vi.hoisted(
  () => {
    let issuePrompts: IssuePrompt[] = [];
    return {
      copyWithToastMock: vi.fn(),
      setIssuePrompts: (prompts: IssuePrompt[]) => {
        issuePrompts = prompts;
      },
      getIssuePrompts: () => issuePrompts,
    };
  },
);

vi.mock("@/lib/copy", () => ({ copyWithToast: copyWithToastMock }));

// 每个用例通过 setIssuePrompts 控制 useIssuePromptList 的返回值
vi.mock("@/lib/hooks/use-issue-prompt", () => ({
  useIssuePromptList: () => getIssuePrompts(),
}));

const orderedIssues = [
  { id: "i-1" } as Issue,
  { id: "i-2" } as Issue,
];

function setup(
  props: Partial<Parameters<typeof BoardBatchCopyButton>[0]> = {},
) {
  const onCopied = vi.fn();
  const getOrderedSelectedIssues = vi.fn(() => orderedIssues);
  render(
    <BoardBatchCopyButton
      projectId="proj-1"
      selectedCount={orderedIssues.length}
      getOrderedSelectedIssues={getOrderedSelectedIssues}
      onCopied={onCopied}
      {...props}
    />,
  );
  return { onCopied, getOrderedSelectedIssues };
}

async function openDropdownMenu() {
  fireEvent.click(screen.getByRole("button", { name: /复制提示词/ }));
  await waitFor(() => {
    expect(screen.getByRole("menu")).toBeInTheDocument();
  });
}

/** 等待第 N 次复制完成（copyWithToast 的 Promise 已 settle） */
async function waitForCopySettled() {
  await waitFor(() => expect(copyWithToastMock).toHaveBeenCalledTimes(1));
  await copyWithToastMock.mock.results[0].value;
}

describe("BoardBatchCopyButton", () => {
  beforeEach(() => {
    copyWithToastMock.mockReset();
    copyWithToastMock.mockResolvedValue(true);
    setIssuePrompts([
      {
        id: "default",
        name: "默认",
        content: "请处理此问题",
        supports_batch: true,
      },
    ]);
  });

  it("0 条选中时禁用并提示先选择", () => {
    setIssuePrompts([
      {
        id: "a",
        name: "模板A",
        content: "内容A",
        supports_batch: true,
      },
      {
        id: "b",
        name: "模板B",
        content: "内容B",
        supports_batch: true,
      },
    ]);
    setup({ selectedCount: 0 });

    const button = screen.getByRole("button", { name: /复制提示词/ });
    expect(button).toBeDisabled();
    expect(screen.getByTitle("先选择要复制的问题")).toBeInTheDocument();
    expect(
      screen.queryByTitle("没有可用于批量的提示词"),
    ).not.toBeInTheDocument();
  });

  it("没有可批量模板时禁用并单独提示", () => {
    setIssuePrompts([
      {
        id: "solo",
        name: "仅单个",
        content: "内容",
        supports_batch: false,
      },
    ]);
    setup({ selectedCount: 2 });

    const button = screen.getByRole("button", { name: /复制提示词/ });
    expect(button).toBeDisabled();
    expect(screen.getByTitle("没有可用于批量的提示词")).toBeInTheDocument();
    expect(screen.queryByTitle("先选择要复制的问题")).not.toBeInTheDocument();
  });

  it("下拉只列出 supports_batch 的模板", async () => {
    setIssuePrompts([
      {
        id: "batch",
        name: "批量模板",
        content: "批量内容",
        supports_batch: true,
      },
      {
        id: "batch-2",
        name: "批量模板二",
        content: "批量内容二",
        supports_batch: true,
      },
      {
        id: "single",
        name: "单个模板",
        content: "单个内容",
        supports_batch: false,
      },
    ]);
    setup();

    await openDropdownMenu();

    expect(
      screen.getByRole("menuitem", { name: "批量模板" }),
    ).toBeInTheDocument();
    expect(
      screen.queryByRole("menuitem", { name: "单个模板" }),
    ).not.toBeInTheDocument();
  });

  it("单个可批量模板时直接点击复制，内容用按列顺序的 id", async () => {
    setIssuePrompts([
      {
        id: "default",
        name: "默认",
        content: "请处理此问题",
        supports_batch: true,
      },
    ]);
    const { onCopied, getOrderedSelectedIssues } = setup();

    fireEvent.click(screen.getByRole("button", { name: /复制提示词/ }));
    await waitForCopySettled();

    expect(getOrderedSelectedIssues).toHaveBeenCalledTimes(1);
    expect(copyWithToastMock).toHaveBeenCalledWith(
      `/fast-ship 请处理此问题
---
项目ID：proj-1
问题ID：i-1
问题ID：i-2`,
      "已复制提示词",
    );
    expect(onCopied).toHaveBeenCalledTimes(1);
  });

  it("多个可批量模板时下拉选择后复制", async () => {
    setIssuePrompts([
      {
        id: "default",
        name: "默认",
        content: "请处理此问题",
        supports_batch: true,
      },
      {
        id: "review",
        name: "评审",
        content: "请评审此问题",
        supports_batch: true,
      },
    ]);
    const { onCopied } = setup({
      getOrderedSelectedIssues: vi.fn(() => [
        { id: "i-2" } as Issue,
        { id: "i-1" } as Issue,
      ]),
    });

    await openDropdownMenu();
    fireEvent.click(screen.getByRole("menuitem", { name: "评审" }));
    await waitForCopySettled();

    expect(copyWithToastMock).toHaveBeenCalledWith(
      `/fast-ship 请评审此问题
---
项目ID：proj-1
问题ID：i-2
问题ID：i-1`,
      "已复制提示词",
    );
    expect(onCopied).toHaveBeenCalledTimes(1);
  });

  it("复制失败时不调用 onCopied（保持勾选）", async () => {
    copyWithToastMock.mockResolvedValue(false);
    const { onCopied } = setup();

    fireEvent.click(screen.getByRole("button", { name: /复制提示词/ }));
    await waitForCopySettled();

    expect(copyWithToastMock).toHaveBeenCalledTimes(1);
    expect(onCopied).not.toHaveBeenCalled();
  });
});
