package service

import (
	"encoding/json"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/godbobo/fast_ship/server/internal/pkg/errs"
)

// 结果瘦身与截断：先把业务响应投影成工具专用精简对象（适配层已完成），
// 再序列化、估算。超过预算时保存完整精简结果，回给模型结构化预览；
// fs_result.read 的 offset/limit 以 Unicode 字符数计，避免切断 UTF-8。

// botResultBudgetChars 预算：约 4k token（chars/4 估算）。任意兼容模型无法
// 保证统一 tokenizer，阈值与工具占用都按估算处理。
const botResultBudgetChars = 16000

const botResultReadDefaultLimit = 4000
const botResultReadMaxLimit = 16000

// botToolOutcome 是一次工具执行的统一产出：完整结果、给模型的预览与统计。
type botToolOutcome struct {
	ResultJSON  string // 完整精简结果（总是保存，供 fs_result.read 续读）
	PreviewJSON string // 进入模型上下文的受限结果
	ResultChars int64
	Truncated   bool
}

// botBuildOutcome 由执行结果构造产出；执行失败时给模型结构化错误。
func botBuildOutcome(callID string, result interface{}, execErr error) botToolOutcome {
	if execErr != nil {
		errPayload := map[string]interface{}{
			"call_id": callID,
			"error":   botErrMessage(execErr),
		}
		raw, _ := json.Marshal(errPayload)
		return botToolOutcome{ResultJSON: string(raw), PreviewJSON: string(raw), ResultChars: int64(utf8.RuneCountInString(string(raw)))}
	}
	raw, err := json.Marshal(result)
	if err != nil {
		raw, _ = json.Marshal(map[string]interface{}{"call_id": callID, "error": "result marshal failed"})
	}
	return botBuildOutcomeRaw(callID, string(raw), nil)
}

// botBuildOutcomeRaw 同 botBuildOutcome，作用于已序列化的结果字符串。
func botBuildOutcomeRaw(callID, raw string, execErr error) botToolOutcome {
	if execErr != nil {
		errPayload, _ := json.Marshal(map[string]interface{}{
			"call_id": callID,
			"error":   botErrMessage(execErr),
		})
		raw = string(errPayload)
	}
	chars := int64(utf8.RuneCountInString(raw))
	out := botToolOutcome{ResultJSON: raw, ResultChars: chars}
	if chars <= botResultBudgetChars {
		out.PreviewJSON = raw
		return out
	}
	// 截断预览：模型可按 next_offset 用 fs_result.read 续读。
	runes := []rune(raw)
	preview := string(runes[:botResultBudgetChars])
	previewPayload := map[string]interface{}{
		"call_id":     callID,
		"truncated":   true,
		"preview":     preview,
		"total_chars": chars,
		"next_offset": botResultBudgetChars,
	}
	previewRaw, _ := json.Marshal(previewPayload)
	out.PreviewJSON = string(previewRaw)
	out.Truncated = true
	return out
}

func botErrMessage(err error) string {
	msg := err.Error()
	if len(msg) > 500 {
		msg = msg[:500]
	}
	return msg
}

// execResultRead 按 call_id + offset + limit 读取已存结果（Unicode 字符计）。
func (s *BotService) execResultRead(call *model.BotToolCall) (string, error) {
	var args botResultArgs
	if err := json.Unmarshal([]byte(call.ArgumentsJSON), &args); err != nil {
		return "", errs.ErrInvalidParams
	}
	target, err := s.calls.FindByID(strings.TrimSpace(args.CallID))
	if err != nil {
		return "", errs.ErrBotCallNotFound
	}
	if target.BotID != call.BotID {
		// 越权读取别的机器人的结果：按不存在处理，不泄露存在性。
		return "", errs.ErrBotCallNotFound
	}
	if target.ResultJSON == nil {
		return "", errs.New(errs.ErrInvalidParams.Code, "该调用没有已保存的结果")
	}

	offset := int64(args.Offset)
	if offset < 0 {
		offset = 0
	}
	limit := int64(args.Limit)
	if limit <= 0 {
		limit = botResultReadDefaultLimit
	}
	if limit > botResultReadMaxLimit {
		limit = botResultReadMaxLimit
	}

	runes := []rune(*target.ResultJSON)
	total := int64(len(runes))
	if offset > total {
		offset = total
	}
	end := offset + limit
	if end > total {
		end = total
	}
	payload := map[string]interface{}{
		"call_id":     target.ID,
		"total_chars": total,
		"offset":      offset,
		"content":     string(runes[offset:end]),
		"has_more":    end < total,
	}
	return botMarshalResult(payload)
}

// ---------- 能力查询 / 帮助 / 停止 / 取消 ----------

// execCapabilityStatus 明确返回 available:false；不建立调度或记忆表，也不转调
// 现有 AutomationService。
func (s *BotService) execCapabilityStatus(call *model.BotToolCall) (string, error) {
	capability := "schedule"
	if call.ToolName == "fs_memory" {
		capability = "memory"
	}
	return botMarshalResult(map[string]interface{}{
		"capability":      capability,
		"available":       false,
		"toolset_version": botToolsetVersion,
	})
}

// botHelpText 是嵌入程序的工具使用说明；fs_help.read 原样返回。
const botHelpText = `Fast Ship 机器人工具说明：

- 读取类（同步）：fs_project list/get、fs_issue list/get/comments/timeline、fs_thread list/get/item/composer_options/workspace_options、fs_environment list/projects。
- 写操作（异步、需确认或已有授权）：fs_project create/update、fs_issue create/update/set_workflow/replace_checklist/comment、fs_thread launch/send/stop。
- 写操作受理后你会收到回执 status=pending_confirmation（等待用户确认）或 queued（已授权、已排队）；执行完成后平台会以「平台后台结果」消息送达终态。
- fs_issue replace_checklist 是整组替换：items 必须是完整清单，不是增量。
- fs_thread launch 受理成功只表示启动 RPC 已发出，远端 Agent 是否完成用 fs_thread get/item 查询。
- 大结果被截断时（truncated=true）用 fs_result.read 按 offset/limit 续读，单位是 Unicode 字符。
- fs_stop.stop 结束当前主线运行；fs_cancel.cancel 取消本机器人的某个后台调用。
- fs_schedule/fs_memory 首期不可用（available=false）。`

// execStopCurrentRun 请求停止当前主线 run；回复后由安全点收尾为 stopped。
func (s *BotService) execStopCurrentRun(call *model.BotToolCall) (string, error) {
	now := time.Now().UTC()
	ok, err := s.runs.UpdateColumns(s.runIDOfCall(call), map[string]interface{}{"finished_at": nil},
		map[string]interface{}{"stop_requested_at": now})
	if err != nil {
		return "", errs.ErrInternal
	}
	return botMarshalResult(map[string]interface{}{
		"stopping": ok,
		"note":     "当前 run 将在安全点结束；已受理的后台调用不受影响，可逐项取消",
	})
}

// execCancelOwnCall 取消本机器人的指定后台调用（用户界面取消等价指令，
// 不再弹二次确认）。终态由 cancelBotCall 原子落定。
func (s *BotService) execCancelOwnCall(call *model.BotToolCall) (string, error) {
	var args botCancelArgs
	if err := json.Unmarshal([]byte(call.ArgumentsJSON), &args); err != nil {
		return "", errs.ErrInvalidParams
	}
	target, err := s.calls.FindByID(strings.TrimSpace(args.CallID))
	if err != nil || target.BotID != call.BotID {
		return "", errs.ErrBotCallNotFound
	}
	final, cancelled := s.cancelBotCall(target)
	if !cancelled {
		return botMarshalResult(map[string]interface{}{
			"call_id": target.ID,
			"status":  string(final.Status),
			"note":    "调用已处于终态或无法取消",
		})
	}
	return botMarshalResult(map[string]interface{}{
		"call_id":                  target.ID,
		"status":                   string(final.Status),
		"effect_may_have_occurred": final.EffectMayHaveOccurred,
	})
}

// botUserIDOf 读取机器人归属人；机器人一定存在（调用账本随机器人级联删除）。
func (s *BotService) botUserIDOf(botID string) string {
	bot, err := s.bots.Get(botID)
	if err != nil {
		return ""
	}
	return bot.UserID
}
