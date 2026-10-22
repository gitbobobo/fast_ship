package service

import (
	"github.com/godbobo/fast_ship/server/internal/catalog"
	"github.com/godbobo/fast_ship/server/internal/model"
)

// botCostSourceCatalog 是目前唯一的计价来源：内置 models.dev 目录单价。
const botCostSourceCatalog = "catalog"

// botCostEstimate 按目录单价估算单次模型请求成本（USD，INT-139）。
// 返回 false 的情形一律由调用方落 NULL——目录无价、自定义 provider、模型
// 不在目录、缺 input/output 单价，缺失 ≠ 0，展示层整项隐藏。
//
// 计价拆分（cached_tokens ⊆ prompt_tokens，是命中的输入）：
//   - 未命中缓存的输入 (prompt - cached) 按 cost.input；
//   - 缓存命中输入按 cost.cache_read，缺失回退 cost.input——不提供缓存计价
//     的厂商对命中输入按普通输入计费；
//   - 输出按 cost.output。
//
// cache_write（缓存写入费）上游极少提供，命中输入已覆盖读取侧，不单独计。
func botCostEstimate(catalogID, modelID string, prompt, cached, completion int64) (float64, bool) {
	if catalogID == "" || catalogID == catalog.CustomCatalogID {
		return 0, false
	}
	m, ok := catalog.FindModel(catalogID, modelID)
	if !ok || m.Cost == nil || m.Cost.Input == nil || m.Cost.Output == nil {
		return 0, false
	}
	uncached := prompt - cached
	if uncached < 0 {
		uncached = 0
	}
	read := *m.Cost.Input
	if m.Cost.CacheRead != nil {
		read = *m.Cost.CacheRead
	}
	usd := (float64(uncached)**m.Cost.Input + float64(cached)*read + float64(completion)**m.Cost.Output) / 1e6
	return usd, true
}

// botRequestCost 按 run 冻结快照里的目录归属与模型计价（INT-139）：历史
// run 的单价按派发时目录解析，用户换 provider 后旧账不漂到新目录。
// 请求没报 usage 时不可计价（返回 false）。
func botRequestCost(run *model.BotRun, bot *model.Bot, reply *botModelReply) (float64, bool) {
	if run == nil || reply == nil || !reply.UsageComplete {
		return 0, false
	}
	return botCostEstimate(runCatalogIDOf(run, bot), runModelOf(run, bot),
		reply.PromptTokens, reply.CachedTokens, reply.CompletionTokens)
}

// addToFloatPtr 同 addToPtr 的浮点版：首个值建指针，保 NULL 与 0 的区分。
func addToFloatPtr(p *float64, v float64) *float64 {
	if p == nil {
		return &v
	}
	*p += v
	return p
}
