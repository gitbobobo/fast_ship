# 测试与竞态检测

## 入口

- `make test-server`：`cd server && go test ./...`，必需检查，全量约 90s。
- `make test-server-race`：`go test -race -timeout 20m ./...`，可选目标，供并发类改动自查。
- `make test-web`、`make test-release`：web 侧检查与发版脚本检查。

## 为什么 -race 不进必需门禁（INT-197）

2026-11-06 在 darwin/arm64（go1.21.4 toolchain go1.27.3）干净 main（ea961698）实测：

| 命令 | 耗时 | 结果 |
| --- | --- | --- |
| `go test -count=1 ./...` | ~1m33s | 通过 |
| `go test -race -count=1 ./...` | ~10m30s | **失败**：`internal/service` 单包 601.966s 越过默认 `-test.timeout=10m` 被 panic 杀掉 |

`-race` 全量比无 race 慢约 7 倍，且瓶颈集中在 `internal/service`：该包测试从不关闭 gorm SQLite 句柄、`BotService.Start` 无对应 `Stop`，goroutine 与内存随用例数累积（panic 转储约 1400+ goroutine，详见 INT-205）。高负载下运行会以超时 panic 收场，被点名的测试随负载漂移——门禁信号不可靠。

`internal/service` 恰恰是并发敏感包，「只对敏感包启用 -race」省不下它的成本，等价于全量。所以结论：

- 现阶段 `-race` 只做**可选目标** `make test-server-race`，显式 `-timeout 20m` 兜底，不挂进 `test`/`test-server`。
- 修完 INT-205（测试关闭 DB、`BotService.Stop`）后重测全量 `-race` 耗时，再评估是否纳入必需检查。
- 不设跳过名单。
