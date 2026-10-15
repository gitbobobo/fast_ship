import { render, screen } from "@testing-library/react";
import { OptionSelect } from "@/components/ui/option-select";

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
});
