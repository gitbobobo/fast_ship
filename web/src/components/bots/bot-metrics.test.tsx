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
  // null 表示「无最近请求」；契约的 request 可选不可空，这里归一成 undefined
  request: BotRequestMetrics | null = fullRequest,
  overrides: Partial<MetricsData> = {},
): MetricsData {
  return {
    context_window: 100000,
    max_output_tokens: 8000,
    context_source: "detected",
    context_detail: "GET /models",
    request: request ?? undefined,
    ...overrides,
  };
}

// 与 bot-metrics.tsx 的 RINGS 同一几何：外环缓存、内环上下文。
const CACHE_C = 2 * Math.PI * 8;
const CONTEXT_C = 2 * Math.PI * 4.5;

/** 进度弧：带 stroke-dashoffset 的 circle；缺失 / 0% 时不渲染。 */
function arcOf(testId: string) {
  return screen.getByTestId(testId).querySelector("circle[stroke-dashoffset]");
}

/** 轨道：环里第一层 circle。 */
function trackOf(testId: string) {
  return screen.getByTestId(testId).querySelector("circle");
}

/** 口径与数值放在 <title> 里供屏幕阅读器与测试读取。 */
function titleOf(testId: string) {
  return screen.getByTestId(testId).querySelector("title")?.textContent ?? "";
}

describe("BotMetrics", () => {
  it("完整数据：双环取值正确（外缓存 80%、内上下文 25%），速度 40.0 tok/s 且不再有「速度」文案", () => {
    render(<BotMetrics metrics={makeMetrics()} />);

    // 速度 = 200 tokens / 5s = 40 tok/s；可见文本只有数值与单位。
    const speed = screen.getByTestId("bot-metric-speed");
    expect(speed).toHaveTextContent("40.0 tok/s");
    expect(speed).toHaveAttribute(
      "title",
      expect.stringContaining("最近一次模型请求"),
    );
    expect(speed.textContent).not.toContain("速度");
    expect(screen.getByTestId("bot-metrics")).not.toHaveTextContent("速度");

    // 外环 = 缓存命中率：r=8、浅灰弧，80% → offset = C × 0.2。
    expect(trackOf("bot-metric-cache")?.getAttribute("r")).toBe("8");
    const cacheArc = arcOf("bot-metric-cache");
    expect(cacheArc).not.toBeNull();
    expect(cacheArc?.getAttribute("class")).toContain(
      "stroke-muted-foreground/70",
    );
    expect(Number(cacheArc?.getAttribute("stroke-dashoffset"))).toBeCloseTo(
      CACHE_C * 0.2,
      6,
    );
    expect(titleOf("bot-metric-cache")).toContain("缓存命中率 80%");
    expect(titleOf("bot-metric-cache")).toContain(
      "cached_tokens / prompt_tokens",
    );

    // 内环 = 上下文占比：r=4.5、更浅的灰，25% → offset = C × 0.75；
    // title 保留完整口径（估算、tokens 数、估算口径、窗口来源、细节）。
    expect(trackOf("bot-metric-context")?.getAttribute("r")).toBe("4.5");
    const contextArc = arcOf("bot-metric-context");
    expect(contextArc).not.toBeNull();
    expect(contextArc?.getAttribute("class")).toContain(
      "stroke-muted-foreground/45",
    );
    expect(Number(contextArc?.getAttribute("stroke-dashoffset"))).toBeCloseTo(
      CONTEXT_C * 0.75,
      6,
    );
    const title = titleOf("bot-metric-context");
    expect(title).toContain("上下文占比 ≈25%");
    expect(title).toContain("估算");
    expect(title).toContain("25,000 / 100,000");
    expect(title).toContain("usage_delta/3.5");
    expect(title).toContain("自动检测");
    expect(title).toContain("GET /models");

    // 尺寸与输入框那一行文字相称，不把输入框撑高。
    const svg = screen.getByTestId("bot-metrics").querySelector("svg");
    expect(svg).toHaveAttribute("width", "20");
    expect(svg).toHaveAttribute("height", "20");
  });

  it("usage 未收齐时缓存与速度不可用：虚线轨道、无进度弧，上下文不受影响", () => {
    render(
      <BotMetrics
        metrics={makeMetrics({ ...fullRequest, usage_complete: false })}
      />,
    );
    expect(screen.getByTestId("bot-metric-speed").textContent).toBe("—");
    expect(arcOf("bot-metric-cache")).toBeNull();
    // 缺失画虚线轨道，不与「真实 0%」混淆。
    expect(trackOf("bot-metric-cache")?.getAttribute("stroke-dasharray")).toBe(
      "3 3",
    );
    expect(titleOf("bot-metric-cache")).toContain("暂无缓存命中率");
    // input_tokens_estimate 不依赖 usage 完整性，上下文环仍画出弧。
    expect(arcOf("bot-metric-context")).not.toBeNull();
    expect(titleOf("bot-metric-context")).toContain("≈25%");
  });

  it("generation_ms 为 0 时速度不可用而非 0", () => {
    render(
      <BotMetrics metrics={makeMetrics({ ...fullRequest, generation_ms: 0 })} />,
    );
    const speed = screen.getByTestId("bot-metric-speed");
    expect(speed.textContent).toBe("—");
    expect(speed.textContent).not.toContain("0");
  });

  it("cached_tokens 为 0 是真实 0%：实线轨道、title 写明 0%，与缺失的虚线可区分", () => {
    render(
      <BotMetrics metrics={makeMetrics({ ...fullRequest, cached_tokens: 0 })} />,
    );
    expect(trackOf("bot-metric-cache")?.getAttribute("stroke-dasharray")).toBeNull();
    // 0% 不画进度弧（round linecap 会在零长度弧上画出圆点）。
    expect(arcOf("bot-metric-cache")).toBeNull();
    expect(titleOf("bot-metric-cache")).toContain("缓存命中率 0%");
  });

  it("request 为 null 时双环与速度都不可用，不出现 0", () => {
    render(<BotMetrics metrics={makeMetrics(null)} />);
    expect(titleOf("bot-metric-cache")).toContain("暂无");
    expect(titleOf("bot-metric-context")).toContain("暂无");
    expect(arcOf("bot-metric-cache")).toBeNull();
    expect(arcOf("bot-metric-context")).toBeNull();
    expect(screen.getByTestId("bot-metric-speed").textContent).toBe("—");
    expect(screen.getByTestId("bot-metrics").textContent).not.toContain("0");
  });

  it("无 metrics（旧服务端）时全部不可用、不显示 0，aria-label 汇总为不可用", () => {
    render(<BotMetrics metrics={undefined} />);
    expect(titleOf("bot-metric-cache")).toContain("暂无");
    expect(titleOf("bot-metric-context")).toContain("暂无");
    expect(screen.getByTestId("bot-metric-speed").textContent).toBe("—");
    expect(screen.getByTestId("bot-metrics").textContent).not.toContain("0");
    expect(screen.getByTestId("bot-metrics").querySelector("svg")).toHaveAttribute(
      "aria-label",
      expect.stringContaining("缓存命中率不可用"),
    );
  });

  it("input_tokens_estimate 为 0 时上下文不可用：虚线轨道而非 ≈0%", () => {
    render(
      <BotMetrics
        metrics={makeMetrics({ ...fullRequest, input_tokens_estimate: 0 })}
      />,
    );
    expect(trackOf("bot-metric-context")?.getAttribute("stroke-dasharray")).toBe(
      "3 3",
    );
    expect(arcOf("bot-metric-context")).toBeNull();
    expect(titleOf("bot-metric-context")).toContain("暂无上下文占比");
    // 缓存环不受影响。
    expect(arcOf("bot-metric-cache")).not.toBeNull();
  });

  it("估算超出窗口时占比夹紧到 100%（不画出过量弧）", () => {
    render(
      <BotMetrics
        metrics={makeMetrics({
          ...fullRequest,
          input_tokens_estimate: 250000,
        })}
      />,
    );
    const arc = arcOf("bot-metric-context");
    expect(Number(arc?.getAttribute("stroke-dashoffset"))).toBeCloseTo(0, 6);
    expect(titleOf("bot-metric-context")).toContain("上下文占比 ≈100%");
  });

  it("窗口未知时只有上下文不可用，缓存与速度照常", () => {
    render(<BotMetrics metrics={makeMetrics(fullRequest, { context_window: 0 })} />);
    expect(arcOf("bot-metric-context")).toBeNull();
    expect(titleOf("bot-metric-context")).toContain("暂无上下文占比");
    expect(arcOf("bot-metric-cache")).not.toBeNull();
    expect(screen.getByTestId("bot-metric-speed")).toHaveTextContent(
      "40.0 tok/s",
    );
  });

  it("cost_estimate 非空时显示 ≈$x 成本（INT-139），为空时整项隐藏", () => {
    render(
      <BotMetrics
        metrics={makeMetrics({ ...fullRequest, cost_estimate: 0.0002112 })}
      />,
    );
    const cost = screen.getByTestId("bot-metric-cost");
    expect(cost).toHaveTextContent("≈$0.0002");
    expect(cost).toHaveAttribute("title", expect.stringContaining("目录单价"));
  });

  it("cost_estimate 为 null 时不渲染成本项（缺失 ≠ 0）", () => {
    render(
      <BotMetrics
        metrics={makeMetrics({ ...fullRequest, cost_estimate: null })}
      />,
    );
    expect(screen.queryByTestId("bot-metric-cost")).toBeNull();
    expect(screen.getByTestId("bot-metrics").textContent).not.toContain("$");
  });

  it("有数据时 aria-label 汇总两项可读值", () => {
    render(<BotMetrics metrics={makeMetrics()} />);
    expect(screen.getByTestId("bot-metrics").querySelector("svg")).toHaveAttribute(
      "aria-label",
      "模型指标：缓存命中率 80%，上下文占比 ≈25%",
    );
  });
});
