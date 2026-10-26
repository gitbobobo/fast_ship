package service

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/godbobo/fast_ship/server/internal/pkg/errs"
	"github.com/godbobo/fast_ship/server/internal/repository"
	"github.com/google/uuid"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

// 预算与超限告警（INT-164 第三期）：预算挂在机器人或某个定时任务上，周期为
// 每日或每月，单位美元，带一组告警阈值（如 0.8 = 80%、1 = 100%）。
//
// 口径与花费页一致：只统计已计价金额（cost_estimate，NULL = 未计价，不计入），
// 已计价缺失不记 0 也不外推。周期按用户有效时区切：daily = 当地日
// [零点, 现在)，monthly = 当月 1 日零点起至现在。机器人花费 = 窗口内全部 run
// （主线与 schedule）已计价成本 + 窗口内全部维护请求；任务花费 = 该任务的
// schedule run + 解析到该任务的 schedule_event 主线 run + 它们的关联维护。
//
// 告警只作提示，不停止运行；同一预算同一周期同一阈值只告警一次（主线
// lifecycle 消息 dedup 键 + 站内通知 problemKey 都以周期键与整数基点做键，
// 不含金额——金额变化不改变键，所以不会重复打扰）。未设置预算时本文件
// 的所有路径都不生效，系统行为与引入预算前完全一致。

const (
	// botBudgetEvalInterval 是预算评估周期；60s 与夜间整理扫描同频。
	// botBudgetKickMinInterval 是落账 kick 轮次的最小间隔，防止高频写入
	// 让窗口重扫近似连续执行。
	botBudgetEvalInterval    = 60 * time.Second
	botBudgetKickMinInterval = 10 * time.Second
	// botBudgetThresholdMax 是阈值上限（100 = 10000%）：允许超过 100% 的
	// 档位，但不接受离谱值。
	botBudgetThresholdMax = 100.0
	// botBudgetMaxThresholds 是单条预算的阈值个数上限，防止一次请求塞爆。
	botBudgetMaxThresholds = 16
)

// BotBudgetResponse 是一条预算的用户投影：配置 + 当前周期的已计价花费。
type BotBudgetResponse struct {
	ID          string    `json:"id"`
	Period      string    `json:"period"`
	AmountUSD   float64   `json:"amount_usd"`
	Thresholds  []float64 `json:"thresholds"`
	SpendUSD    float64   `json:"spend_usd"`
	PeriodKey   string    `json:"period_key"`
	WindowStart string    `json:"spend_window_start"`
	WindowEnd   string    `json:"spend_window_end"`
	CreatedAt   string    `json:"created_at"`
	UpdatedAt   string    `json:"updated_at"`
}

// BotBudgetUpsertRequest 是 PUT 的请求体：金额与阈值都必须显式给出。
type BotBudgetUpsertRequest struct {
	AmountUSD  float64   `json:"amount_usd"`
	Thresholds []float64 `json:"thresholds"`
}

// ---------- 阈值规范化与周期窗口 ----------

// botBudgetNormalizeThresholds 把阈值规范化为「整数基点、去重、升序」的
// 浮点列表并返回库内 JSON 文本。整数基点是告警键的规范形式（不用浮点
// 格式化），0.8 与 0.8000001 归一到同一档。
func botBudgetNormalizeThresholds(in []float64) ([]float64, string, error) {
	if len(in) == 0 {
		return nil, "", errs.New(errs.ErrInvalidParams.Code, "thresholds 至少需要一个告警阈值（如 0.8、1）")
	}
	if len(in) > botBudgetMaxThresholds {
		return nil, "", errs.New(errs.ErrInvalidParams.Code,
			fmt.Sprintf("thresholds 至多 %d 个", botBudgetMaxThresholds))
	}
	seen := make(map[int]struct{}, len(in))
	bps := make([]int, 0, len(in))
	for _, th := range in {
		if math.IsNaN(th) || math.IsInf(th, 0) || th <= 0 || th > botBudgetThresholdMax {
			return nil, "", errs.New(errs.ErrInvalidParams.Code,
				fmt.Sprintf("thresholds 的每个值必须在 (0, %.0f] 内（1 = 100%%）", botBudgetThresholdMax))
		}
		bp := botBudgetThresholdBasisPoints(th)
		if bp <= 0 {
			return nil, "", errs.New(errs.ErrInvalidParams.Code, "thresholds 的精度至少为 0.01%")
		}
		if _, dup := seen[bp]; dup {
			continue
		}
		seen[bp] = struct{}{}
		bps = append(bps, bp)
	}
	sort.Ints(bps)
	out := make([]float64, 0, len(bps))
	for _, bp := range bps {
		out = append(out, float64(bp)/10000)
	}
	raw, err := json.Marshal(out)
	if err != nil {
		return nil, "", err
	}
	return out, string(raw), nil
}

// botBudgetThresholdBasisPoints 把阈值换算成整数基点（1 = 10000bp =
// 100%）。告警键只认这个整数，避免浮点格式化带来的键漂移。
func botBudgetThresholdBasisPoints(threshold float64) int {
	return int(math.Round(threshold * 10000))
}

// botBudgetDecodeThresholds 读回库内阈值 JSON 文本。
func botBudgetDecodeThresholds(raw string) ([]float64, error) {
	var out []float64
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return nil, err
	}
	return out, nil
}

// botBudgetWindow 返回 period 在 now 所在周期的窗口与规范周期键。
// previous=true 时返回上一个完整周期（回看兜底：进程停摆期间跨界的阈值在
// 恢复后仍会补一条告警）。
func botBudgetWindow(period model.BotBudgetPeriod, now time.Time, loc *time.Location, previous bool) (start, end time.Time, key string) {
	local := now.In(loc)
	dayStart := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, loc)
	if period == model.BotBudgetPeriodMonthly {
		monthStart := time.Date(local.Year(), local.Month(), 1, 0, 0, 0, 0, loc)
		if previous {
			startLocal := monthStart.AddDate(0, -1, 0)
			return startLocal.UTC(), monthStart.UTC(), startLocal.Format("2006-01")
		}
		return monthStart.UTC(), now.UTC(), monthStart.Format("2006-01")
	}
	if previous {
		startLocal := dayStart.AddDate(0, 0, -1)
		return startLocal.UTC(), dayStart.UTC(), startLocal.Format("2006-01-02")
	}
	return dayStart.UTC(), now.UTC(), dayStart.Format("2006-01-02")
}

// botBudgetPeriodLabel 返回「日 / 月」，用于「每日预算 / 每月预算」文案。
func botBudgetPeriodLabel(period model.BotBudgetPeriod) string {
	if period == model.BotBudgetPeriodMonthly {
		return "月"
	}
	return "日"
}

// botBudgetThresholdReached 用基点比较花费与「金额 × 阈值」，避免浮点误差
// 把恰好等于阈值的边界判丢。全程 float64（math.Round 返回 float64），
// 不做 int64 转换——天文数字金额下 int64 溢出是实现定义行为。
func botBudgetThresholdReached(amountUSD, threshold, spendUSD float64) bool {
	return math.Round(spendUSD*10000) >= math.Round(amountUSD*threshold*10000)
}

// botBudgetMoney 是文案里的美元金额：小于 $1 保留至多 4 位小数并剥尾零
// （与 web formatCostEstimate 的分位段同口径，$0.0050 → $0.005），更大
// 金额按分位显示。
func botBudgetMoney(v float64) string {
	if math.Abs(v) < 1 {
		s := strings.TrimRight(strconv.FormatFloat(v, 'f', 4, 64), "0")
		return "$" + strings.TrimRight(s, ".")
	}
	return "$" + strconv.FormatFloat(v, 'f', 2, 64)
}

// botBudgetPercentText 把整数基点渲染成百分比文案（8000 → "80%"）。
func botBudgetPercentText(bp int) string {
	return strconv.FormatFloat(float64(bp)/100, 'f', -1, 64) + "%"
}

// botBudgetActualText 渲染当前占比：低于十倍按百分数（"328%"），达到十倍
// 改按倍数（4200% → "约 42 倍"）避免多位百分数难读。与前端
// formatSpendPercent 同口径。
func botBudgetActualText(pct float64) string {
	if pct < 1000 {
		return fmt.Sprintf("%.0f%%", pct)
	}
	return fmt.Sprintf("约 %.0f 倍", pct/100)
}

// ---------- 评估循环 ----------

// budgetEvalLoop 每分钟评估一次全部预算，usage 落账后的 kick 让它立即再评
// 一轮（容量 1 的 channel 天然合并连续写入）。首轮立即执行：启动即补一次
// 上一周期/当前周期已越过的阈值。评估失败只记日志，绝不影响机器人运行。
//
// 上一周期窗口只在首轮（停摆兜底）或 kick 触发的轮次里重扫：该窗口的花费
// 只可能因跨界前创建、切换后才落账的写而增长，而每一笔 usage 落账都会
// kick——纯 tick 轮次扫它是固定开销却没有新数据可看。kick 轮次带最小间隔：
// 高频落账时每条写都 kick，不节流会让月窗 SpendInWindow 重扫近似连续执行；
// 被节流的 kick 挂起 prev 重扫需求，并入下一次 tick/kick 合并执行。
func (s *BotService) budgetEvalLoop() {
	lastEval := time.Now()
	s.evalBudgetsOnce(true)
	ticker := time.NewTicker(botBudgetEvalInterval)
	defer ticker.Stop()
	pendingPrev := false
	for {
		includePrev := pendingPrev
		pendingPrev = false
		select {
		case <-ticker.C:
		case <-s.budgetKick:
			if time.Since(lastEval) < botBudgetKickMinInterval {
				pendingPrev = true
				continue
			}
			includePrev = true
		}
		s.evalBudgetsOnce(includePrev)
		lastEval = time.Now()
	}
}

// kickBudgetEval 在 usage 写成功后通知评估循环；非阻塞发送，通道满时
// 上一信号已代表本轮变化，丢信号由 60s tick 兜底。
func (s *BotService) kickBudgetEval() {
	select {
	case s.budgetKick <- struct{}{}:
	default:
	}
}

// evalBudgetsOnce 按用户分组评估全部预算：同一用户的窗口花费只扫描一次。
// includePrev 为 true 时连上一周期一起评（首轮停摆兜底 + usage 落账 kick
// 轮次——上一窗口的花费只可能因跨界 straggler 落账而增长）；纯 tick 轮次
// 传 false 跳过上一周期扫描。
func (s *BotService) evalBudgetsOnce(includePrev bool) {
	repo := repository.NewBotBudgetRepository(s.db)
	budgets, err := repo.ListAll()
	if err != nil {
		s.logger.Warn("预算评估读取预算失败", zap.Error(err))
		return
	}
	if len(budgets) == 0 {
		return
	}
	byUser := make(map[string][]model.BotBudget, 4)
	order := make([]string, 0, 4)
	for i := range budgets {
		userID := budgets[i].UserID
		if _, ok := byUser[userID]; !ok {
			order = append(order, userID)
		}
		byUser[userID] = append(byUser[userID], budgets[i])
	}
	for _, userID := range order {
		s.evalUserBudgets(repo, userID, byUser[userID], includePrev)
	}
}

// botBudgetWindowKey 是窗口花费缓存的键：同一用户同周期同「当前/上一」只扫
// 一次。
type botBudgetWindowKey struct {
	period   model.BotBudgetPeriod
	previous bool
}

// evalUserBudgets 评估一个用户的全部预算。预算或目标缺失（目标已删除由
// 外键级联清理，理论上不会出现）只跳过该条，不影响其他预算。
func (s *BotService) evalUserBudgets(repo *repository.BotBudgetRepository, userID string, budgets []model.BotBudget, includePrev bool) {
	_, loc := s.botUserTimezoneOf(userID)
	now := s.now()

	// 任务预算需要任务名与所属机器人；任务已删除时预算已被级联清理，
	// 查不到就跳过。
	tasks := make(map[string]*model.BotScheduleTask, len(budgets))
	botNames := make(map[string]string, len(budgets))
	botIDSet := make(map[string]struct{}, len(budgets))
	for i := range budgets {
		b := &budgets[i]
		if b.BotID != nil {
			botIDSet[*b.BotID] = struct{}{}
			continue
		}
		if b.ScheduleTaskID == nil {
			continue
		}
		task, err := s.schedTasks.FindByID(*b.ScheduleTaskID)
		if err != nil {
			// 任务不存在是正常路径（预算通常已被级联清理）；其他错误是
			// 瞬时读库失败，记 warn 便于排查预算为何长期不生效。
			if !errors.Is(err, gorm.ErrRecordNotFound) {
				s.logger.Warn("预算评估读取任务失败",
					zap.String("task_id", *b.ScheduleTaskID), zap.Error(err))
			}
			continue
		}
		tasks[task.ID] = task
		botIDSet[task.BotID] = struct{}{}
	}
	botIDs := make([]string, 0, len(botIDSet))
	for botID := range botIDSet {
		botIDs = append(botIDs, botID)
		if bot, err := s.bots.Get(botID); err == nil {
			botNames[botID] = bot.Name
		}
	}
	if len(botIDs) == 0 {
		return
	}

	spendCache := map[botBudgetWindowKey]repository.BotBudgetSpend{}
	loadSpend := func(key botBudgetWindowKey) (repository.BotBudgetSpend, bool) {
		if sp, ok := spendCache[key]; ok {
			return sp, true
		}
		start, end, _ := botBudgetWindow(key.period, now, loc, key.previous)
		sp, err := repo.SpendInWindow(botIDs, start, end)
		if err != nil {
			s.logger.Warn("预算花费聚合失败",
				zap.String("user_id", userID), zap.Error(err))
			return repository.BotBudgetSpend{}, false
		}
		spendCache[key] = sp
		return sp, true
	}

	for i := range budgets {
		b := &budgets[i]
		botID, taskName := "", ""
		if b.BotID != nil {
			botID = *b.BotID
		} else if b.ScheduleTaskID != nil {
			task, ok := tasks[*b.ScheduleTaskID]
			if !ok {
				continue
			}
			botID, taskName = task.BotID, task.Name
		}
		botName, ok := botNames[botID]
		if !ok {
			continue
		}
		thresholds, err := botBudgetDecodeThresholds(b.Thresholds)
		if err != nil {
			s.logger.Warn("预算阈值解析失败",
				zap.String("budget_id", b.ID), zap.Error(err))
			continue
		}
		for _, previous := range []bool{false, true} {
			if previous && !includePrev {
				continue
			}
			key := botBudgetWindowKey{period: b.Period, previous: previous}
			spend, ok := loadSpend(key)
			if !ok {
				break
			}
			var spentUSD float64
			if b.ScheduleTaskID != nil {
				spentUSD = spend.Task(*b.ScheduleTaskID)
			} else {
				spentUSD = spend.Bot(botID)
			}
			_, _, periodKey := botBudgetWindow(b.Period, now, loc, previous)
			for _, th := range thresholds {
				if !botBudgetThresholdReached(b.AmountUSD, th, spentUSD) {
					continue
				}
				alert := botBudgetAlert{
					budget:    b,
					botID:     botID,
					botName:   botName,
					taskName:  taskName,
					periodKey: periodKey,
					threshold: th,
					spendUSD:  spentUSD,
				}
				if err := s.alertBudgetThreshold(alert); err != nil {
					s.logger.Warn("预算告警发布失败",
						zap.String("budget_id", b.ID), zap.String("period_key", periodKey), zap.Error(err))
				}
			}
		}
	}
}

// botBudgetAlert 是一条待发布的告警现场。
type botBudgetAlert struct {
	budget    *model.BotBudget
	botID     string
	botName   string
	taskName  string
	periodKey string
	threshold float64
	spendUSD  float64
}

// alertBudgetThreshold 在同一个事务里写站内通知与主线 lifecycle 消息：
// 两者要么都出现，要么都不出现。消息 dedup 键先查重——同一（预算、周期、
// 阈值）已经告过警时直接返回，不重复打扰，也不刷通知的 last_seen_at。
func (s *BotService) alertBudgetThreshold(alert botBudgetAlert) error {
	b := alert.budget
	thBp := botBudgetThresholdBasisPoints(alert.threshold)
	pct := botBudgetPercentText(thBp)
	money := botBudgetMoney(alert.spendUSD)
	amount := botBudgetMoney(b.AmountUSD)
	label := botBudgetPeriodLabel(b.Period)

	actualPct := alert.spendUSD / b.AmountUSD * 100
	actualText := botBudgetActualText(actualPct)
	var title string
	if actualPct >= 100 {
		if b.ScheduleTaskID != nil {
			title = fmt.Sprintf("定时任务「%s」已超出每%s预算（阈值 %s，当前 %s）", alert.taskName, label, pct, actualText)
		} else {
			title = fmt.Sprintf("机器人「%s」已超出每%s预算（阈值 %s，当前 %s）", alert.botName, label, pct, actualText)
		}
	} else if b.ScheduleTaskID != nil {
		title = fmt.Sprintf("定时任务「%s」每%s预算已达 %s（当前 %s）", alert.taskName, label, pct, actualText)
	} else {
		title = fmt.Sprintf("机器人「%s」每%s预算已达 %s（当前 %s）", alert.botName, label, pct, actualText)
	}
	body := fmt.Sprintf("周期 %s 已花费 %s，预算 %s，当前占比 %s，告警阈值 %s。"+
		"只统计已计价金额，未计价请求未计入；告警只作提示，不会停止运行。",
		alert.periodKey, money, amount, actualText, pct)

	scope := "bot"
	taskID := ""
	if b.ScheduleTaskID != nil {
		scope = "schedule_task"
		taskID = *b.ScheduleTaskID
	}
	dedup := fmt.Sprintf("budget:%s:%s:%d", b.ID, alert.periodKey, thBp)
	note := fmt.Sprintf("已花费 %s，预算 %s，达到 %s 阈值（只统计已计价金额，告警不影响运行）",
		money, amount, pct)
	content := botLifecycleContent{
		Event: "budget_threshold",
		Detail: json.RawMessage(botMustJSON(map[string]interface{}{
			"budget_id":              b.ID,
			"scope":                  scope,
			"bot_id":                 alert.botID,
			"schedule_task_id":       taskID,
			"schedule_task_name":     alert.taskName,
			"period":                 string(b.Period),
			"period_key":             alert.periodKey,
			"amount_usd":             b.AmountUSD,
			"threshold":              alert.threshold,
			"threshold_basis_points": thBp,
			"spend_usd":              alert.spendUSD,
			"title":                  title,
			"note":                   note,
		})),
	}
	raw, err := json.Marshal(content)
	if err != nil {
		return err
	}
	created := false
	txErr := s.db.Transaction(func(tx *gorm.DB) error {
		txMsgs := repository.NewBotMessageRepository(tx)
		if _, err := txMsgs.FindByDedupKey(alert.botID, dedup); err == nil {
			return nil
		} else if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		if err := NewNotificationSink(tx).PublishTx(tx, NotificationInput{
			UserID:     b.UserID,
			Kind:       model.NotificationBotBudgetThreshold,
			ProblemKey: fmt.Sprintf("budget:%s:%s:%d", b.ID, alert.periodKey, thBp),
			// stateKey 不含金额与周期：同一预算同一档位的指纹固定，后续周期
			// 不会因金额变化清掉已读。
			StateKey: fmt.Sprintf("budget:%s:%d", b.ID, thBp),
			Title:    title,
			Body:     body,
		}); err != nil {
			return err
		}
		seq, err := s.bots.NextMessageSeqTx(tx, alert.botID)
		if err != nil {
			return err
		}
		msgID := uuid.NewString()
		msg := &model.BotMessage{
			ID: msgID, BotID: alert.botID, Seq: seq,
			Kind: model.BotMessageKindLifecycle, ContentJSON: string(raw),
			DedupKey: &dedup, CreatedAt: time.Now().UTC(),
		}
		if err := txMsgs.AppendTx(tx, msg); err != nil {
			return err
		}
		if err := s.appendEventTx(tx, alert.botID, nil, "", model.BotEventDelta, botEventEnvelope{
			Kind:    "lifecycle",
			Payload: map[string]interface{}{"event": "budget_threshold", "message_id": msgID},
		}); err != nil {
			return err
		}
		created = true
		return nil
	})
	if txErr != nil {
		return txErr
	}
	if created {
		s.publishEvent(alert.botID)
	}
	return nil
}

// ---------- 用户侧读/写服务 ----------

// botBudgetParsePeriod 校验路径里的周期段。
func botBudgetParsePeriod(raw string) (model.BotBudgetPeriod, error) {
	switch model.BotBudgetPeriod(raw) {
	case model.BotBudgetPeriodDaily:
		return model.BotBudgetPeriodDaily, nil
	case model.BotBudgetPeriodMonthly:
		return model.BotBudgetPeriodMonthly, nil
	}
	return "", errs.New(errs.ErrInvalidParams.Code, "周期必须是 daily 或 monthly")
}

// botBudgetValidateRequest 校验金额与阈值，返回金额与规范化后的阈值 JSON 文本。
func botBudgetValidateRequest(req *BotBudgetUpsertRequest) (float64, string, error) {
	if req == nil {
		return 0, "", errs.New(errs.ErrInvalidParams.Code, "请求体为空")
	}
	if math.IsNaN(req.AmountUSD) || math.IsInf(req.AmountUSD, 0) || req.AmountUSD <= 0 {
		return 0, "", errs.New(errs.ErrInvalidParams.Code, "amount_usd 必须是大于 0 的美元金额")
	}
	_, rawThresholds, err := botBudgetNormalizeThresholds(req.Thresholds)
	if err != nil {
		return 0, "", err
	}
	return req.AmountUSD, rawThresholds, nil
}

// ListBotBudgets 返回机器人预算（每日/每月各至多一条）与当前周期花费。
func (s *BotService) ListBotBudgets(userID, botID string) ([]BotBudgetResponse, error) {
	if err := s.checkOwnership(userID, botID); err != nil {
		return nil, err
	}
	rows, err := repository.NewBotBudgetRepository(s.db).ListByBot(botID)
	if err != nil {
		return nil, errs.ErrInternal
	}
	return s.botBudgetResponsesOf(userID, botID, "", rows)
}

// UpsertBotBudget 覆盖机器人某周期的预算。
func (s *BotService) UpsertBotBudget(userID, botID, period string, req *BotBudgetUpsertRequest) (*BotBudgetResponse, error) {
	parsed, err := botBudgetParsePeriod(period)
	if err != nil {
		return nil, err
	}
	amount, thresholds, err := botBudgetValidateRequest(req)
	if err != nil {
		return nil, err
	}
	if err := s.checkOwnership(userID, botID); err != nil {
		return nil, err
	}
	row := &model.BotBudget{
		ID: uuid.NewString(), UserID: userID, BotID: &botID,
		Period: parsed, AmountUSD: amount, Thresholds: thresholds,
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}
	if err := repository.NewBotBudgetRepository(s.db).Upsert(row); err != nil {
		return nil, errs.ErrInternal
	}
	// 新建/改预算立即评一轮（含上一周期回看——月中新建的预算也要补一次
	// 停摆语义的 catch-up），不必等下一个 tick。
	s.kickBudgetEval()
	saved, err := repository.NewBotBudgetRepository(s.db).ListByBot(botID)
	if err != nil {
		return nil, errs.ErrInternal
	}
	out, err := s.botBudgetResponsesOf(userID, botID, "", saved)
	if err != nil {
		return nil, err
	}
	for i := range out {
		if out[i].Period == string(parsed) {
			return &out[i], nil
		}
	}
	return nil, errs.ErrInternal
}

// DeleteBotBudget 删除机器人某周期的预算（幂等：不存在也算成功）。
func (s *BotService) DeleteBotBudget(userID, botID, period string) error {
	parsed, err := botBudgetParsePeriod(period)
	if err != nil {
		return err
	}
	if err := s.checkOwnership(userID, botID); err != nil {
		return err
	}
	if _, err := repository.NewBotBudgetRepository(s.db).DeleteByBotPeriod(botID, parsed); err != nil {
		return errs.ErrInternal
	}
	return nil
}

// botBudgetTask 校验任务归属（task.bot_id == botID 且 bot 属于用户）。
func (s *BotService) botBudgetTask(userID, botID, taskID string) (*model.BotScheduleTask, error) {
	if err := s.checkOwnership(userID, botID); err != nil {
		return nil, err
	}
	task, err := s.schedTasks.FindByIDAndBot(taskID, botID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errs.ErrBotScheduleTaskNotFound
		}
		return nil, errs.ErrInternal
	}
	return task, nil
}

// ListScheduleTaskBudgets 返回定时任务预算（每日/每月各至多一条）与当前
// 周期花费（只算该任务的口径）。
func (s *BotService) ListScheduleTaskBudgets(userID, botID, taskID string) ([]BotBudgetResponse, error) {
	task, err := s.botBudgetTask(userID, botID, taskID)
	if err != nil {
		return nil, err
	}
	rows, err := repository.NewBotBudgetRepository(s.db).ListByScheduleTask(task.ID)
	if err != nil {
		return nil, errs.ErrInternal
	}
	return s.botBudgetResponsesOf(userID, botID, task.ID, rows)
}

// UpsertScheduleTaskBudget 覆盖定时任务某周期的预算。
func (s *BotService) UpsertScheduleTaskBudget(userID, botID, taskID, period string, req *BotBudgetUpsertRequest) (*BotBudgetResponse, error) {
	parsed, err := botBudgetParsePeriod(period)
	if err != nil {
		return nil, err
	}
	amount, thresholds, err := botBudgetValidateRequest(req)
	if err != nil {
		return nil, err
	}
	task, err := s.botBudgetTask(userID, botID, taskID)
	if err != nil {
		return nil, err
	}
	row := &model.BotBudget{
		ID: uuid.NewString(), UserID: userID, ScheduleTaskID: &task.ID,
		Period: parsed, AmountUSD: amount, Thresholds: thresholds,
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}
	repo := repository.NewBotBudgetRepository(s.db)
	if err := repo.Upsert(row); err != nil {
		return nil, errs.ErrInternal
	}
	s.kickBudgetEval()
	saved, err := repo.ListByScheduleTask(task.ID)
	if err != nil {
		return nil, errs.ErrInternal
	}
	out, err := s.botBudgetResponsesOf(userID, botID, task.ID, saved)
	if err != nil {
		return nil, err
	}
	for i := range out {
		if out[i].Period == string(parsed) {
			return &out[i], nil
		}
	}
	return nil, errs.ErrInternal
}

// DeleteScheduleTaskBudget 删除定时任务某周期的预算（幂等）。
func (s *BotService) DeleteScheduleTaskBudget(userID, botID, taskID, period string) error {
	parsed, err := botBudgetParsePeriod(period)
	if err != nil {
		return err
	}
	task, err := s.botBudgetTask(userID, botID, taskID)
	if err != nil {
		return err
	}
	if _, err := repository.NewBotBudgetRepository(s.db).DeleteByScheduleTaskPeriod(task.ID, parsed); err != nil {
		return errs.ErrInternal
	}
	return nil
}

// botBudgetResponsesOf 把预算行投影成响应：当前周期花费按 taskID 为空与否
// 取机器人口径或任务口径，同一周期的窗口只扫描一次。
func (s *BotService) botBudgetResponsesOf(userID, botID, taskID string, rows []model.BotBudget) ([]BotBudgetResponse, error) {
	_, loc := s.botUserTimezoneOf(userID)
	now := s.now()
	repo := repository.NewBotBudgetRepository(s.db)
	spendByPeriod := map[model.BotBudgetPeriod]float64{}
	windows := map[model.BotBudgetPeriod][3]string{}
	out := make([]BotBudgetResponse, 0, len(rows))
	for i := range rows {
		row := &rows[i]
		spent, ok := spendByPeriod[row.Period]
		if !ok {
			start, end, key := botBudgetWindow(row.Period, now, loc, false)
			spend, err := repo.SpendInWindow([]string{botID}, start, end)
			if err != nil {
				return nil, errs.ErrInternal
			}
			if taskID == "" {
				spent = spend.Bot(botID)
			} else {
				spent = spend.Task(taskID)
			}
			spendByPeriod[row.Period] = spent
			windows[row.Period] = [3]string{
				start.UTC().Format(time.RFC3339), end.UTC().Format(time.RFC3339), key,
			}
		}
		thresholds, err := botBudgetDecodeThresholds(row.Thresholds)
		if err != nil {
			return nil, errs.ErrInternal
		}
		window := windows[row.Period]
		out = append(out, BotBudgetResponse{
			ID:          row.ID,
			Period:      string(row.Period),
			AmountUSD:   row.AmountUSD,
			Thresholds:  thresholds,
			SpendUSD:    spent,
			PeriodKey:   window[2],
			WindowStart: window[0],
			WindowEnd:   window[1],
			CreatedAt:   row.CreatedAt.UTC().Format(time.RFC3339Nano),
			UpdatedAt:   row.UpdatedAt.UTC().Format(time.RFC3339Nano),
		})
	}
	return out, nil
}
