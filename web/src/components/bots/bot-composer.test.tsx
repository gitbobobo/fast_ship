import { fireEvent, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import { BotComposer } from "./bot-composer";

function renderComposer(
  overrides: { text?: string; submitting?: boolean; disabled?: boolean } = {},
) {
  const onSubmit = vi.fn();
  render(
    <BotComposer
      text={overrides.text ?? "你好"}
      onTextChange={() => {}}
      onSubmit={onSubmit}
      submitting={overrides.submitting}
      disabled={overrides.disabled}
    />,
  );
  return {
    onSubmit,
    textarea: screen.getByLabelText("机器人消息内容"),
  };
}

describe("BotComposer 键盘行为", () => {
  it("Enter 发送", () => {
    const { onSubmit, textarea } = renderComposer();
    fireEvent.keyDown(textarea, { key: "Enter" });
    expect(onSubmit).toHaveBeenCalledTimes(1);
  });

  it("Shift+Enter 换行不发送", () => {
    const { onSubmit, textarea } = renderComposer();
    fireEvent.keyDown(textarea, { key: "Enter", shiftKey: true });
    expect(onSubmit).not.toHaveBeenCalled();
  });

  it("输入法组合中的 Enter 不发送（isComposing / keyCode 229）", () => {
    const { onSubmit, textarea } = renderComposer();
    fireEvent.keyDown(textarea, { key: "Enter", isComposing: true });
    expect(onSubmit).not.toHaveBeenCalled();
    // 旧环境用 keyCode 229 标记组合输入，同样不发送。
    fireEvent.keyDown(textarea, { key: "Enter", keyCode: 229 });
    expect(onSubmit).not.toHaveBeenCalled();
  });

  it("空白文本按 Enter 不发送", () => {
    const { onSubmit, textarea } = renderComposer({ text: "   " });
    fireEvent.keyDown(textarea, { key: "Enter" });
    expect(onSubmit).not.toHaveBeenCalled();
  });

  it("submitting 时 Enter 不重复发送", () => {
    const { onSubmit, textarea } = renderComposer({ submitting: true });
    fireEvent.keyDown(textarea, { key: "Enter" });
    expect(onSubmit).not.toHaveBeenCalled();
  });

  it("点击发送按钮仍走表单提交", async () => {
    const { onSubmit } = renderComposer();
    await userEvent.click(screen.getByRole("button", { name: "发送" }));
    expect(onSubmit).toHaveBeenCalledTimes(1);
  });

  it("Enter 拦掉默认换行；Shift+Enter 与组合输入放行给浏览器换行", () => {
    const { textarea } = renderComposer();
    // fireEvent 返回 false 表示事件被 preventDefault——Enter 不换行。
    expect(fireEvent.keyDown(textarea, { key: "Enter" })).toBe(false);
    // 放行时原生默认行为生效，浏览器插入换行。
    expect(fireEvent.keyDown(textarea, { key: "Enter", shiftKey: true })).toBe(
      true,
    );
    expect(
      fireEvent.keyDown(textarea, { key: "Enter", isComposing: true }),
    ).toBe(true);
    expect(fireEvent.keyDown(textarea, { key: "Enter", keyCode: 229 })).toBe(
      true,
    );
  });

  it("Cmd/Ctrl+Enter 沿用同一发送分支；叠加 Shift 则不发送", () => {
    const { onSubmit, textarea } = renderComposer();
    fireEvent.keyDown(textarea, { key: "Enter", metaKey: true });
    fireEvent.keyDown(textarea, { key: "Enter", ctrlKey: true });
    expect(onSubmit).toHaveBeenCalledTimes(2);
    fireEvent.keyDown(textarea, { key: "Enter", metaKey: true, shiftKey: true });
    fireEvent.keyDown(textarea, { key: "Enter", ctrlKey: true, shiftKey: true });
    expect(onSubmit).toHaveBeenCalledTimes(2);
  });

  it("disabled 时 Enter 不发送", () => {
    const { onSubmit, textarea } = renderComposer({ disabled: true });
    fireEvent.keyDown(textarea, { key: "Enter" });
    expect(onSubmit).not.toHaveBeenCalled();
  });
});
