import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import type {
  BotMetrics as MetricsData,
  BotRequestMetrics,
} from "@/lib/api/bots";
import { BotMetrics } from "./bot-metrics";

const fullRequest: BotRequestMetrics = {
  request_id: "req-1",
  run_id: "r1",
  ordinal: 1,
  status: "succeeded",
  prompt_tokens: 1000,
  completion_tokens: 200,
  cached_tokens: 800,
  usage_complete: true,
  input_tokens_estimate: 25000,
  input_estimator: "usage_delta/3.5",
  started_at: "2026-10-18T01:00:00Z",
  finished_at: "2026-10-18T01:00:10Z",
  generation_ms: 5000,
};

function makeMetrics(
  request: BotRequestMetrics | null = fullRequest,
  overrides: Partial<MetricsData> = {},
): MetricsData {
  return {
    context_window: 100000,
    max_output_tokens: 8000,
    context_source: "detected",
    context_detail: "GET /models",
    request,
    ...overrides,
  };
}

describe("BotMetrics", () => {
  it("完整数据：速度、上下文占比、缓存命中率齐全且注明口径", () => {
    render(<BotMetrics metrics={makeMetrics()} />);

    // 速度 = 200 tokens / 5s = 40 tok/s。
    const speed = screen.getByTestId("bot-metric-speed");
    expect(speed).toHaveTextContent("速度 40.0 tok/s");
    expect(speed).toHaveAttribute("title", expect.stringContaining("最近一次模型请求"));

    // 上下文 = 25000 / 100000 = 25%，标注为估算并给出估算口径与窗口来源。
    const context = screen.getByTestId("bot-metric-context");
    expect(context).toHaveTextContent("上下文 ≈25%");
    const title = context.getAttribute("title") ?? "";
    expect(title).toContain("估算");
    expect(title).toContain("25,000 / 100,000");
    expect(title).toContain("usage_delta/3.5");
    expect(title).toContain("自动检测");
    expect(title).toContain("GET /models");

    // 缓存 = 800 / 1000 = 80%。
    expect(screen.getByTestId("bot-metric-cache")).toHaveTextContent("缓存 80%");
  });

  it("usage 未收齐时计数不可用：速度与缓存显示「—」，上下文估算仍展示", () => {
    render(
      <BotMetrics
        metrics={makeMetrics({ ...fullRequest, usage_complete: false })}
      />,
    );
    expect(screen.getByTestId("bot-metric-speed")).toHaveTextContent("速度 —");
    expect(screen.getByTestId("bot-metric-cache")).toHaveTextContent("缓存 —");
    // input_tokens_estimate 不依赖 usage 完整性。
    expect(screen.getByTestId("bot-metric-context")).toHaveTextContent("≈25%");
  });

  it("generation_ms 为 0 时速度不可用而非 0", () => {
    render(
      <BotMetrics
        metrics={makeMetrics({ ...fullRequest, generation_ms: 0 })}
      />,
    );
    const speed = screen.getByTestId("bot-metric-speed");
    expect(speed).toHaveTextContent("速度 —");
    expect(speed).not.toHaveTextContent("0");
  });

  it("cached_tokens 为 0 是真实数据，显示 0% 而非「—」", () => {
    render(
      <BotMetrics
        metrics={makeMetrics({ ...fullRequest, cached_tokens: 0 })}
      />,
    );
    expect(screen.getByTestId("bot-metric-cache")).toHaveTextContent("缓存 0%");
  });

  it("request 为 null 时三项都显示「—」", () => {
    render(<BotMetrics metrics={makeMetrics(null)} />);
    expect(screen.getByTestId("bot-metric-speed")).toHaveTextContent("—");
    expect(screen.getByTestId("bot-metric-context")).toHaveTextContent("—");
    expect(screen.getByTestId("bot-metric-cache")).toHaveTextContent("—");
  });

  it("无 metrics（旧服务端）时三项都显示「—」，不显示 0", () => {
    render(<BotMetrics metrics={undefined} />);
    const region = screen.getByTestId("bot-metrics");
    expect(region).toHaveTextContent("速度 —");
    expect(region).toHaveTextContent("上下文 —");
    expect(region).toHaveTextContent("缓存 —");
    expect(region).not.toHaveTextContent("0");
  });

  it("input_tokens_estimate 为 0 显示「—」而不是 ≈0%", () => {
    render(
      <BotMetrics
        metrics={makeMetrics({ ...fullRequest, input_tokens_estimate: 0 })}
      />,
    );
    const context = screen.getByTestId("bot-metric-context");
    expect(context).toHaveTextContent("—");
    expect(context).not.toHaveTextContent("≈");
    // title 如实说明缺失原因，不把 0 当成真实占比。
    expect(context.getAttribute("title")).toContain("暂无上下文占比");
  });

  it("小屏也可见（不再 hidden），只隐藏标签文字保留数值", () => {
    render(<BotMetrics metrics={makeMetrics()} />);
    const region = screen.getByTestId("bot-metrics");
    // 容器在所有尺寸都渲染：不是 hidden，是 flex。
    expect(region).toHaveClass("flex");
    expect(region).not.toHaveClass("hidden");
    // 三个标签文字都是 <sm 隐藏、sm 起显示；数值 span 不带 hidden。
    const labels = region.querySelectorAll("span.hidden.sm\\:inline");
    expect(labels).toHaveLength(3);
    const value = screen.getByTestId("bot-metric-context").querySelector(".font-mono");
    expect(value).not.toHaveClass("hidden");
    // 紧凑排布：不换行、允许收缩，不挤压输入与发送按钮。
    expect(region).toHaveClass("min-w-0");
    expect(region).toHaveClass("whitespace-nowrap");
  });
});
