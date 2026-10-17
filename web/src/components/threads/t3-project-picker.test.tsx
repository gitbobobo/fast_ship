import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import type { ComposerProject } from "@/lib/api/threads";
import { T3ProjectPicker } from "./t3-project-picker";

const PROJECTS: ComposerProject[] = [
  { id: "p1", title: "Fast Ship", workspace_root: "/code/fast-ship" },
  { id: "p2", title: "Mobile Repo", workspace_root: "/code/mobile-repo" },
  { id: "p3", title: "文档" },
];

const trigger = () => screen.getByRole("button", { name: "项目" });

async function openPanel(user: ReturnType<typeof userEvent.setup>) {
  await user.click(trigger());
  return await screen.findByRole("listbox");
}

describe("T3ProjectPicker", () => {
  it("触发器显示项目徽标与名称", () => {
    render(
      <T3ProjectPicker
        projects={PROJECTS}
        value="p1"
        onValueChange={vi.fn()}
      />,
    );
    expect(trigger()).toHaveTextContent("FS");
    expect(trigger()).toHaveTextContent("Fast Ship");
  });

  it("triggerLabel 覆盖显示文本；无选中时显示 placeholder", () => {
    const { rerender } = render(
      <T3ProjectPicker
        projects={PROJECTS}
        value="missing"
        onValueChange={vi.fn()}
        triggerLabel="missing（已失效）"
      />,
    );
    expect(trigger()).toHaveTextContent("missing（已失效）");
    rerender(
      <T3ProjectPicker
        projects={PROJECTS}
        value=""
        onValueChange={vi.fn()}
      />,
    );
    expect(trigger()).toHaveTextContent("选择项目");
  });

  it("打开面板列出项目并聚焦搜索框，行内带 Local · path 副标题", async () => {
    const user = userEvent.setup();
    render(
      <T3ProjectPicker
        projects={PROJECTS}
        value="p1"
        onValueChange={vi.fn()}
      />,
    );
    const listbox = await openPanel(user);
    await waitFor(() =>
      expect(screen.getByLabelText("搜索项目")).toHaveFocus(),
    );
    const options = within(listbox).getAllByRole("option");
    expect(options).toHaveLength(3);
    expect(options[0]).toHaveTextContent("Fast Ship");
    expect(options[0]).toHaveTextContent("Local · /code/fast-ship");
    // 无 workspace_root 的项目不显示副标题。
    expect(options[2]).toHaveTextContent("文档");
    expect(options[2]).not.toHaveTextContent("Local");
    expect(options[0]).toHaveAttribute("aria-selected", "true");
    expect(options[1]).toHaveAttribute("aria-selected", "false");
  });

  it("搜索按标题大小写不敏感过滤，无结果显示空态", async () => {
    const user = userEvent.setup();
    render(
      <T3ProjectPicker
        projects={PROJECTS}
        value=""
        onValueChange={vi.fn()}
      />,
    );
    const listbox = await openPanel(user);
    await user.type(screen.getByLabelText("搜索项目"), "SHIP");
    expect(within(listbox).getAllByRole("option")).toHaveLength(1);
    expect(within(listbox).getByRole("option")).toHaveTextContent("Fast Ship");
    await user.clear(screen.getByLabelText("搜索项目"));
    await user.type(screen.getByLabelText("搜索项目"), "不存在");
    expect(within(listbox).queryAllByRole("option")).toHaveLength(0);
    expect(screen.getByText("无匹配项目")).toBeInTheDocument();
  });

  it("直选 chip 只标到前 9 条可见行", async () => {
    const ten: ComposerProject[] = Array.from({ length: 10 }, (_, i) => ({
      id: `p${i}`,
      title: `Project ${i + 1}`,
    }));
    const user = userEvent.setup();
    render(
      <T3ProjectPicker projects={ten} value="" onValueChange={vi.fn()} />,
    );
    const listbox = await openPanel(user);
    const options = within(listbox).getAllByRole("option");
    expect(options).toHaveLength(10);
    for (let i = 0; i < 9; i++) {
      expect(options[i]).toHaveTextContent(
        new RegExp(`(⌘|Ctrl\\+)${i + 1}`),
      );
    }
    expect(options[9]).not.toHaveTextContent(/⌘|Ctrl\+/);
  });

  it("↑/↓ 环绕移动高亮，Enter 选中高亮行", async () => {
    const onValueChange = vi.fn();
    const user = userEvent.setup();
    render(
      <T3ProjectPicker
        projects={PROJECTS}
        value="p1"
        onValueChange={onValueChange}
      />,
    );
    await openPanel(user);
    // 第一行 ↑ 环绕到最后一行。
    fireEvent.keyDown(window, { key: "ArrowUp" });
    fireEvent.keyDown(window, { key: "Enter" });
    expect(onValueChange).toHaveBeenCalledWith("p3");
    await waitFor(() => expect(screen.queryByRole("listbox")).toBeNull());

    // 重开面板，高亮从第一行重新开始，↓ 后 Enter 选中第二行。
    await user.click(trigger());
    await screen.findByRole("listbox");
    fireEvent.keyDown(window, { key: "ArrowDown" });
    fireEvent.keyDown(window, { key: "Enter" });
    expect(onValueChange).toHaveBeenCalledWith("p2");
    await waitFor(() => expect(screen.queryByRole("listbox")).toBeNull());
  });

  it("⌘N / Ctrl+N 直选第 N 条可见行", async () => {
    const onValueChange = vi.fn();
    const user = userEvent.setup();
    render(
      <T3ProjectPicker
        projects={PROJECTS}
        value=""
        onValueChange={onValueChange}
      />,
    );
    await openPanel(user);
    fireEvent.keyDown(window, { key: "2", metaKey: true });
    expect(onValueChange).toHaveBeenCalledWith("p2");
    await waitFor(() => expect(screen.queryByRole("listbox")).toBeNull());

    await user.click(trigger());
    await screen.findByRole("listbox");
    fireEvent.keyDown(window, { key: "1", ctrlKey: true });
    expect(onValueChange).toHaveBeenCalledWith("p1");
  });

  it("越界数字键不选中但仍拦截默认行为", async () => {
    const onValueChange = vi.fn();
    const user = userEvent.setup();
    render(
      <T3ProjectPicker
        projects={PROJECTS}
        value=""
        onValueChange={onValueChange}
      />,
    );
    await openPanel(user);
    const event = new KeyboardEvent("keydown", {
      key: "9",
      metaKey: true,
      cancelable: true,
    });
    window.dispatchEvent(event);
    expect(event.defaultPrevented).toBe(true);
    expect(onValueChange).not.toHaveBeenCalled();
    // 面板保持打开。
    expect(screen.getByRole("listbox")).toBeInTheDocument();
  });

  it("IME 组合中的 Enter 不触发选择", async () => {
    const onValueChange = vi.fn();
    const user = userEvent.setup();
    render(
      <T3ProjectPicker
        projects={PROJECTS}
        value=""
        onValueChange={onValueChange}
      />,
    );
    await openPanel(user);
    fireEvent.keyDown(window, { key: "ArrowDown", isComposing: true });
    fireEvent.keyDown(window, { key: "Enter", isComposing: true });
    fireEvent.keyDown(window, { key: "Enter", keyCode: 229 });
    expect(onValueChange).not.toHaveBeenCalled();
    fireEvent.keyDown(window, { key: "Enter" });
    expect(onValueChange).toHaveBeenCalledWith("p1");
  });

  it("Esc 关闭面板且事件不外传", async () => {
    const user = userEvent.setup();
    render(
      <T3ProjectPicker
        projects={PROJECTS}
        value=""
        onValueChange={vi.fn()}
      />,
    );
    await openPanel(user);
    const spy = vi.fn();
    document.addEventListener("keydown", spy, true);
    try {
      fireEvent.keyDown(screen.getByLabelText("搜索项目"), { key: "Escape" });
      expect(spy).not.toHaveBeenCalled();
    } finally {
      document.removeEventListener("keydown", spy, true);
    }
    await waitFor(() => expect(screen.queryByRole("listbox")).toBeNull());
  });

  it("点当前项目只关闭面板不回调；点其它项目回调并关闭", async () => {
    const onValueChange = vi.fn();
    const user = userEvent.setup();
    render(
      <T3ProjectPicker
        projects={PROJECTS}
        value="p1"
        onValueChange={onValueChange}
      />,
    );
    let listbox = await openPanel(user);
    await user.click(within(listbox).getByRole("option", { name: /Fast Ship/ }));
    expect(onValueChange).not.toHaveBeenCalled();
    await waitFor(() => expect(screen.queryByRole("listbox")).toBeNull());

    listbox = await openPanel(user);
    await user.click(
      within(listbox).getByRole("option", { name: /Mobile Repo/ }),
    );
    expect(onValueChange).toHaveBeenCalledTimes(1);
    expect(onValueChange).toHaveBeenCalledWith("p2");
    await waitFor(() => expect(screen.queryByRole("listbox")).toBeNull());
  });

  it("disabled 时不可打开", async () => {
    const user = userEvent.setup();
    render(
      <T3ProjectPicker
        projects={PROJECTS}
        value="p1"
        onValueChange={vi.fn()}
        disabled
      />,
    );
    expect(trigger()).toBeDisabled();
    await user.click(trigger());
    expect(screen.queryByRole("listbox")).toBeNull();
  });

  it("空项目列表：触发器显示 placeholder，面板显示无匹配项目", async () => {
    const user = userEvent.setup();
    render(
      <T3ProjectPicker projects={[]} value="" onValueChange={vi.fn()} />,
    );
    expect(trigger()).toHaveTextContent("选择项目");
    await openPanel(user);
    expect(screen.getByText("无匹配项目")).toBeInTheDocument();
  });
});
