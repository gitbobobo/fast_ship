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

// botResultPreviewChars 是截断预览的长度：只给开头一小段，避免模型把
// 「看到了预览」误当成「已经看到完整结果」。其余靠 fs_result.read 续读。
const botResultPreviewChars = 500

// botToolOutcome 是一次工具执行的统一产出：完整结果、给模型的预览与统计。
type botToolOutcome struct {
	ResultJSON  string // 完整精简结果（总是保存，供 fs_result.read 续读）
	PreviewJSON string // 进入模型上下文的受限结果
	ResultChars int64
	ResultItems int64
	Truncated   bool
	// WaitResult 是 fs_thread/wait 的完整结构化收尾结果（INT-160）：随「平台
	// 后台结果」消息的 wait_result 字段进入模型上下文（整条消息已被围栏，不
	// 再内层转义），与 ResultJSON 同值——结果太短不会截断，这里只是让它以
	// 结构化对象出现在唤醒消息里。非 wait 调用恒空。
	WaitResult json.RawMessage `json:"-"`
}

// botBuildOutcome 由执行结果构造产出；执行失败时给模型结构化错误。
func botBuildOutcome(callID string, result interface{}, execErr error) botToolOutcome {
	if execErr != nil {
		errPayload := map[string]interface{}{
			"call_id": callID,
			"error":   botErrMessage(execErr),
		}
		raw, _ := json.Marshal(errPayload)
		s := string(raw)
		return botToolOutcome{ResultJSON: s, PreviewJSON: s, ResultChars: int64(utf8.RuneCountInString(s)), ResultItems: 0}
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
	out := botToolOutcome{ResultJSON: raw, ResultChars: chars, ResultItems: botResultItemCount(raw)}
	if chars <= botResultBudgetChars {
		out.PreviewJSON = raw
		return out
	}
	// 截断预览：模型可按 next_offset 用 fs_result.read 续读。预览只给开头
	// 一小段，且 next_offset 指向预览之后的位置，内容不重叠也不丢失。
	runes := []rune(raw)
	previewLen := len(runes)
	if previewLen > botResultPreviewChars {
		previewLen = botResultPreviewChars
	}
	preview := string(runes[:previewLen])
	previewPayload := map[string]interface{}{
		"call_id":     callID,
		"truncated":   true,
		"preview":     preview,
		"total_chars": chars,
		"next_offset": previewLen,
		"note":        "仅预览开头，用 fs_result action=read call_id=<call_id> offset=next_offset 续读",
	}
	previewRaw, _ := json.Marshal(previewPayload)
	out.PreviewJSON = string(previewRaw)
	out.Truncated = true
	return out
}

// botResultItemCount 按工具结果形状取当前返回条数：顶层数组用长度；
// {items:[...]} 包装（botSlimPage）用 items 长度；空列表 0；普通对象 1。
func botResultItemCount(raw string) int64 {
	s := strings.TrimSpace(raw)
	if s == "" || s == "null" || s == "[]" || s == "{}" {
		return 0
	}
	if s[0] == '[' {
		var arr []json.RawMessage
		if json.Unmarshal([]byte(s), &arr) == nil {
			return int64(len(arr))
		}
		return 0
	}
	if s[0] == '{' {
		var obj map[string]json.RawMessage
		if json.Unmarshal([]byte(s), &obj) == nil {
			if items, ok := obj["items"]; ok {
				var arr []json.RawMessage
				if json.Unmarshal(items, &arr) == nil {
					return int64(len(arr))
				}
			}
		}
		return 1
	}
	return 1
}

func botErrMessage(err error) string {
	msg := err.Error()
	// 按字符截断（INT-139 R7）：字节截断可能劈在多字节字符中间，
	// 落库/外发的就是非法 UTF-8。
	if rs := []rune(msg); len(rs) > 500 {
		msg = string(rs[:500])
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
	// 读取体必须整体落在预算内：否则它自己会被通用截断逻辑再截一次，
	// 而模型按 next_offset 继续读时就会跳过一段永远没人看过的内容。
	// 逐步收缩 content 直到序列化体积不超预算（JSON 转义会膨胀，故迭代收敛）。
	content := runes[offset:end]
	var payload map[string]interface{}
	var raw string
	for attempt := 0; ; attempt++ {
		note := "这是 call_id " + target.ID + " 的结果切片；继续读用 offset=next_offset。"

		payload = map[string]interface{}{
			"call_id":     target.ID,
			"total_chars": total,
			"offset":      offset,
			"end":         offset + int64(len(content)),
			"next_offset": offset + int64(len(content)),
			"has_more":    offset+int64(len(content)) < total,
			"content":     string(content),
			"note":        note,
		}
		raw = botMustJSON(payload)
		over := int64(utf8.RuneCountInString(raw)) - botResultBudgetChars
		if over <= 0 || len(content) == 0 || attempt >= 8 {
			break
		}
		keep := int64(len(content)) - over - 16
		// 至少前进 1 个字符：转义膨胀（如大量反斜杠/引号）会让 over 不小于
		// 本次切片长度，否则会返回空 content + 同一 next_offset，续读永远
		// 卡在原地。预算足够容纳 1 个转义后的字符。
		if keep < 1 && offset+int64(len(content)) < total {
			keep = 1
		}
		if keep < 0 {
			keep = 0
		}
		content = content[:keep]
	}
	return raw, nil
}

// ---------- 能力查询 / 帮助 / 停止 / 取消 ----------

// execCapabilityStatus 返回能力状态：fs_memory 自 INT-102 起可用；fs_schedule
// 自 INT-103（v3）起可用。available 按调用方透传的本 run 冻结 toolsetVersion
// 判定（与会话工具面同一事实源，同 sessionContextOf 的约定），不读 bots 行
// 活值：本 run 入口激活失败而并发 run 激活成功时，活值已是 v3+，但本会话的
// 工具面里没有 fs_schedule 动作，答可用会诱导模型调用必然失败的动作。
func (s *BotService) execCapabilityStatus(call *model.BotToolCall, toolsetVersion int64) (string, error) {
	capability := "schedule"
	available := false
	note := ""
	if call.ToolName == "fs_memory" {
		capability = "memory"
		available = true
	} else {
		available = toolsetVersion >= 3
		if !available {
			note = "定时任务在工具集 v3 开放；当前会话冻结在旧版本，压缩或重启边界后升级"
		}
	}
	out := map[string]interface{}{
		"capability":      capability,
		"available":       available,
		"toolset_version": botToolsetVersion,
	}
	if note != "" {
		out["note"] = note
	}
	return botMarshalResult(out)
}

// botHelpText 是嵌入程序的工具使用说明；fs_help.read 原样返回。
const botHelpText = `Fast Ship 机器人工具说明：

- 读取类（同步）：fs_project list/get、fs_issue list/get/comments/timeline、fs_thread list/get/item/composer_options/workspace_options、fs_environment list/projects。
- 写操作（异步、需确认或已有授权）：fs_project create/update、fs_issue create/update/set_workflow/replace_checklist/comment、fs_thread launch/send/stop/settle/unsettle/resume_queue。
- 写操作受理后你会收到回执 status=pending_confirmation（等待用户确认）或 queued（已授权、已排队）；执行完成后平台会以「平台后台结果」消息送达终态。
- fs_issue get 返回正文、清单、标签等完整信息，无清单/标签时 labels/checklist 为空数组；列表项只保留决策所需的最小字段。update/replace_checklist 前先 get 读回 updated_at / checklist_updated_at，并原样带上 expected_updated_at / expected_checklist_updated_at；目标已被改动时返回 conflict 与最新内容，应重新读取、保留双方修改后再提交。
- fs_issue replace_checklist 是整组替换：items 必须是完整清单，不是增量。
- fs_thread list 默认只返回未归档、未结算的线程（已结算的要带 settled=true 才看得到），支持 env_id/project_id/query/settled/status/updated_after 过滤与 page/page_size 分页（query 匹配线程 id/标题/项目名/项目 id/环境名，不区分大小写包含）；返回 total_items/returned_items/offset/has_more、settled_filter（本次实际生效的结算筛选）以及各环境的读取覆盖情况（incomplete_environments）。每项带 run_status（T3 原始线程状态：status 不是 idle 时线程还没结束——后台 run/后台工作仍在，run_status 只是最近一次根 run 的状态；status=idle 时它才是最终结局，completed/failed/cancelled 靠它区分）与 pull_requests[]（repo/number/url/watching，snapshot 已同步时另有 state/merged_at/is_draft/checks_state/mergeability；watching=true 表示 T3 挂着监听，检查结束/他人评论/分支冲突会唤醒该线程）。每项另带 activity（运行活动摘要，只含非零信号：活动 run、待应答请求、后台工作数、停止中；空对象即平静线程）与 last_reply（末条助手回复摘要，300 字符截断；无助手回复则省略；单条详情读不到时 detail_unavailable=true，读超时/预算耗尽被中止的标 detail_skipped=true）——看线程有没有回话、还在不在跑，通常不必再逐线程 get。
- fs_thread get 返回线程状态、最近条目（含条目 id 与短摘要；subagent 条目带 child_thread_id，用它进子线程 get）与完整历史翻页入口（item_offset/next_item_offset）；条目正文用 fs_thread action=item item_id=<id> 补读。线程摘要（list/get）带 provider/model/branch，是线程实际生效的执行配置。
- fs_thread launch 受理成功只表示启动 RPC 已发出，远端 Agent 是否完成用 fs_thread get/item 查询。可选执行配置：model_selection（instance_id/model/options 取值用 composer_options 返回的 providers 清单，options 是 [{id,value}] 键值对）、runtime_mode（approval-required/auto-accept-edits/auto/full-access）、workspace（type=root/worktree/existing_worktree：root 用项目主检出，worktree 新建工作树可带 base_ref/branch/start_from_origin，existing_worktree 复用已有工作树需 worktree_path；字段与 type 不匹配会被拒绝：root 不接受策略字段、worktree 不接受 worktree_path、existing_worktree 不接受 base_ref/start_from_origin）；省略时：model_selection 沿用项目默认模型（见 composer_options projects[].default_model_selection）、runtime_mode 为 full-access、workspace 用项目主检出。composer_options 返回 projects/providers/not_settable：providers 是按实例归一化的可用模型目录（已剔除停用/未安装/不可用项），not_settable 列出机器人不能指定的配置项。
- fs_thread stop 是 run.interrupt + holdQueue：仅当线程存在可中断 run/后台工作才派发停止命令，并挂起该线程的排队消息（队列 held，此后新消息也进队列但不执行）、结束该线程的 PR 监听、停掉它委派的子任务；线程只剩 PR 监听这类不可中断占位时 stop 是空操作（stopped=false），PR 监听继续；回执只表示受理，是否真的停了用 fs_thread get 的 activity.stopping 判断。
- fs_thread send 在线程运行中会把消息排队；队列里已有被挂起的排队消息（stop 或 T3 重启恢复所致）时，新消息也会继承挂起，要先 resume_queue。
- fs_thread settle 把线程收进 T3 历史区：list 默认不含已结算线程，要带 settled=true 才看得到；结算会清掉置顶与该线程所有 PR 监听；线程还在跑、有排队消息或有待应答的审批/提问时会被拒绝（先停止或先应答再结算）。unsettle 撤销、放回默认列表。resume_queue 恢复该线程被挂起的队列，排队消息继续执行。
- fs_thread wait 挂起等待线程变化（异步动作，受理即 running，不进执行队列，无写副作用；本动作自工具集 v13 起提供，更早版本的会话不可调用）：on 选触发条件（idle/needs_reply/error，逗号分隔子集，缺省全部），timeout_minutes 是等待上限（1-30，缺省 10；定时任务会话内不得超过 15）。线程开始运行并命中条件（idle 需稳定约 20 秒）即唤醒；约 60 秒内线程始终没开始运行会以 not_started 唤醒；到超时按 outcome 区分 timeout_unchanged（监听可信、确实没动）/timeout_progressing（在动但没到终态）/watch_degraded（连接不可信，结论仅供参考）/thread_gone。唤醒的「平台后台结果」带 wait_result：变化字段前后对比（status/run_status/activity/queue.held/PR 状态）、条目绝对数、等待期间更新过的条目数 new_items（updated_at 晚于挂上时刻的条目数）与最近 3 条条目摘要，醒来通常不必再 get。取消用 fs_cancel；每机器人同时最多 3 个、同一线程 1 个（重复挂立即返回 already_waiting 与已有 call_id）、同一线程 1 小时内最多挂 6 次。
- 大结果被截断时（truncated=true）结果体只给开头预览（preview），用 fs_result action=read call_id=<call_id> offset=next_offset 续读，单位是 Unicode 字符；续读返回体标明切片范围与 next_offset，按它继续不会漏内容。
- fs_stop.stop 结束当前主线运行；用户点「停止」只是发来一条「停止」消息，是否停止由你决定，可用 fs_stop 执行。fs_cancel.cancel 取消本机器人的某个后台调用。
- 中断（如服务重启、调用被取消）后结果可能无法核实（effect_may_have_occurred=true）：不要断言失败、不要自动重放副作用，先用读取类工具核实现状。
- 记忆：fs_memory list/read/create/replace/append/delete/history/restore（写操作需 expected_version 乐观锁）；fs_memory_search search 关键词检索（≥3 字符全文匹配）。USER.md 记用户画像，MEMORY.md 记长期事实，daily/YYYY-MM-DD.md 是每日笔记。
- 定时任务：fs_schedule list/create/update/delete/runs/run/enable/disable。cron 是标准五字段（分 时 日 月 周），时区用独立 IANA 字段（可选，省略即用户时区，「每天 9 点」按用户所在地执行；update 不传时区=保持原值）。list/runs/disable 与只改 name/cron/timezone 的 update、dry_run=true 的 run 同步立即生效；create、改动 instruction/needs_report/model 的 update、run（dry_run=true 预览除外）、enable、delete 需要用户确认：先受理（回执 status=pending_confirmation），用户批准后在后台执行，结果以「平台后台结果」消息送达，不要把受理当成已执行；批准创建或修改任务等于授权该任务以后每次运行默认放行写操作，enable 等于重新启用一个以后每次运行默认放行写操作的任务，delete 不可逆。任务由你创建/修改/删除；用户只能查看与开关。run 立即手动执行一次：显式触发就是一次执行、不要求任务启用，回执只表示已受理（终态用 runs 查询，trigger_source 区分 cron/manual）；同一任务已有未结束运行时被拒绝；dry_run=true 只预览、不建运行记录。定时任务运行中不能管理任务（防止绕过写操作确认），请在主线对话里操作。每次运行是独立会话，只带你的记忆与任务指令，写操作默认放行；无话可说时只回复一行 [[SILENT]]。任务各有一块跨运行保留的小状态：state_get（无参数）读取、state_set 写入（state 为任意 JSON 值，≤8192 UTF-8 字节；省略或 null 即清空）；这两个动作只能在定时任务运行（schedule run）内使用，主线会话调用会被拒绝。当前状态已随运行时上下文的 task_state 字段给出（带 task_state_updated_at；是本次运行开始时的快照，省略表示当时为空，运行中变化后要最新值才调 state_get），开局不必调 state_get；写入与当前完全相同的内容会直接回 unchanged、不产生变更——没有变化就不要重复写。任务指令写「意图」而不是固定操作步骤：说清目标、什么情况才需要汇报、去重方式（用 state_get/state_set 记「上次已报的 ID」，只报新增），例如「每天 9 点检查新 issue，有新的才告诉我」；只有新内容确实值得打断用户的周期检查才开 needs_report=true。`

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
