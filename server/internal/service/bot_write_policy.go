package service

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"
	"unicode"

	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/godbobo/fast_ship/server/internal/pkg/errs"
	"github.com/godbobo/fast_ship/server/internal/repository"
	"github.com/google/uuid"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

// 写操作分级放行（INT-160）：分档由服务端按「动作 + 归一化参数 + 目标线程
// 内存状态」计算，模型不能自证安全。三档口径：
//
//   - tier1 自动执行（不建确认卡、不进 always-allow 授权）：send「继续」模板
//     （带前置条件与配额）、settle（线程平静）、unsettle（线程已知）。
//   - tier2 维持确认卡（本版不做限额）：其余需确认动作。
//   - tier3 主线照旧确认卡；定时任务会话同步拒绝（强制主线重发并汇报）。
//
// 策略不写进工具定义（避免受工具集冻结影响），每条 tier1 规则带独立开关与
// 误判自动降级；降级出的确认卡带过期时间，worker 执行前复检前置条件。

const (
	// botPolicySendQuotaPerThreadHour 同一 bot 对同一线程 1 小时内自动 send
	// 「继续」的上限；连发两次无效，第三次大概率也无效，应改为报告用户。
	botPolicySendQuotaPerThreadHour = 2
	botPolicySendQuotaWindow        = time.Hour
	// botPolicyCardExpiry 是降级确认卡的过期时间：过期后批准入队的调用在
	// 执行前复检即失败（判断时满足、执行时已变的窗口不能无限大）。
	botPolicyCardExpiry = 30 * time.Minute
	// botPolicyAlwaysAllowTTL 是新建「始终允许」的默认有效期。
	botPolicyAlwaysAllowTTL = 30 * 24 * time.Hour
	// botPolicyMisjudgeWindow 是误判检测的反向操作窗口；窗口内累计
	// botPolicyFalsePositivesToDisable 次即自动关闭规则。
	botPolicyMisjudgeWindow          = 30 * time.Minute
	botPolicyFalsePositivesToDisable = 2
	// botPolicyFalsePositiveReset 是误判计数窗口：距上次误判超过 24 小时的
	// 下一次误判把计数重置为 1。
	botPolicyFalsePositiveReset = 24 * time.Hour
	// botPolicySettleDetailBudget 是 settle 前置条件读取 queue.held 的预算
	//（queue.held 不在 hub 内存里，只能在评估那一刻读一次详情）。
	botPolicySettleDetailBudget = 5 * time.Second
)

// tier1 规则 ID（进 policy_json 与 bot_write_policy_rules 主键，发布后不可改）。
const (
	botPolicyRuleSendContinue = "thread_send_continue"
	botPolicyRuleSettleIdle   = "thread_settle_idle"
	botPolicyRuleUnsettle     = "thread_unsettle"
)

// botWritePolicyRuleInfo 是规则目录的静态条目：规则清单 API 与自动降级通知
// 共用同一份名称与描述。
type botWritePolicyRuleInfo struct {
	ID          string `json:"rule_id"`
	Name        string `json:"name"`
	Description string `json:"description"`
}

var botWritePolicyRuleCatalog = []botWritePolicyRuleInfo{
	{
		ID:          botPolicyRuleSendContinue,
		Name:        "自动发送「继续」",
		Description: "对空闲、有运行痕迹且 1 小时内自动发送未超 2 次的线程发送精确模板（继续/继续执行/请继续/继续推进），不引入新意图",
	},
	{
		ID:          botPolicyRuleSettleIdle,
		Name:        "自动结算平静线程",
		Description: "线程已空闲、无待应答请求、无活动运行/后台任务且队列未挂起时执行 settle（settle 与 unsettle 可互逆）",
	},
	{
		ID:          botPolicyRuleUnsettle,
		Name:        "自动撤销结算",
		Description: "线程在可信视图里已知时执行 unsettle，把线程放回默认列表",
	},
}

// botPolicyContinueTemplates 是「继续」模板词表（写死服务端）：send 正文归一化
// （去空白与标点、转小写）后必须精确命中其一。不靠长度、不靠模型自述。
var botPolicyContinueTemplates = buildBotPolicyContinueTemplates()

func buildBotPolicyContinueTemplates() map[string]bool {
	out := map[string]bool{}
	for _, t := range []string{"继续", "继续执行", "请继续", "继续推进"} {
		out[botPolicyNormalizeText(t)] = true
	}
	return out
}

// botPolicyNormalizeText 归一化 send 正文：去全部空白与标点/符号、转小写，
// 只留字母数字与 CJK。
func botPolicyNormalizeText(s string) string {
	var b strings.Builder
	for _, r := range s {
		if unicode.IsSpace(r) || unicode.IsPunct(r) || unicode.IsSymbol(r) {
			continue
		}
		b.WriteRune(unicode.ToLower(r))
	}
	return b.String()
}

func botPolicyIsContinueTemplate(text string) bool {
	return botPolicyContinueTemplates[botPolicyNormalizeText(text)]
}

// botWritePolicyRecord 是 policy_json 的落库形态：三条路径（自动执行 / 确认卡 /
// 定时任务同步拒绝）都写。Expiry 只出现在降级确认卡上；ApprovedCallID 是降级
// （配额用尽）时最近一次消耗额度的自动调用 ID，供审计追溯「这次为什么没放行」。
type botWritePolicyRecord struct {
	Tier            string                 `json:"tier"` // tier1 / tier2 / tier3
	RuleID          string                 `json:"rule_id,omitempty"`
	Auto            bool                   `json:"auto"`
	Source          string                 `json:"source,omitempty"` // tier1 / always_allow / schedule_default / confirmation / schedule_denied
	Facts           map[string]interface{} `json:"facts,omitempty"`
	DowngradeReason string                 `json:"downgrade_reason,omitempty"`
	Expiry          *time.Time             `json:"expiry,omitempty"`
	ApprovedCallID  string                 `json:"approved_call_id,omitempty"`
	ScheduleDenied  bool                   `json:"schedule_denied,omitempty"`
}

func botWritePolicyRecordOf(call *model.BotToolCall) *botWritePolicyRecord {
	if call == nil || call.PolicyJSON == "" {
		return nil
	}
	rec := &botWritePolicyRecord{}
	if err := json.Unmarshal([]byte(call.PolicyJSON), rec); err != nil {
		return nil
	}
	return rec
}

func botPolicyRecordJSON(rec *botWritePolicyRecord) string {
	if rec == nil {
		return ""
	}
	raw, err := json.Marshal(rec)
	if err != nil {
		return ""
	}
	return string(raw)
}

// botWritePolicyEval 是一次策略评估的完整结论。
type botWritePolicyEval struct {
	record botWritePolicyRecord
	// authz 非 nil 表示主线命中的既有 always-allow（自动排队并回填
	// authorization_id，行为与 INT-160 之前一致）。
	authz *model.BotAuthorization
	// denied 表示定时任务会话 tier3 同步拒绝：调用按受理端同步失败处理。
	denied bool
}

// ---------- 分档 ----------

// botPolicyCandidateRule 返回 tier1 候选规则 ID；非候选（返回空）按基础档处理。
// send 只有精确命中「继续」模板才是候选：非模板的 send 直接落 tier3。
func botPolicyCandidateRule(prepared *botPreparedCall) string {
	switch prepared.ToolName + "/" + prepared.Action {
	case "fs_thread/settle":
		return botPolicyRuleSettleIdle
	case "fs_thread/unsettle":
		return botPolicyRuleUnsettle
	case "fs_thread/send":
		var args botThreadArgs
		_ = json.Unmarshal([]byte(prepared.ArgumentsJSON), &args)
		if botPolicyIsContinueTemplate(args.Text) {
			return botPolicyRuleSendContinue
		}
	}
	return ""
}

// botPolicyBaseTier 是非 tier1 候选写动作的基础档：launch、fs_schedule
// create/update/delete、fs_issue 全部写动作与不满足 tier1 条件的
// send/stop/settle/unsettle/resume_queue 是 tier3；其余（fs_project 写、
// fs_schedule run/enable）是 tier2。
func botPolicyBaseTier(prepared *botPreparedCall) string {
	switch prepared.ToolName {
	case "fs_issue":
		return "tier3"
	case "fs_schedule":
		switch prepared.Action {
		case "create", "update", "delete":
			return "tier3"
		}
	case "fs_thread":
		switch prepared.Action {
		case "launch", "stop", "resume_queue", "send":
			return "tier3"
		}
	}
	return "tier2"
}

// botPolicyRuleForAction 把一个动作映射回覆盖它的 tier1 规则（误判检测用）；
// 不被任何规则覆盖的动作返回空。
func botPolicyRuleForAction(toolName, action string) string {
	switch toolName + "/" + action {
	case "fs_thread/send":
		return botPolicyRuleSendContinue
	case "fs_thread/settle":
		return botPolicyRuleSettleIdle
	case "fs_thread/unsettle":
		return botPolicyRuleUnsettle
	}
	return ""
}

func botPolicyRuleName(ruleID string) string {
	for _, info := range botWritePolicyRuleCatalog {
		if info.ID == ruleID {
			return info.Name
		}
	}
	return ruleID
}

// ---------- 线程状态事实（全部服务端查证） ----------

// botPolicyThreadState 是 hub 内存里目标线程的可信投影：known=false 表示视图
// 不可信或线程不在可信面上，所有前置条件按不满足处理（fail-safe 交回确认）。
type botPolicyThreadState struct {
	known             bool
	status            string
	runStatus         string
	hasPendingRequest bool
	hasActiveRun      bool
	backgroundTasks   int
	hasTraces         bool
	latestRunID       string
}

func (st botPolicyThreadState) facts() map[string]interface{} {
	out := map[string]interface{}{
		"known": st.known,
	}
	if !st.known {
		return out
	}
	out["status"] = st.status
	out["run_status"] = st.runStatus
	if st.hasPendingRequest {
		out["has_pending_requests"] = true
	}
	if st.hasActiveRun {
		out["active_run"] = true
	}
	if st.backgroundTasks > 0 {
		out["background_tasks"] = st.backgroundTasks
	}
	if st.latestRunID != "" {
		out["latest_run_id"] = st.latestRunID
	}
	out["has_run_traces"] = st.hasTraces
	return out
}

// policyThreadState 读取 hub 内存快照（PeekShell，无网络 I/O）：与 wait 评估器
// 同一可信口径（State==t3ShellReady 才算数）。
func (s *BotService) policyThreadState(userID, envID, threadID string) botPolicyThreadState {
	st := botPolicyThreadState{}
	if s.threads == nil {
		return st
	}
	view, err := s.threads.PeekShell(userID, envID)
	if err != nil || view == nil || view.Snaps == nil || view.State != t3ShellReady {
		return st
	}
	shell, _, ok := view.Snaps.findThread(threadID)
	if !ok {
		return st
	}
	st.known = true
	st.status = string(t3ThreadStatus(shell))
	st.runStatus = shell.Status
	st.hasPendingRequest = shell.PendingRuntimeRequest != nil
	st.hasActiveRun = shell.ActiveRunID != nil && *shell.ActiveRunID != ""
	for _, task := range shell.PendingBackgroundTasks {
		if task.Kind != "command" {
			st.backgroundTasks++
		}
	}
	if shell.LatestRunID != nil {
		st.latestRunID = *shell.LatestRunID
	}
	// 「继续」不引入新意图的判据：线程有既有运行痕迹（latest_run_id 或活动
	// 运行或后台任务）。全新空线程上的「继续」是无的放矢，不放行。
	st.hasTraces = st.latestRunID != "" || st.hasActiveRun || st.backgroundTasks > 0
	return st
}

// policyQueueHeld 读线程队列挂起状态（不在 hub 内存里，读一次详情）；读取
// 失败返回 ok=false，由调用方保守降级。
func (s *BotService) policyQueueHeld(userID, envID, threadID string) (held, ok bool) {
	if s.threads == nil {
		return false, false
	}
	ctx, cancel := context.WithTimeout(context.Background(), botPolicySettleDetailBudget)
	defer cancel()
	detail, err := s.threads.Detail(ctx, userID, envID, threadID)
	if err != nil {
		return false, false
	}
	return detail.Queue.Held, true
}

// ---------- 前置条件（评估与执行前复检共用） ----------

// policyCheckSendPreconditions 校验 send「继续」的线程侧前置条件；checkQuota
// 为 false 时跳过配额（执行前复检场景：用户已明确批准，额度不再是前置）。
// batchUsed 是同一回复里先于本调用受理的自动「继续」数——限额评估只查已
// 提交行会漏掉同批成员，由调用方把它们计入。返回 (通过, 降级原因)。配额
// 用尽时把最近一次消耗额度的自动调用 ID 记进 rec.ApprovedCallID，供审计
// 追溯「这次为什么没放行」。
func (s *BotService) policyCheckSendPreconditions(bot *model.Bot, args botThreadArgs, rec *botWritePolicyRecord, now time.Time, checkQuota bool, batchUsed int) (bool, string) {
	facts := rec.Facts
	st := s.policyThreadState(bot.UserID, args.EnvID, args.ThreadID)
	facts["thread"] = st.facts()
	if !st.known {
		return false, "目标线程不在可信视图里（可能已结算、归档或环境不可读），无法确认「继续」不引入新意图"
	}
	if !st.hasTraces {
		return false, "目标线程没有既有运行痕迹，无法确认「继续」只是延续原有计划"
	}
	if st.status != "idle" {
		return false, "目标线程不在空闲状态（当前 " + st.status + "），运行中的线程不宜再插话"
	}
	if !checkQuota {
		return true, ""
	}
	used, lastAutoID, ok := s.policySendQuotaUsed(bot.ID, args.EnvID, args.ThreadID, now)
	used += batchUsed
	quota := map[string]interface{}{"used": used, "limit": botPolicySendQuotaPerThreadHour}
	if !ok {
		quota["unknown"] = true
	}
	if lastAutoID != "" {
		quota["recent_auto_call_id"] = lastAutoID
	}
	facts["quota"] = quota
	if !ok {
		return false, "自动「继续」的额度查询失败，保守交回确认"
	}
	if used >= botPolicySendQuotaPerThreadHour {
		rec.ApprovedCallID = lastAutoID
		return false, "自动「继续」的每小时额度已用尽（1 小时内已自动发送 " +
			itoa64(int64(used)) + " 次），继续连发大概率无效，请向用户汇报"
	}
	return true, ""
}

// policyCheckSettlePreconditions 校验 settle 的线程侧前置条件：已知、空闲、
// 无待应答请求、无活动运行/后台任务、队列未挂起。任一不满足即降级。
func (s *BotService) policyCheckSettlePreconditions(bot *model.Bot, args botThreadArgs, facts map[string]interface{}, now time.Time) (bool, string) {
	st := s.policyThreadState(bot.UserID, args.EnvID, args.ThreadID)
	facts["thread"] = st.facts()
	if !st.known {
		return false, "线程不在可信视图里（可能已结算、归档或环境不可读），结算会让它从巡检视野消失"
	}
	if st.status != "idle" {
		return false, "线程不在空闲状态（当前 " + st.status + "），结算运行中的线程会丢掉正在进行的观察"
	}
	if st.hasPendingRequest {
		return false, "线程有待应答的审批/提问，先应答再结算"
	}
	if st.hasActiveRun || st.backgroundTasks > 0 {
		return false, "线程还有活动运行或后台任务未结束"
	}
	held, ok := s.policyQueueHeld(bot.UserID, args.EnvID, args.ThreadID)
	facts["queue_held"] = held
	if !ok {
		return false, "无法读取线程队列状态，保守交回确认"
	}
	if held {
		return false, "线程有被挂起的排队消息，先恢复或清理队列再结算"
	}
	return true, ""
}

// policyCheckUnsettlePreconditions 校验 unsettle 的线程侧前置条件：线程已知。
func (s *BotService) policyCheckUnsettlePreconditions(bot *model.Bot, args botThreadArgs, facts map[string]interface{}) (bool, string) {
	st := s.policyThreadState(bot.UserID, args.EnvID, args.ThreadID)
	facts["thread"] = st.facts()
	if !st.known {
		return false, "线程不在可信视图里，无法确认它的结算状态"
	}
	return true, ""
}

// policySendQuotaUsed 统计窗口内本 bot 对该线程已自动执行的「继续」次数
// （从调用账本的 policy_json 计数，含后续失败/取消的行——失败的发送同样
// 消耗了一次尝试），并带回最近一次自动调用的 ID。计数在 SQL 侧按 bot、
// 规则与目标线程过滤（LIMIT 截断的全局扫描会被其他机器人的行挤占，不能
// 当完整计数用）。查询失败返回 ok=false，方向安全。
func (s *BotService) policySendQuotaUsed(botID, envID, threadID string, now time.Time) (int, string, bool) {
	return s.policySendQuotaUsedTx(s.db, botID, envID, threadID, now)
}

// policySendQuotaUsedTx 是 policySendQuotaUsed 的任意句柄版本：受理事务内
// 的二次复核用事务句柄再数一遍已提交行（含同事务早前插入的同批成员）。
func (s *BotService) policySendQuotaUsedTx(db *gorm.DB, botID, envID, threadID string, now time.Time) (int, string, bool) {
	n, lastAutoID, err := repository.NewBotToolCallRepository(db).CountPolicyAutoCallsByThreadSinceTx(
		db, botID, botPolicyRuleSendContinue, envID, threadID, now.Add(-botPolicySendQuotaWindow))
	if err != nil {
		return 0, "", false
	}
	return int(n), lastAutoID, true
}

// policyRuleEnabled 读规则的独立开关：行不存在即启用。查询失败按禁用处理
// （fail-safe：交回确认，不无声放行）。
func (s *BotService) policyRuleEnabled(botID, ruleID string) bool {
	row, err := repository.NewBotWritePolicyRuleRepository(s.db).GetTx(s.db, botID, ruleID)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return true
	}
	if err != nil {
		s.logger.Warn("分级放行规则开关查询失败，按禁用处理",
			zap.String("bot_id", botID), zap.String("rule_id", ruleID), zap.Error(err))
		return false
	}
	return row.Enabled
}

// ---------- 评估入口 ----------

// evaluateWritePolicy 对一个已通过校验与范围解析的需确认写调用算出分档与
// 执行路径。batchAutoSendUsed 是同一回复里先于本调用受理的自动「继续」数
// （同批成员共享每小时额度，见 policyCheckSendPreconditions）。主线：tier1
// 自动排队（不建授权）、tier1 候选不成立降级为带过期的确认卡、tier2/tier3
// 先查 always-allow 再落确认卡。定时任务会话：tier1 自动执行、tier2 维持
// INT-103 默认放行、tier3 同步拒绝。
func (s *BotService) evaluateWritePolicy(bot *model.Bot, run *model.BotRun, prepared *botPreparedCall, batchAutoSendUsed int) *botWritePolicyEval {
	now := time.Now().UTC()
	schedule := run.SessionKind == model.BotSessionKindSchedule
	eval := &botWritePolicyEval{}
	rec := botWritePolicyRecord{
		Tier:   "tier2",
		Auto:   false,
		Source: "confirmation",
		Facts:  map[string]interface{}{},
	}
	eval.record = rec

	candidate := botPolicyCandidateRule(prepared)
	var downgrade string
	tier1OK := false
	if candidate != "" {
		eval.record.Tier = "tier1"
		eval.record.RuleID = candidate
		if !s.policyRuleEnabled(bot.ID, candidate) {
			downgrade = "该规则已关闭（误判自动降级或手动关闭），同类动作回到确认"
		} else {
			var args botThreadArgs
			_ = json.Unmarshal([]byte(prepared.ArgumentsJSON), &args)
			switch candidate {
			case botPolicyRuleSendContinue:
				tier1OK, downgrade = s.policyCheckSendPreconditions(bot, args, &eval.record, now, true, batchAutoSendUsed)
			case botPolicyRuleSettleIdle:
				tier1OK, downgrade = s.policyCheckSettlePreconditions(bot, args, eval.record.Facts, now)
			case botPolicyRuleUnsettle:
				tier1OK, downgrade = s.policyCheckUnsettlePreconditions(bot, args, eval.record.Facts)
			}
		}
	} else {
		eval.record.Tier = botPolicyBaseTier(prepared)
	}

	switch {
	case candidate != "" && tier1OK:
		// tier1 自动执行：不建 always-allow 授权（策略放行是每次现算的，
		// 授权是持久的——两者的审计语义必须分开）。
		eval.record.Auto = true
		eval.record.Source = "tier1"
	case schedule && (candidate != "" || eval.record.Tier == "tier3"):
		// 定时任务会话 tier3 同步拒绝（不建卡、强制汇报）：主线重新发起同一
		// 动作才会生成正常确认卡。
		eval.record.Tier = "tier3"
		eval.record.RuleID = ""
		eval.record.Facts["candidate_rule"] = candidate
		eval.record.Source = "schedule_denied"
		eval.record.ScheduleDenied = true
		if candidate != "" {
			eval.record.DowngradeReason = downgrade
		}
		eval.denied = true
	case schedule:
		// tier2 维持 INT-103 默认放行。
		eval.record.Auto = true
		eval.record.Source = "schedule_default"
	default:
		// 主线：既有 always-allow 优先于降级（用户明示的持久授权不因策略
		// 前置失效而悄悄降格）；其次 tier1 候选降级为带过期的确认卡；
		// tier2/tier3 落普通确认卡。
		if authz := s.matchAuthorization(bot.ID, prepared); authz != nil {
			eval.record.Auto = true
			eval.record.Source = "always_allow"
			eval.authz = authz
		} else if downgrade != "" {
			eval.record.DowngradeReason = downgrade
			expiry := now.Add(botPolicyCardExpiry)
			eval.record.Expiry = &expiry
		}
	}
	return eval
}

// ---------- 执行前复检 ----------

// botPolicyRecheckError 是执行前复检失败的专用错误：错误码固定
// policy_recheck_failed（finalizeCall 识别后覆盖默认的 e40001）。
type botPolicyRecheckError struct{ reason string }

func (e *botPolicyRecheckError) Error() string {
	return "执行前复检未通过：" + e.reason
}

// isBotPolicyRecheckError 报告错误是否是执行前复检失败。
func isBotPolicyRecheckError(err error) bool {
	var recheckErr *botPolicyRecheckError
	return errors.As(err, &recheckErr)
}

// botCallErrorCodeOf 是调用账本与后台结果消息共用的错误码口径：复检失败
// 用专用码。自定义 error 类型不经 AppError 折算的话会被翻成 invalid_arguments，
// 账本检索与前端提示就对不上了。
func botCallErrorCodeOf(err error) string {
	if isBotPolicyRecheckError(err) {
		return "policy_recheck_failed"
	}
	return botErrCodeOf(err)
}

// policyRecheckCall 在 worker 认领后、真正执行前重评 tier1 前置条件：覆盖
// source=tier1 的自动放行与带有效期的降级批准卡（「判断时满足、执行时已变」
// 的窗口不因无人确认而消失）。source=always_allow 不复检——受理端已裁决
// 「持久授权优先于降级」，用户明示的授权不能在执行时被同一组前置条件静默
// 推翻。普通确认卡（tier2/tier3）与无策略记录的调用同样不复检。复检一律
// 跳过配额：额度只约束受理时刻的自动放行，已入队的调用不因额度用尽而翻脸。
// 过期（降级卡）或前置不满足都返回复检错误，调用落 failed +
// policy_recheck_failed + effect=false。
func (s *BotService) policyRecheckCall(bot *model.Bot, call *model.BotToolCall) error {
	rec := botWritePolicyRecordOf(call)
	if rec == nil || rec.Tier != "tier1" || rec.RuleID == "" || rec.Source == "always_allow" {
		return nil
	}
	now := time.Now().UTC()
	if rec.Expiry != nil && now.After(*rec.Expiry) {
		return &botPolicyRecheckError{
			"确认卡已过期（降级确认卡 30 分钟内有效）；请重新评估线程现状后再次发起",
		}
	}
	var args botThreadArgs
	_ = json.Unmarshal([]byte(call.ArgumentsJSON), &args)
	facts := map[string]interface{}{}
	rec.Facts = facts
	var reason string
	switch rec.RuleID {
	case botPolicyRuleSendContinue:
		// 复检不查配额：用户已明确批准/受理时额度已核过这一次，额度只约束
		// 无人在场的自动放行。
		_, reason = s.policyCheckSendPreconditions(bot, args, rec, now, false, 0)
	case botPolicyRuleSettleIdle:
		_, reason = s.policyCheckSettlePreconditions(bot, args, facts, now)
	case botPolicyRuleUnsettle:
		_, reason = s.policyCheckUnsettlePreconditions(bot, args, facts)
	default:
		return nil
	}
	if reason != "" {
		return &botPolicyRecheckError{reason + "。请向用户说明并重新评估"}
	}
	return nil
}

// ---------- 误判检测与自动降级 ----------

// botPolicyThreadTargetOf 从执行上下文取线程目标：ThreadID 对 send/stop 等动作
// 缺省（幂等身份只给 command/message），此时回退 scope_id——受理时两者来自
// 同一个 thread_id 参数。
func botPolicyThreadTargetOf(execCtx botCallExecContext) (envID, threadID string) {
	threadID = execCtx.ThreadID
	if threadID == "" {
		threadID = execCtx.ScopeID
	}
	return execCtx.EnvID, threadID
}

// policyRecentAutoOnThread 报告窗口内同规则、同目标线程是否有过 tier1 自动
// 放行。判定在 SQL 侧按 bot/规则/目标线程过滤（LIMIT 截断的全局扫描不能当
// 完整窗口用）；查询失败按「没有」处理——误判检测偏保守，多记的代价只是
// 规则早一点回到确认档。
func (s *BotService) policyRecentAutoOnThread(botID, ruleID, envID, threadID string) bool {
	exists, err := repository.NewBotToolCallRepository(s.db).PolicyAutoCallExistsSinceTx(
		s.db, botID, ruleID, envID, threadID, time.Now().UTC().Add(-botPolicyMisjudgeWindow))
	if err != nil {
		s.logger.Warn("误判窗口查询失败，按无自动放行处理",
			zap.String("bot_id", botID), zap.String("rule_id", ruleID), zap.Error(err))
		return false
	}
	return exists
}

// recordPolicyMisjudgment 给 (bot, rule) 记一次误判：24 小时窗口内累计达到
// 阈值即自动关闭（enabled=false + auto_disabled_at），并给主线追加一条
// 生命周期消息说明「规则已自动降级回确认」。计数推进、关闭落库与通知落库
// 在同一事务里：读-改-写不进事务会并发丢更新；关闭与通知分两次提交的话，
// 中间崩溃后已关规则直接 return，通知就永远丢了。规则已关闭时不再累计。
func (s *BotService) recordPolicyMisjudgment(botID, ruleID, detail string) {
	now := time.Now().UTC()
	disabled := false
	txErr := s.db.Transaction(func(tx *gorm.DB) error {
		disabled = false
		repo := repository.NewBotWritePolicyRuleRepository(tx)
		row, err := repo.GetTx(tx, botID, ruleID)
		switch {
		case errors.Is(err, gorm.ErrRecordNotFound):
			row = &model.BotWritePolicyRule{BotID: botID, RuleID: ruleID, Enabled: true}
		case err != nil:
			return err
		}
		if !row.Enabled {
			return nil
		}
		if row.LastFalsePositiveAt == nil || now.Sub(*row.LastFalsePositiveAt) > botPolicyFalsePositiveReset {
			row.FalsePositiveCount24h = 1
		} else {
			row.FalsePositiveCount24h++
		}
		row.LastFalsePositiveAt = &now
		row.UpdatedAt = now
		disabled = row.FalsePositiveCount24h >= botPolicyFalsePositivesToDisable
		if disabled {
			row.Enabled = false
			row.AutoDisabledAt = &now
		}
		if err := repo.UpsertEnabledTx(tx, row); err != nil {
			return err
		}
		if disabled {
			// 通知与关闭同事务：要么都落库、要么都不落。dedup 键绑本次关闭
			// 事件（auto_disabled_at），重放不会重复追加。
			return s.appendPolicyRuleDisabledNoticeTx(tx, botID, ruleID, now)
		}
		return nil
	})
	if txErr != nil {
		s.logger.Warn("误判计数落库失败",
			zap.String("bot_id", botID), zap.String("rule_id", ruleID), zap.Error(txErr))
		return
	}
	if disabled {
		s.publishEvent(botID)
	}
}

// appendPolicyRuleDisabledNoticeTx 在调用方事务内落规则自动降级的主线通知
// （走既有生命周期消息与事件机制，幂等键绑本次关闭事件的时间戳）。
func (s *BotService) appendPolicyRuleDisabledNoticeTx(tx *gorm.DB, botID, ruleID string, now time.Time) error {
	dedup := "policy_disable:" + ruleID + ":" + now.UTC().Format(time.RFC3339Nano)
	txMsgs := repository.NewBotMessageRepository(tx)
	if existing, err := txMsgs.FindByDedupKey(botID, dedup); err == nil {
		_ = existing
		return nil
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}
	seq, err := s.bots.NextMessageSeqTx(tx, botID)
	if err != nil {
		return err
	}
	detailRaw, _ := json.Marshal(map[string]interface{}{
		"rule_id":     ruleID,
		"rule_name":   botPolicyRuleName(ruleID),
		"user_note":   "自动放行规则「" + botPolicyRuleName(ruleID) + "」在 24 小时内累计 2 次误判，已自动降级回确认：此后同类动作每次都会先向你确认，可在授权管理中重新开启。",
		"disabled_at": now.UTC().Format(time.RFC3339Nano),
	})
	content := botLifecycleContent{
		Event:  "policy_rule_autodisabled",
		Detail: detailRaw,
	}
	raw, err := json.Marshal(content)
	if err != nil {
		return err
	}
	msg := &model.BotMessage{
		ID: uuid.NewString(), BotID: botID, Seq: seq,
		Kind: model.BotMessageKindLifecycle, ContentJSON: string(raw),
		DedupKey: &dedup, CreatedAt: now,
	}
	if err := txMsgs.AppendTx(tx, msg); err != nil {
		return err
	}
	return s.appendEventTx(tx, botID, nil, "", model.BotEventDelta, botEventEnvelope{
		Kind:    "lifecycle",
		Payload: map[string]interface{}{"event": "policy_rule_autodisabled", "message_id": msg.ID},
	})
}

// detectPolicyInverseAcceptance 检测结算/撤销结算的往返（settle 后 30 分钟
// 内 unsettle，或反向）：方向相反的写说明前一次自动放行是误判。只从用户
// 决策路径（批准反向确认卡）调用——机器人自己受理的往返（自动放行、
// 定时任务默认放行）是正常的巡检行为，不算误判。
func (s *BotService) detectPolicyInverseAcceptance(botID, action, envID, threadID string) {
	inverse := "settle"
	if action == "settle" {
		inverse = "unsettle"
	}
	ruleID := botPolicyRuleForAction("fs_thread", inverse)
	if ruleID == "" || envID == "" || threadID == "" {
		return
	}
	if s.policyRecentAutoOnThread(botID, ruleID, envID, threadID) {
		s.recordPolicyMisjudgment(botID, ruleID, "自动"+botPolicyRuleName(ruleID)+"后 30 分钟内出现了方向相反的结算操作")
	}
}

// detectPolicyMisjudgeOnDecision 在确认决定落库后检测用户方向：拒批同目标写、
// 批准与刚自动放行动作方向相反的写（send「继续」后确认 stop、自动 unsettle
// 后批准 settle 卡或反向），都按 (rule, scope) 记一次误判。
func (s *BotService) detectPolicyMisjudgeOnDecision(botID string, call *model.BotToolCall, decision string) {
	var execCtx botCallExecContext
	_ = json.Unmarshal([]byte(call.ExecutionContextJSON), &execCtx)
	envID, threadID := botPolicyThreadTargetOf(execCtx)
	switch {
	case decision == string(model.BotDecisionReject):
		ruleID := botPolicyRuleForAction(call.ToolName, call.Action)
		if ruleID == "" {
			return
		}
		// send_continue 只认命中「继续」模板的正文：botPolicyRuleForAction
		// 按动作映射，任意 send 都算同类会让一张内容无关的拒批卡也记误判。
		if ruleID == botPolicyRuleSendContinue {
			var rejArgs botThreadArgs
			_ = json.Unmarshal([]byte(call.ArgumentsJSON), &rejArgs)
			if !botPolicyIsContinueTemplate(rejArgs.Text) {
				return
			}
		}
		if s.policyRecentAutoOnThread(botID, ruleID, envID, threadID) {
			s.recordPolicyMisjudgment(botID, ruleID, "用户拒批了同目标的同类写操作")
		}
	case decision == string(model.BotDecisionApprove) &&
		call.ToolName == "fs_thread" && call.Action == "stop":
		if s.policyRecentAutoOnThread(botID, botPolicyRuleSendContinue, envID, threadID) {
			s.recordPolicyMisjudgment(botID, botPolicyRuleSendContinue, "自动发送「继续」后用户批准了停止该线程")
		}
	case decision == string(model.BotDecisionApprove) &&
		call.ToolName == "fs_thread" && (call.Action == "settle" || call.Action == "unsettle"):
		// 确认卡上的反向操作与受理端往返同一事实：前一次自动放行的方向刚被
		// 用户亲手反转。复用受理端检测（同 rule、同线程、同 30 分钟窗口）。
		s.detectPolicyInverseAcceptance(botID, call.Action, envID, threadID)
	}
}

// ---------- 规则开关 API ----------

// BotWritePolicyRuleResponse 是规则开关的 API 投影：静态目录与库中行合并，
// 没落过行的规则按启用展示。
type BotWritePolicyRuleResponse struct {
	RuleID                string  `json:"rule_id"`
	Name                  string  `json:"name"`
	Description           string  `json:"description"`
	Enabled               bool    `json:"enabled"`
	FalsePositiveCount24h int     `json:"false_positive_count_24h"`
	AutoDisabledAt        *string `json:"auto_disabled_at"`
	UpdatedAt             *string `json:"updated_at"`
}

// ListWritePolicyRules 列出本机器人可独立开关的 tier1 规则及误判状态。
func (s *BotService) ListWritePolicyRules(userID, botID string) ([]BotWritePolicyRuleResponse, error) {
	if err := s.checkOwnership(userID, botID); err != nil {
		return nil, err
	}
	rows, err := repository.NewBotWritePolicyRuleRepository(s.db).ListByBot(botID)
	if err != nil {
		return nil, errs.ErrInternal
	}
	byID := map[string]*model.BotWritePolicyRule{}
	for i := range rows {
		byID[rows[i].RuleID] = &rows[i]
	}
	out := make([]BotWritePolicyRuleResponse, 0, len(botWritePolicyRuleCatalog))
	for _, info := range botWritePolicyRuleCatalog {
		entry := BotWritePolicyRuleResponse{
			RuleID:      info.ID,
			Name:        info.Name,
			Description: info.Description,
			Enabled:     true,
		}
		if row := byID[info.ID]; row != nil {
			entry.Enabled = row.Enabled
			entry.FalsePositiveCount24h = row.FalsePositiveCount24h
			entry.AutoDisabledAt = rfc3339(row.AutoDisabledAt)
			entry.UpdatedAt = rfc3339(&row.UpdatedAt)
		}
		out = append(out, entry)
	}
	return out, nil
}

// SetWritePolicyRuleEnabledRequest 是规则开关请求体（handler 校验只接受
// {"enabled":bool}）。
type SetWritePolicyRuleEnabledRequest struct {
	Enabled *bool `json:"enabled"`
}

// SetWritePolicyRuleEnabled 开关一条规则；不存在则落行。手动重新启用会清掉
// 自动降级时间戳与误判计数（重新开始累计）。
func (s *BotService) SetWritePolicyRuleEnabled(userID, botID, ruleID string, enabled bool) (*BotWritePolicyRuleResponse, error) {
	if err := s.checkOwnership(userID, botID); err != nil {
		return nil, err
	}
	known := false
	for _, info := range botWritePolicyRuleCatalog {
		if info.ID == ruleID {
			known = true
			break
		}
	}
	if !known {
		return nil, errs.New(errs.ErrInvalidParams.Code, "未知的分级放行规则: "+ruleID)
	}
	// 读-改-写包进事务：与 recordPolicyMisjudgment 的事务（同一行
	// UpsertEnabledTx）并发时互相覆盖会让手动重开把刚落库的
	// enabled=false/auto_disabled_at/计数写回启用态；事务内按最新行合并，
	// 提交序即裁决序。
	var row *model.BotWritePolicyRule
	txErr := s.db.Transaction(func(tx *gorm.DB) error {
		repo := repository.NewBotWritePolicyRuleRepository(tx)
		var err error
		row, err = repo.GetTx(tx, botID, ruleID)
		switch {
		case errors.Is(err, gorm.ErrRecordNotFound):
			row = &model.BotWritePolicyRule{BotID: botID, RuleID: ruleID}
		case err != nil:
			return err
		}
		row.Enabled = enabled
		if enabled {
			// 手动重开：清自动降级痕迹与误判计数，窗口重新开始。
			row.AutoDisabledAt = nil
			row.FalsePositiveCount24h = 0
			row.LastFalsePositiveAt = nil
		}
		row.UpdatedAt = time.Now().UTC()
		return repo.UpsertEnabledTx(tx, row)
	})
	if txErr != nil {
		return nil, errs.ErrInternal
	}
	return &BotWritePolicyRuleResponse{
		RuleID:                ruleID,
		Name:                  botPolicyRuleName(ruleID),
		Enabled:               row.Enabled,
		FalsePositiveCount24h: row.FalsePositiveCount24h,
		AutoDisabledAt:        rfc3339(row.AutoDisabledAt),
		UpdatedAt:             rfc3339(&row.UpdatedAt),
	}, nil
}

// ---------- 存量授权迁移 ----------

// MigrateTieredAuthorization 是 INT-160 的一次性迁移（幂等）：存量「始终允许」
// 没有过期字段、等于永久授权，统一撤销。只写 revoked_at 留痕不删——
// bot_tool_calls.authorization_id 外键是 ON DELETE CASCADE，物理删除会连带
// 删掉引用它的调用账本行。迁移后新建的 always-allow 一律带 30 天过期。
func (s *BotService) MigrateTieredAuthorization() error {
	res := s.db.Model(&model.BotAuthorization{}).
		Where("revoked_at IS NULL AND expires_at IS NULL").
		Update("revoked_at", time.Now().UTC())
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected > 0 {
		s.logger.Info("INT-160：存量永久「始终允许」已统一撤销（留痕不删，用户可按需重建限时授权）",
			zap.Int64("count", res.RowsAffected))
	}
	return nil
}

// botAuthorizationExpired 报告授权是否已过有效期（未过期为 false：nil 视为
// 未设置——理论上 INT-160 后不再产生）。
func botAuthorizationExpired(a *model.BotAuthorization, now time.Time) bool {
	return a.ExpiresAt != nil && !a.ExpiresAt.After(now)
}
