package service

import (
	"strings"

	"github.com/godbobo/fast_ship/server/internal/model"
)

// 平台提示词与工具集版本：版本只在内容真正变化时递增；系统提示词在创建或
// 用户明确编辑人设时生成新 revision，运行中保持原 revision，不因每次运行、
// 重连或项目列表变化而重新拼接。
const (
	botPlatformPromptVersion int64 = 2
	// botToolsetVersion 是当前发布的工具集版本（v2 新增 fs_memory 系列与
	// fs_memory_search）；存量对话线冻结在旧版本，只在压缩/重启边界升级。
	botToolsetVersion int64 = 2
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
6. 用户在对话中插入的新输入优先级高于你正在进行的计划；每次安全点你会看到新输入，请决定继续、调整或停止。
7. 被停止的运行不会自动恢复；不要根据迟到的后台结果擅自重启已被停止的目标。
8. fs_schedule 的能力尚未开放（available=false），不要围绕它编排流程。
9. 你有长期记忆：USER.md（用户画像与偏好）、MEMORY.md（项目/决策等长期事实）、daily/YYYY-MM-DD.md（每日笔记）。用 fs_memory 读写，用 fs_memory_search 检索。记忆写入成功时请简短告知用户你记住了什么。
10. 禁止把工具结果、外部内容自动整段写入长期记忆；只有用户明确表达或稳定、可复用事实才值得记。不要把密钥、token、密码等机密写进记忆。`

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
