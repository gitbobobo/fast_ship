package quota

import "strings"

// factoryCoreLabelPrefix 是早于 Pool 字段的历史快照里 core 池窗口名的前缀。
const factoryCoreLabelPrefix = "Core "

// BackfillWindowPools 给早于 Pool 字段落库的历史快照补池名。
//
// 这些快照里的 Factory 窗口没有 pool，池信息只写在 label 上：「Core 5 小时」
// 表示 core 池，旧版接口的「标准额度」/「高级额度」是标准池与高级池。读路径
// 在投影成响应前补齐，否则前端会把两个池并成一个匿名池，把「标准用尽 + 高级
// 可用」的账号误判成已用尽（下一次成功抓取会写回新格式，但失败退避期间可能
// 停很久）。
//
// 已有 pool 的窗口原样保留；非 Factory 供应商不做任何推断。
func BackfillWindowPools(providerID string, windows []Window) []Window {
	if providerID != "factory" {
		return windows
	}
	out := make([]Window, len(windows))
	copy(out, windows)
	for i := range out {
		if out[i].Pool != "" {
			continue
		}
		switch {
		case strings.HasPrefix(out[i].Label, factoryCoreLabelPrefix):
			out[i].Pool = "Core"
			out[i].Label = strings.TrimPrefix(out[i].Label, factoryCoreLabelPrefix)
		case out[i].Label == "标准额度":
			out[i].Pool = "标准"
		case out[i].Label == "高级额度":
			out[i].Pool = "高级"
		default:
			// 新接口的标准池窗口本来就不带前缀（5 小时 / 每周 / 每月）。
			out[i].Pool = "标准"
		}
	}
	return out
}
