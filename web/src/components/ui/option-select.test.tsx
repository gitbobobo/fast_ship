import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { vi } from "vitest";
import {
  OptionSelect,
  withMissingOption,
} from "@/components/ui/option-select";

// 回归守护：收起态必须显示 label 而不是内部 value（Base UI 无 items 时会露 value）。
describe("OptionSelect", () => {
  it("收起时显示选中项的 label 而非 value", () => {
    render(
      <OptionSelect
        value="no"
        onValueChange={() => {}}
        options={[
          { value: "no", label: "否（默认）" },
          { value: "yes", label: "是" },
        ]}
      />,
    );

    const trigger = screen.getByRole("combobox");
    expect(trigger).toHaveTextContent("否（默认）");
    expect(trigger).not.toHaveTextContent("no");
  });

  it("value 不在 options 中或有自定义需求时可用 valueLabel 覆盖收起态", () => {
    render(
      <OptionSelect
        value="ghost-project"
        onValueChange={() => {}}
        options={[{ value: "a", label: "项目 A" }]}
        valueLabel="已选项目"
      />,
    );

    const trigger = screen.getByRole("combobox");
    expect(trigger).toHaveTextContent("已选项目");
  });

  it("未选中（空值）时显示 placeholder", () => {
    render(
      <OptionSelect
        value=""
        onValueChange={() => {}}
        options={[{ value: "a", label: "项目 A" }]}
        placeholder="选择环境"
      />,
    );

    expect(screen.getByRole("combobox")).toHaveTextContent("选择环境");
  });

  it("value 为 null 时同样显示 placeholder", () => {
    render(
      <OptionSelect
        value={null}
        onValueChange={() => {}}
        options={[{ value: "a", label: "项目 A" }]}
        placeholder="选择项目"
      />,
    );

    expect(screen.getByRole("combobox")).toHaveTextContent("选择项目");
  });

  it("value 为 undefined 时按受控空值处理，显示 placeholder", () => {
    render(
      <OptionSelect
        value={undefined}
        onValueChange={() => {}}
        options={[{ value: "a", label: "项目 A" }]}
        placeholder="选择项目"
      />,
    );

    expect(screen.getByRole("combobox")).toHaveTextContent("选择项目");
  });

  it("空值时 valueLabel 不盖掉 placeholder", () => {
    render(
      <OptionSelect
        value=""
        onValueChange={() => {}}
        options={[{ value: "a", label: "项目 A" }]}
        placeholder="选择项目"
        valueLabel="已选项目"
      />,
    );

    expect(screen.getByRole("combobox")).toHaveTextContent("选择项目");
    expect(screen.getByRole("combobox")).not.toHaveTextContent("已选项目");
  });

  it("异步选项 pending 时注入占位项显示文案而非原始 ID", () => {
    render(
      <OptionSelect
        value="env-1"
        onValueChange={() => {}}
        options={withMissingOption([], "env-1", "加载中…")}
      />,
    );

    const trigger = screen.getByRole("combobox");
    expect(trigger).toHaveTextContent("加载中…");
    expect(trigger).not.toHaveTextContent("env-1");
  });

  it("value 缺失时注入禁用的占位项，防止误选覆盖联动字段", () => {
    const options = withMissingOption(
      [{ value: "a", label: "项目 A" }],
      "env-1",
      "env-1（已失效）",
    );

    expect(options[0]).toEqual({
      value: "env-1",
      label: "env-1（已失效）",
      disabled: true,
    });
    expect(options).toHaveLength(2);
  });

  it("重选当前值不触发 onValueChange，防止联动字段被同值点击清空", async () => {
    const user = userEvent.setup();
    const onValueChange = vi.fn();
    render(
      <OptionSelect
        value="a"
        onValueChange={onValueChange}
        options={[
          { value: "a", label: "项目 A" },
          { value: "b", label: "项目 B" },
        ]}
      />,
    );

    await user.click(screen.getByRole("combobox"));
    await user.click(await screen.findByRole("option", { name: "项目 A" }));
    expect(onValueChange).not.toHaveBeenCalled();

    await user.click(screen.getByRole("combobox"));
    await user.click(await screen.findByRole("option", { name: "项目 B" }));
    expect(onValueChange).toHaveBeenCalledWith("b");
  });

  it("value 已在 options 中或为空时不注入占位项", () => {
    const base = [{ value: "a", label: "项目 A" }];

    expect(withMissingOption(base, "a", "多余")).toEqual([
      { value: "a", label: "项目 A" },
    ]);
    expect(withMissingOption(base, null, "多余")).toEqual([
      { value: "a", label: "项目 A" },
    ]);
    expect(withMissingOption(base, "", "多余")).toEqual([
      { value: "a", label: "项目 A" },
    ]);
  });
});
