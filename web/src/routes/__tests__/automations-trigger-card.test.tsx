import { useState } from "react";
import { fireEvent, render, screen } from "@testing-library/react";
import { vi } from "vitest";
import { TriggerCard } from "@/routes/automations/trigger-card";
import type { TriggerForm } from "@/lib/automations/form-model";

vi.mock("@/lib/hooks/use-projects", () => ({
  useProjects: vi.fn(() => ({
    data: { items: [] },
    isLoading: false,
    isError: false,
  })),
}));

const prTrigger = (over: Partial<TriggerForm> = {}): TriggerForm => ({
  key: "k-1",
  trigger_type: "github_pr_synchronize",
  cron_expression: "",
  timezone: "",
  issue_project_id: null,
  event_filter: null,
  model_selections: [],
  debounce_seconds: 60,
  ...over,
});

// 受控宿主：onChange 写回 state，与编辑页里的表单行为一致。
function Harness({ onChange }: { onChange?: (next: TriggerForm) => void }) {
  const [trigger, setTrigger] = useState(() => prTrigger());
  return (
    <TriggerCard
      trigger={trigger}
      providers={[]}
      t3Project={undefined}
      onChange={(next) => {
        setTrigger(next);
        onChange?.(next);
      }}
      onRemove={() => {}}
    />
  );
}

const debounceInput = () =>
  screen.getByLabelText("防抖窗口（秒）") as HTMLInputElement;

describe("TriggerCard PR 防抖输入", () => {
  it("清空输入框保留原值（不会静默变成 0 = 立即开审）", () => {
    render(<Harness />);
    expect(debounceInput().value).toBe("60");

    fireEvent.change(debounceInput(), { target: { value: "" } });

    expect(debounceInput().value).toBe("60");
  });

  it("合法值照常写入（0 也算合法），非法值不写入", () => {
    const onChange = vi.fn();
    render(<Harness onChange={onChange} />);

    fireEvent.change(debounceInput(), { target: { value: "120" } });
    expect(debounceInput().value).toBe("120");
    expect(onChange).toHaveBeenCalledTimes(1);
    expect(onChange.mock.calls[0][0].debounce_seconds).toBe(120);

    fireEvent.change(debounceInput(), { target: { value: "0" } });
    expect(debounceInput().value).toBe("0");
    expect(onChange.mock.calls[1][0].debounce_seconds).toBe(0);
    onChange.mockClear();

    // 非数字：Number() 得到 NaN，必须忽略而不是写进表单。
    fireEvent.change(debounceInput(), { target: { value: "abc" } });
    expect(onChange).not.toHaveBeenCalled();
    expect(debounceInput().value).toBe("0");
  });
});
