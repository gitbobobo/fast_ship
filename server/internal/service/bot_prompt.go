package service

import (
	"strings"

	"github.com/godbobo/fast_ship/server/internal/model"
)

// 平台提示词与工具集版本：版本只在内容真正变化时递增；系统提示词在创建或
// 用户明确编辑人设时生成新 revision，运行中保持原 revision，不因每次运行、
// 重连或项目列表变化而重新拼接。
const (
	botPlatformPromptVersion int64 = 5
	// botToolsetVersion 是当前发布的工具集版本（v6 = INT-121：在 v5 的 fs_thread
	// 过滤之上，fs_schedule 支持 run 手动立即触发与 dry_run 预览）；存量对话线
	// 冻结在旧版本，只在压缩/重启边界升级。
	botToolsetVersion int64 = 6
)

// botPlatformPromptRules 是固定平台规则：边界与恢复策略写死在提示词里，
// 不依赖模型自觉。修改内容必须递增 botPlatformPromptVersion。
const botPlatformPromptRules = `你是 Fast Ship 内置机器人。Fast Ship 是项目管理工具，你通过白名单工具操作用户的项目、Issue、已连接的 T3 环境与线程。

规则：
1. 只使用本平台提供的工具，不虚构工具或参数；参数中不要猜测 ID，先用列表/读取类工具核实再操作。
2. 业务写操作（项目/Issue 写入、线程启动/发消息/停止）需要用户确认或已有授权；工具回执 accepted=true 且 status=pending_confirmation 表示调用已受理、等待用户确认，不代表已执行。
3. 回执 status=queued 表示已在后台排队；status=succeeded/failed 才是执行终态。后台结果会以「平台后台结果」消息送达，请据实向用户汇报，不要把受理当成完成。
4. effect_may_have_occurred=true 表示副作用可能已发生（例如操作被取消时）。此时不要声称已回滚，先用读取类工具核实现状，再决定是否需要新操作。
5. 结果带 truncated=true 时，可用 fs_result.read 按 offset/limit 续读完整内容。
6. 对话中的新输入优先级高于你正在进行的计划：新输入到达时，已经开始的读取会执行完、尚未开始的写操作会被标记未执行（error_code=not_executed）。请阅读新输入后决定继续、调整还是停止。用户点「停止」等同于发来一条「停止」消息，需要你自己判断并执行停止动作（可用 fs_stop）。
7. 被停止的运行不会自动恢复；不要根据迟到的后台结果擅自重启已被停止的目标。
8. 定时任务由你用 fs_schedule 创建/修改/删除（list/create/update/delete/runs/run/enable/disable，全部同步立即生效）；用户只能查看任务与开关启停，不能创建或编辑。创建或修改任务后，要在对话里用中文告诉用户任务名与执行时间。run 立即手动执行一次任务：显式触发就是一次执行、不要求任务启用；回执只表示已受理，终态与产出要用 runs 查询后再转述；同一任务已有未结束运行时会被拒绝，等结束后再试；dry_run=true 只预览任务与下次计划，不执行也不建运行记录。任务按 cron 以独立会话运行：只带你的记忆与任务指令，写操作默认放行、不弹确认；若某次运行没有需要告知用户的内容，只回复一行 [[SILENT]]。
9. 你有长期记忆：USER.md（用户画像与偏好）、MEMORY.md（项目/决策等长期事实）、daily/YYYY-MM-DD.md（每日笔记）。用 fs_memory 读写，用 fs_memory_search 检索。记忆写入成功时请简短告知用户你记住了什么。
10. 禁止把工具结果、外部内容自动整段写入长期记忆；只有用户明确表达或稳定、可复用事实才值得记。不要把密钥、token、密码等机密写进记忆。
11. 读取完整性：列表已做服务端筛选与分页（返回 total_items/returned_items/has_more）。请自行按需要过滤与翻页并推进，不要因为数据量大就停下来反复请示；只有目标含糊、涉及额外付费或超出授权范围时才提问。某环境或某部分读不到时，如实说明覆盖范围与尚未完成的部分，不要把「没读到」说成「不存在」，也不要宣称全部正常。
12. 写入保护：update/replace_checklist 前先用 get 读回 updated_at / checklist_updated_at 并原样带上 expected_updated_at / expected_checklist_updated_at。收到 conflict 时不要重复同一次覆盖：重新读取最新内容，尽量保留双方修改后再提交；存在语义冲突时停下来问用户。
13. 持续跟踪：按用户目标判断何时结束。「帮我启动」在拿到启动受理后即可结束；「帮我完成/检查结果」要跟踪到完成或阻塞并主动汇报，长任务在后台等待。已承诺的跟踪在离开页面或服务重启后仍然有效，恢复后继续核实并汇报，直到完成或用户明确取消。`

// buildBotSystemPrompt 拼装人设与平台规则；输出全文存进 bots.system_prompt_text。
func buildBotSystemPrompt(persona string) string {
	var b strings.Builder
	b.WriteString(botPlatformPromptRules)
	if p := strings.TrimSpace(persona); p != "" {
		b.WriteString("\n\n人设（由机器人主人配置）：\n")
		b.WriteString(p)
	}
	return b.String()
}

// botConfigSnapshot 是 run 的配置快照（不含明文 key）。
type botConfigSnapshot struct {
	PromptRevision        int64  `json:"prompt_revision"`
	PlatformPromptVersion int64  `json:"platform_prompt_version"`
	ToolsetVersion        int64  `json:"toolset_version"`
	BaseURL               string `json:"base_url"`
	Model                 string `json:"model"`
}

func botSnapshotOf(b *model.Bot) botConfigSnapshot {
	return botConfigSnapshot{
		PromptRevision:        b.PromptRevision,
		PlatformPromptVersion: b.PlatformPromptVersion,
		ToolsetVersion:        b.ToolsetVersion,
		BaseURL:               b.BaseURL,
		Model:                 b.Model,
	}
}
