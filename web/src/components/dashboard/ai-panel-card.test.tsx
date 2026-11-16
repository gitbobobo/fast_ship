import { render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { AiPanelCard } from "@/components/dashboard/ai-panel-card";

describe("AiPanelCard", () => {
  it("renders per-env rows, review buckets and bots stats", () => {
    render(
      <MemoryRouter>
        <AiPanelCard
          ai={{
            t3: {
              environments: [
                {
                  environment_id: "env-1",
                  environment_name: "工作站",
                  running: 2,
                  waiting: 1,
                  queued: 1,
                  readable: true,
                },
                {
                  environment_id: "env-2",
                  environment_name: "离线机",
                  running: 1,
                  waiting: 3,
                  queued: 0,
                  readable: true,
                },
              ],
            },
            reviews: {
              passed: 3,
              blocked: 1,
              errored: 1,
              in_flight: 2,
              pass_rate: 0.75,
            },
            bots: {
              bot_count: 1,
              timezone: "Asia/Shanghai",
              cost: null,
              total_requests: 12,
              total_tokens: 3456,
              next_schedule: {
                bot_id: "b1",
                bot_name: "值夜机器人",
                task_name: "晚报",
                next_run_at: "2026-05-30T12:00:00Z",
              },
            },
          }}
        />
      </MemoryRouter>,
    );

    expect(screen.getByTestId("ai-panel-card")).toBeInTheDocument();
    // K>0 括号注记包含关系；K=0 维持「运行 N · 等待 M」（panel P13）
    expect(screen.getByText("运行 2（含排队 1）· 等待 1")).toBeInTheDocument();
    expect(screen.getByText("运行 1 · 等待 3")).toBeInTheDocument();
    // 通过率 = 3/(3+1) = 75%
    expect(screen.getByText("通过率 75%")).toBeInTheDocument();
    expect(screen.getByText("Tokens · 请求（1 个机器人）")).toBeInTheDocument();
    expect(screen.getByText(/3,456 · 12/)).toBeInTheDocument();
    // cost=null 是缺失不是 0：显示「未计价」而不是 $0.00
    expect(screen.getByText("未计价")).toBeInTheDocument();
    expect(screen.getByText(/下次任务/)).toBeInTheDocument();
    expect(screen.getByText(/晚报 · 值夜机器人/)).toBeInTheDocument();
  });

  it("keeps healthy env rows visible when the block is partially readable", () => {
    // 后端可产出形态：complete=false 时载荷里的不可读行必须带 readable:false；
    // 可读环境的行不能被整块短路隐藏（panel P3）。
    render(
      <MemoryRouter>
        <AiPanelCard
          ai={{
            t3: {
              complete: false,
              environments: [
                {
                  environment_id: "env-1",
                  environment_name: "工作站",
                  running: 2,
                  waiting: 1,
                  queued: 1,
                  readable: true,
                },
                {
                  environment_id: "env-2",
                  environment_name: "离线机",
                  running: 0,
                  waiting: 0,
                  queued: 0,
                  readable: false,
                },
              ],
            },
          }}
        />
      </MemoryRouter>,
    );

    // 健康行仍在（含排队括号注记），不可读行标「本轮不可读」，区块头带琥珀色降级徽章
    expect(screen.getByText("运行 2（含排队 1）· 等待 1")).toBeInTheDocument();
    expect(screen.getByText("本轮不可读")).toBeInTheDocument();
    expect(screen.getByText("部分环境暂不可用")).toBeInTheDocument();
    expect(screen.queryByText("暂不可用")).not.toBeInTheDocument();
  });

  it("shows 暂不可用 only when degraded with no environment rows", () => {
    render(
      <MemoryRouter>
        <AiPanelCard
          ai={{
            t3: { complete: false, environments: [] },
          }}
        />
      </MemoryRouter>,
    );

    expect(screen.getByTestId("ai-t3-section")).toHaveTextContent("暂不可用");
  });

  it("hides the pass rate when its denominator is zero and shows the empty bots state", () => {
    render(
      <MemoryRouter>
        <AiPanelCard
          ai={{
            reviews: {
              passed: 0,
              blocked: 0,
              errored: 0,
              in_flight: 1,
              pass_rate: null,
            },
            bots: {
              bot_count: 0,
              timezone: "UTC",
              cost: null,
              total_requests: 0,
              total_tokens: 0,
            },
          }}
        />
      </MemoryRouter>,
    );

    expect(screen.queryByText(/通过率/)).not.toBeInTheDocument();
    expect(screen.getByText("未配置机器人")).toBeInTheDocument();
    expect(screen.getByText("近 7 天（按你的时区）")).toBeInTheDocument();
  });

  it("renders quota mini bars with remaining percent and status badges", () => {
    render(
      <MemoryRouter>
        <AiPanelCard
          ai={{
            quota: {
              accounts: [
                {
                  id: "acc-1",
                  label: "主账号",
                  provider: "claude",
                  provider_name: "Claude",
                  credential_hint: "…ab12",
                  status: "ok",
                  note: "",
                  plan_name: "",
                  last_error: "",
                  created_at: "2026-05-01T00:00:00Z",
                  updated_at: "2026-05-01T00:00:00Z",
                  fetched_at: null,
                  last_success_at: null,
                  balance: null,
                  windows: [
                    {
                      kind: "five_hour",
                      label: "五小时",
                      pool: "",
                      used: 4,
                      limit: 10,
                      unit: "percent",
                      used_percent: 40,
                      reset_at: null,
                    },
                  ],
                },
                {
                  id: "acc-2",
                  label: "备用号",
                  provider: "codex",
                  provider_name: "Codex",
                  credential_hint: "…cd34",
                  status: "credential_invalid",
                  note: "",
                  plan_name: "",
                  last_error: "",
                  created_at: "2026-05-01T00:00:00Z",
                  updated_at: "2026-05-01T00:00:00Z",
                  fetched_at: null,
                  last_success_at: null,
                  balance: null,
                  windows: [],
                },
              ],
            },
          }}
        />
      </MemoryRouter>,
    );

    expect(screen.getByTestId("ai-quota-section")).toBeInTheDocument();
    // 剩余百分比由前端 accountRemaining 推导：100-40=60%
    expect(screen.getByText("60%")).toBeInTheDocument();
    // 快照旧/失效状态与徽章一起展示
    expect(screen.getByText("凭据失效")).toBeInTheDocument();
    expect(screen.getAllByTestId("dashboard-quota-bar")).toHaveLength(2);
  });
});
