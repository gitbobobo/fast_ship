package service

import (
	"fmt"
	"os"
	"runtime"
	"strings"
	"testing"
	"time"
)

// botLeakMarkers 列出「全包测试结束时必须归零」的后台 goroutine 标识：
// database/sql 连接开启器（每个未关闭的 sql.DB 恰好一个）与 BotService 的
// worker / 周期 loop / 主线槽。历史上 service 包测试从不关闭 SQLite 句柄、
// BotService.Start 无对应 Stop，全量 -race 结束时残留上千 goroutine，拖长
// 耗时并偶发越过门禁超时（INT-205）。
var botLeakMarkers = []string{
	"database/sql.(*DB).connectionOpener",
	"service.(*BotService).workerLoop",
	"service.(*BotService).maintWorkerLoop",
	"service.(*BotService).scanLoop",
	"service.(*BotService).nightlyLoop",
	"service.(*BotService).scheduleLoop",
	"service.(*BotService).budgetEvalLoop",
	"service.(*BotService).usageRollupLoop",
	"service.(*BotService).runBotSlot",
	"service.(*BotService).runScheduleSlot",
	"service.(*BotService).runWindowProbe",
	"service.(*BotService).scanUsageRollupOnce",
}

// TestMain 在全包测试结束后做 goroutine 泄漏守卫：给正在收尾的 goroutine
// 一个短暂收敛窗口，仍不归零则让整包失败并打印各类计数。用例本身失败时
// 泄漏多半是失败的伴生结果，只附报告，保住原始退出码。
func TestMain(m *testing.M) {
	code := m.Run()
	if code != 0 {
		reportBotLeak("INT-205 泄漏守卫（用例已失败，仅报告）")
		os.Exit(code)
	}
	deadline := time.Now().Add(5 * time.Second)
	counts, truncated := countBotLeakGoroutines()
	for (totalOf(counts) > 0 || truncated) && time.Now().Before(deadline) {
		time.Sleep(200 * time.Millisecond)
		counts, truncated = countBotLeakGoroutines()
	}
	var leaked []string
	for _, marker := range botLeakMarkers {
		if n := counts[marker]; n > 0 {
			leaked = append(leaked, fmt.Sprintf("  %s x%d\n%s", marker, n, sampleStack(marker)))
		}
	}
	if truncated {
		leaked = append(leaked, "  goroutine 栈缓冲 16MB 被截断，计数不可信，按泄漏处理")
	}
	if len(leaked) > 0 {
		fmt.Fprintf(os.Stderr, "INT-205 泄漏守卫：测试结束后仍有后台 goroutine 存活：\n%s\n", strings.Join(leaked, "\n"))
		os.Exit(1)
	}
	os.Exit(0)
}

// sampleStack 取一个含该标识的 goroutine 栈样本（截到 40 行）：报告泄漏者
// 是谁，不靠猜。
func sampleStack(marker string) string {
	buf := make([]byte, 16<<20)
	n := runtime.Stack(buf, true)
	for _, block := range strings.Split(string(buf[:n]), "\n\n") {
		if strings.Contains(block, marker) {
			lines := strings.Split(block, "\n")
			if len(lines) > 40 {
				lines = append(lines[:40], "  …（截断）")
			}
			return strings.Join(lines, "\n")
		}
	}
	return ""
}

func totalOf(counts map[string]int) int {
	total := 0
	for _, n := range counts {
		total += n
	}
	return total
}

// countBotLeakGoroutines 抓全量 goroutine 栈，按块统计各标识的存活数。
// 返回值末位是截断标记：runtime.Stack 在缓冲不足时静默截断，截断下的计数
// 不可信，守卫按可疑处理（宁可误报也不漏报）。
func countBotLeakGoroutines() (map[string]int, bool) {
	buf := make([]byte, 16<<20)
	n := runtime.Stack(buf, true)
	truncated := n >= len(buf)
	counts := make(map[string]int, len(botLeakMarkers))
	for _, block := range strings.Split(string(buf[:n]), "\n\n") {
		for _, marker := range botLeakMarkers {
			if strings.Contains(block, marker) {
				counts[marker]++
			}
		}
	}
	return counts, truncated
}

func reportBotLeak(header string) {
	counts, _ := countBotLeakGoroutines()
	for _, marker := range botLeakMarkers {
		if counts[marker] > 0 {
			fmt.Fprintf(os.Stderr, "%s: %s x%d\n", header, marker, counts[marker])
		}
	}
}
