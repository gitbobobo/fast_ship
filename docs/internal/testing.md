# 测试与竞态检测

## 入口

- `make test-server`：`cd server && go test ./...`，必需检查，全量约 90s。
- `make test-server-race`：`go test -race -timeout 25m ./...`，可选目标，供并发类改动自查。
- `make test-web`、`make test-release`：web 侧检查与发版脚本检查。

## 为什么 -race 不进必需门禁（INT-197）

2026-11-06 在 darwin/arm64（go1.21.4 toolchain go1.27.3）干净 main（ea961698）实测：

| 命令 | 耗时 | 结果 |
| --- | --- | --- |
| `go test -count=1 ./...` | ~1m33s | 通过 |
| `go test -race -count=1 ./...` | ~10m30s | **失败**：`internal/service` 单包 601.966s 越过默认 `-test.timeout=10m` 被 panic 杀掉 |

`-race` 全量比无 race 慢约 7 倍，且瓶颈集中在 `internal/service`：该包测试从不关闭 gorm SQLite 句柄、`BotService.Start` 无对应 `Stop`，goroutine 与内存随用例数累积（panic 转储约 1400+ goroutine，详见 INT-205）。高负载下运行会以超时 panic 收场，被点名的测试随负载漂移——门禁信号不可靠。

### INT-205 修复后的现状（2026-11-07 同机实测，darwin/arm64 go1.27.3）

INT-205 修掉了上述基建债：全部测试句柄走 `t.Cleanup` 关闭（含 selflog Store，补了 `Close`）、`BotService.Stop` 收敛全部后台 goroutine、包级 `TestMain` 泄漏守卫让「测试结束后仍有 connectionOpener / worker / loop 残留」直接 FAIL。同机对照（main d22ed874）：

| 运行 | 耗时 | 结果 |
| --- | --- | --- |
| 修复前 `go test -race -count=1 ./internal/service/` | 525s | 泄漏守卫报 1458 connectionOpener + 100 workerLoop + 5 maintWorkerLoop + 5 条 loop 残留；另有既有用例在服务器 goroutine 回调里 `t.Fatalf` panic 整包（同 PR 修复） |
| 修复后连跑 5 轮 | 504s / 557s / 680s / 690s / 763s | 全部通过，守卫零残留（末两轮为评审修复后的最终源码，其余三轮为评审修复前） |

结论：

- **门禁价值在确定性而非速度**：-race 的开销主要在插桩，句柄/goroutine 泄漏的消除在本机负载噪声（±30%）下体现不出可靠的耗时下降；但它让「超时 panic 随负载漂移、点名测试漂移」的失效模式失去土壤——最差实测 763s 仍越过默认 10m，所以 `-race` 保持**可选目标**，显式 `-timeout` 兑底，是否纳入必需检查仍由 INT-197 评估。
- 泄漏守卫是常驻回归门：任何新测试忘了关句柄或起了不受 Stop 管的后台 goroutine，包会直接 FAIL 并打出样本栈。
- 不设跳过名单。

### INT-200 超时重新定标（2026-11-15，linux/amd64 12 核）

上表 504-763s 基线为 darwin/arm64 且不含其后合入的 `TestRoundBudgetTwelveLargeScreenshots`（单测 ~160s，INT-193）。套件增长后同机 4 次独立全包 `-race` 实测 1023~1097s（零 race、零超时、方差 <7%）：Round 单测 ~160s、Bot 族 ~385s（INT-232 对照证实 11-07 时点即 ~383s）均为新功能用例的正当成本，20m 余量仅 100~180s。经方向顾问审计，`test-server-race` 超时按执行预算重新定标为 **25m**（对当前最差结果余量 27%，判定线：单包 ≤ 25m 的 80%），不随套件增长自动上调；确认这是新决策，不是对 INT-200 原共识第②条的追溯放宽。
