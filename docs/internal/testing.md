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

### INT-205 修复后的现状（2026-11-07 同机实测，darwin/arm64 go1.27.3）

INT-205 修掉了上述基建债：全部测试句柄走 `t.Cleanup` 关闭（含 selflog Store，补了 `Close`）、`BotService.Stop` 收敛全部后台 goroutine、包级 `TestMain` 泄漏守卫让「测试结束后仍有 connectionOpener / worker / loop 残留」直接 FAIL。同机对照（main d22ed874）：

| 运行 | 耗时 | 结果 |
| --- | --- | --- |
| 修复前 `go test -race -count=1 ./internal/service/` | 525s | 泄漏守卫报 1458 connectionOpener + 100 workerLoop + 5 maintWorkerLoop + 5 条 loop 残留；另有既有用例在服务器 goroutine 回调里 `t.Fatalf` panic 整包（同 PR 修复） |
| 修复后连跑 3 次 | 504s / 557s / 680s | 全部通过，守卫零残留 |

结论：

- **门禁价值在确定性而非速度**：-race 的开销主要在插桩，句柄/goroutine 泄漏的消除在本机负载噪声（±30%）下体现不出可靠的耗时下降；但它让「超时 panic 随负载漂移、点名测试漂移」的失效模式失去土壤——最差实测 680s 仍越过默认 10m，所以 `-race` 保持**可选目标**，显式 `-timeout 20m` 兜底（20m 内余量充足），是否纳入必需检查仍由 INT-197 评估。
- 泄漏守卫是常驻回归门：任何新测试忘了关句柄或起了不受 Stop 管的后台 goroutine，包会直接 FAIL 并打出样本栈。
- 不设跳过名单。
