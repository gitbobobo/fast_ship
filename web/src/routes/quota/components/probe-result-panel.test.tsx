import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { ProbeResultPanel } from "./probe-result-panel";

const OK_RESULT: QuotaProbe = {
  status: "ok",
  plan_name: "Pro",
  windows: [
    {
      kind: "daily",
      label: "每天",
      used_percent: 38,
      used: null,
      limit: null,
      unit: "percent",
      reset_at: null,
    },
  ],
  balance: null,
  note: "",
  error: "",
  detail: "",
};

describe("ProbeResultPanel", () => {
  it("成功时显示套餐名与窗口数", () => {
    render(
      <ProbeResultPanel result={OK_RESULT} error={null} isError={false} />,
    );
    const panel = screen.getByTestId("quota-probe-result");
    expect(panel).toHaveTextContent("凭据有效");
    expect(panel).toHaveTextContent("套餐：Pro");
    expect(panel).toHaveTextContent("1 个额度窗口");
  });

  it("成功但没返回套餐名时显示兜底文案", () => {
    render(
      <ProbeResultPanel
        result={{ ...OK_RESULT, plan_name: "", windows: [] }}
        error={null}
        isError={false}
      />,
    );
    const panel = screen.getByTestId("quota-probe-result");
    expect(panel).toHaveTextContent("已连通，但未返回套餐名");
    expect(panel).not.toHaveTextContent("个额度窗口");
  });

  it("成功且带 note 时一并展示补充说明", () => {
    render(
      <ProbeResultPanel
        result={{ ...OK_RESULT, note: "无 Design Plan 额度，按钱包计费" }}
        error={null}
        isError={false}
      />,
    );
    const panel = screen.getByTestId("quota-probe-result");
    expect(panel).toHaveTextContent("凭据有效");
    expect(panel).toHaveTextContent("无 Design Plan 额度，按钱包计费");
  });

  it("失败时显示服务端原因与 detail 诊断", () => {
    render(
      <ProbeResultPanel
        result={{
          ...OK_RESULT,
          status: "credential_invalid",
          plan_name: "",
          windows: [],
          error: "凭据无效",
          detail: "HTTP 401",
        }}
        error={null}
        isError={false}
      />,
    );
    const panel = screen.getByTestId("quota-probe-result");
    expect(panel).toHaveTextContent("测试失败");
    expect(panel).toHaveTextContent("凭据无效");
    expect(panel).toHaveTextContent("HTTP 401");
  });

  it("请求层失败（无 result）时回退到通用文案", () => {
    render(
      <ProbeResultPanel
        result={undefined}
        error={new Error("network down")}
        isError={true}
      />,
    );
    const panel = screen.getByTestId("quota-probe-result");
    expect(panel).toHaveTextContent("测试失败");
    expect(panel).toHaveTextContent("网络或服务异常");
  });

  it("无结果且无错误时不渲染", () => {
    const { container } = render(
      <ProbeResultPanel result={undefined} error={null} isError={false} />,
    );
    expect(container).toBeEmptyDOMElement();
    expect(screen.queryByTestId("quota-probe-result")).toBeNull();
  });
});
