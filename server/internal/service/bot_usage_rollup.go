package service

import (
	"encoding/json"
	"errors"
	"sort"
	"time"

	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/godbobo/fast_ship/server/internal/repository"
	"github.com/google/uuid"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

// 花费页按天汇总（INT-164）：bot_usage_daily 是实时账本的按日预聚合缓存，
// bot_usage_rollup_days 是逐 (bot,date) 的覆盖标记。本文件负责维护它们：
//
//   - usageRollupLoop 每 60s 调 scanUsageRollupOnce 扫一遍「有明细的现存
//     bot」，把 [最早明细日, 昨天] 内未覆盖/指纹不符的日期各走一个小事务
//     重算；当日行替换与覆盖标记 upsert 在同一事务提交。最近 2 个当地日
//     无条件重算（迟到 INSERT 靠计数指纹、收尾 UPDATE 靠 last_write 指纹
//     判脏，最近两天再兜底一次）。汇总失败只记日志，绝不影响机器人运行。
//   - 有效时区变化（标记上的 timezone ≠ 当前解析值）→ 整表清空该 bot 的
//     汇总与标记后重算——明细还在，date 按新时区重排。已删除 bot 的明细
//     已级联清理，date 永远停在写入时的时区口径（已知限制，见
//     model/bot_usage_daily.go 文件头注释）。
//   - 删除路径见 service/bot.go Delete：冲突预检 → 事务外
//     rollupBotUsageRange 补齐缺口（含今天的部分快照，失败中止删除）→
//     事务内复检全部冲突（含活动维护运行）+ rollupBotDaysTx 补事务期间
//     新脏的日期与今天 → 级联删除。汇总行/标记不对 bots 建外键，删除后
//     历史仍在。
//
// Start() 的接线（go s.usageRollupLoop() 与启动首次异步回填）由上层统一
// 完成；本文件只提供可被调用的入口。

// errBotRolledOut 哨兵：后台汇总提交前复检发现 bot 已删除（与删除竞速）——
// 该 bot 的剩余日期直接放弃，不算错误。
var errBotRolledOut = errors.New("bot usage rollup: bot deleted during rollup")

// usageRollupPlan 是一次单 bot 汇总计划：解析出的有效时区、需要重算的日期
// 区间（升序）与时区变更触发的整表清空标记。
type usageRollupPlan struct {
	tzName string
	loc    *time.Location
	spans  []repository.UsageDaySpan
	wipe   bool
}

// usageLocalDaySpan 把一个当地日标签映射成 UTC 闭开区间。
func usageLocalDaySpan(date string, loc *time.Location) repository.UsageDaySpan {
	// date 一律来自本服务的窗口/迭代生成，合法 YYYY-MM-DD。
	t, _ := time.ParseInLocation("2006-01-02", date, loc)
	return repository.UsageDaySpan{Date: date, Start: t.UTC(), End: t.AddDate(0, 0, 1).UTC()}
}

// usageTimezoneOf 解析用户有效时区；走传入的 db/tx 句柄（删除事务内不能用
// s.db 读 users——生产单连接会自锁）。用户行不存在按未声明处理，读库真错误
// 返回 error（写路径不能把瞬时失败静默当回退时区写进标记）。
func (s *BotService) usageTimezoneOf(db *gorm.DB, userID string) (string, *time.Location, error) {
	if userID == "" {
		name, loc := s.botEffectiveTimezone("")
		return name, loc, nil
	}
	var tz string
	if err := db.Model(&model.User{}).Where("id = ?", userID).Pluck("timezone", &tz).Error; err != nil {
		return "", nil, err
	}
	name, loc := s.botEffectiveTimezone(tz)
	return name, loc, nil
}

// usageRollupPlan 计算单 bot 的重算计划：范围是 [最早明细当地日, 截止日]
// （includeToday=false 截止昨天——周期扫描用；true 截止今天——删除前补齐
// 把今天的部分快照也落进汇总）。候选日里「未覆盖 ∪ 指纹不符 ∪ 最近 2 个
// 当地日」进入 spans；任何已存标记的时区与当前有效时区不同 → wipe=true
// 且全部日期重算（清空后明细会重建汇总，date 按新时区重排）。
func (s *BotService) usageRollupPlan(db *gorm.DB, bot *model.Bot, now time.Time, includeToday bool) (*usageRollupPlan, error) {
	repo := repository.NewBotUsageRollupRepository(db)
	tzName, loc, err := s.usageTimezoneOf(db, bot.UserID)
	if err != nil {
		return nil, err
	}
	plan := &usageRollupPlan{tzName: tzName, loc: loc}

	earliest, err := repo.EarliestUsageAt(bot.ID)
	if err != nil {
		return nil, err
	}
	if earliest == nil {
		return plan, nil // 没有明细，无日可算
	}
	localNow := now.In(loc)
	lastDay := time.Date(localNow.Year(), localNow.Month(), localNow.Day(), 0, 0, 0, 0, loc)
	if !includeToday {
		lastDay = lastDay.AddDate(0, 0, -1)
	}
	// 时区判脏先于「无日可算」短路：旧时区写下的行必须在新时区下清掉，
	// 哪怕新时区里一个可结算日都没有（明细全落在今天）。
	marks, err := repo.MarksByBot(bot.ID)
	if err != nil {
		return nil, err
	}
	markBy := make(map[string]model.BotUsageRollupDay, len(marks))
	for _, m := range marks {
		if m.Timezone != tzName {
			plan.wipe = true
		}
		markBy[m.Date] = m
	}
	if plan.wipe {
		markBy = nil // 清空后全部判未覆盖
	}

	firstLocal := earliest.In(loc)
	day := time.Date(firstLocal.Year(), firstLocal.Month(), firstLocal.Day(), 0, 0, 0, 0, loc)
	var spans []repository.UsageDaySpan
	for !day.After(lastDay) {
		spans = append(spans, repository.UsageDaySpan{
			Date:  day.Format("2006-01-02"),
			Start: day.UTC(),
			End:   day.AddDate(0, 0, 1).UTC(),
		})
		day = day.AddDate(0, 0, 1)
	}
	if len(spans) == 0 {
		return plan, nil
	}
	if plan.wipe {
		// 时区变更清空后全部日期按未覆盖重算，指纹比对无意义，直接全选。
		plan.spans = spans
		return plan, nil
	}

	// 最近 2 个当地日无条件重算：收尾 UPDATE/迟到写入的兜底窗口（指纹
	// 判定之外的第二道保险）。
	recent := map[string]bool{}
	for _, s := range spans[max(0, len(spans)-2):] {
		recent[s.Date] = true
	}
	// 廉价闸：账本水位探针不超标记所记最大写入时，已覆盖日自上次汇总
	// 后没有任何新写——指纹不可能变，只有「未覆盖 ∪ 最近 2 日」要算，
	// 跳过全历史 DayFingerprints（每 60s × 每 bot 的全账本聚合）。
	var markMax int64
	for _, m := range marks {
		if m.LastWriteNs > markMax {
			markMax = m.LastWriteNs
		}
	}
	// 探针经 strftime 毫秒截断，标记 LastWriteNs 带亚毫秒尾数——统一截到毫秒
	// 再比，消除混合精度比较（同毫秒写入由最近两日兜底重算覆盖）。
	markMax -= markMax % int64(time.Millisecond)
	probe, err := repo.SpanWriteMax(bot.ID, spans[0].Start, spans[len(spans)-1].End)
	if err != nil {
		return nil, err
	}
	if probe == nil || !probe.After(time.Unix(0, markMax)) {
		for _, span := range spans {
			if _, ok := markBy[span.Date]; !ok || recent[span.Date] {
				plan.spans = append(plan.spans, span)
			}
		}
		return plan, nil
	}

	fps, err := repo.DayFingerprints(bot.ID, spans)
	if err != nil {
		return nil, err
	}
	for _, span := range spans {
		mark, ok := markBy[span.Date]
		if !ok || recent[span.Date] || !fps[span.Date].Equal(mark) {
			plan.spans = append(plan.spans, span)
		}
	}
	return plan, nil
}

// usageDayBucketOf 把一条 run 扫描行解析成汇总归属桶：(kind, taskID)。
// 与 loadBotUsageLedger 的 applyRunBucket 同口径：schedule run → schedule；
// schedule_event 主线经 trigger_key 反查得出任务 → schedule_event，解析不出
// → main；其余 → main。
func usageDayBucketOf(row repository.UsageRunRow, taskByRefRun map[string]string) (string, *string) {
	switch {
	case row.SessionKind == model.BotSessionKindSchedule:
		return model.BotUsageDaySchedule, row.ScheduleTaskID
	case row.TriggerSource == string(model.BotTriggerScheduleEvent):
		if ref := botScheduleEventRunIDOf(row.TriggerKey); ref != "" {
			if taskID, ok := taskByRefRun[ref]; ok {
				return model.BotUsageDayScheduleEvent, &taskID
			}
		}
		return model.BotUsageDayMain, nil
	default:
		return model.BotUsageDayMain, nil
	}
}

// usageDayRows 算出一个 (bot,date) 的全部归属桶行（不落库）。全部读走传入
// tx——调用方可能在删除事务里（生产单连接）。口径与 loadBotUsageLedger 的
// 实时累加逐项对应：
//   - run 行记 runs/failed/skipped 与请求计数、token 合计、run 自身成本；
//     显示成本样本 = run 自身 + 全部关联维护（无窗口下限，与实时
//     maintByParent 同口径），真零保留、全空不入样。
//   - 当日创建的维护行把请求/成本/token 折进父 run 的桶（父行不存在或属于
//     其他 bot → maintenance 桶）；不产生 runs，也不进 display 样本。
//   - 金额/token 空集合保持 NULL（缺失 ≠ 0）。
func (s *BotService) usageDayRows(tx *gorm.DB, bot *model.Bot, span repository.UsageDaySpan, now time.Time) ([]model.BotUsageDaily, error) {
	usage := repository.NewBotUsageRepository(tx)
	roll := repository.NewBotUsageRollupRepository(tx)

	runs, err := usage.ListRunsInWindow([]string{bot.ID}, span.Start, span.End)
	if err != nil {
		return nil, err
	}
	runIDs := make([]string, 0, len(runs))
	for i := range runs {
		runIDs = append(runIDs, runs[i].ID)
	}
	runAgg, err := usage.RequestAggregatesByRun(runIDs)
	if err != nil {
		return nil, err
	}
	maintRows, err := usage.ListMaintenanceRunsInWindow([]string{bot.ID}, span.Start, span.End)
	if err != nil {
		return nil, err
	}
	maintIDs := make([]string, 0, len(maintRows))
	parentIDs := make([]string, 0, len(maintRows))
	for _, m := range maintRows {
		maintIDs = append(maintIDs, m.ID)
		if m.ParentRunID != nil {
			parentIDs = append(parentIDs, *m.ParentRunID)
		}
	}
	maintAgg, err := usage.MaintenanceRequestAggregatesByRun(maintIDs)
	if err != nil {
		return nil, err
	}
	parentByID, err := usage.ListRunsByIDs(parentIDs)
	if err != nil {
		return nil, err
	}
	// 显示成本样本要的是「该 run 名下全部关联维护」——无时间下限，与实时
	// 路径的 maintByParent 同口径，逐元素一致。
	maintByParent, err := usage.MaintenanceCostsByParentRun(runIDs)
	if err != nil {
		return nil, err
	}

	// schedule_event 反查与实时同源：窗口内 event run 与维护的父 run 一起
	// 收集引用 run id，再反查其 schedule_task_id。
	refIDs := []string{}
	for i := range runs {
		row := &runs[i]
		if row.SessionKind == model.BotSessionKindMain && row.TriggerSource == string(model.BotTriggerScheduleEvent) {
			if ref := botScheduleEventRunIDOf(row.TriggerKey); ref != "" {
				refIDs = append(refIDs, ref)
			}
		}
	}
	for _, row := range parentByID {
		p := row
		if p.SessionKind == model.BotSessionKindMain && p.TriggerSource == string(model.BotTriggerScheduleEvent) {
			if ref := botScheduleEventRunIDOf(p.TriggerKey); ref != "" {
				refIDs = append(refIDs, ref)
			}
		}
	}
	taskByRefRun, err := usage.ScheduleTaskIDsByRunIDs(refIDs)
	if err != nil {
		return nil, err
	}

	type bucketKey struct {
		kind   string
		taskID string // "" = NULL
	}
	type bucketAcc struct {
		row     *model.BotUsageDaily
		display []float64
	}
	accs := map[bucketKey]*bucketAcc{}
	taskIDs := map[string]bool{}
	accOf := func(kind string, taskID *string) *bucketAcc {
		key := bucketKey{kind: kind}
		if taskID != nil {
			key.taskID = *taskID
			taskIDs[*taskID] = true
		}
		acc := accs[key]
		if acc == nil {
			acc = &bucketAcc{row: &model.BotUsageDaily{
				ID:             uuid.NewString(),
				UserID:         bot.UserID,
				BotID:          bot.ID,
				BotName:        bot.Name,
				ScheduleTaskID: taskID,
				Date:           span.Date,
				Kind:           kind,
				RolledAt:       now,
				DisplayCosts:   "[]",
			}}
			accs[key] = acc
		}
		return acc
	}

	for i := range runs {
		row := &runs[i]
		agg := runAgg[row.ID]
		kind, taskID := usageDayBucketOf(*row, taskByRefRun)
		acc := accOf(kind, taskID)
		acc.row.Runs++
		switch model.BotRunStatus(row.Status) {
		case model.BotRunStatusFailed:
			acc.row.Failed++
		case model.BotRunStatusSkipped:
			acc.row.Skipped++
		}
		acc.row.Requests += agg.Total
		acc.row.UnpricedRequests += agg.Total - agg.Counted
		acc.row.PromptTokens += agg.PromptSum
		acc.row.CachedTokens += agg.CachedSum
		acc.row.CompletionTokens += agg.CompletionSum
		acc.row.ReasoningTokens = optInt64Sum(acc.row.ReasoningTokens, agg.ReasoningSum)
		// run 自身金额与实时读取同口径（runDisplayOwn：请求级实收优先，
		// 请求行缺失退回 run 快照）；display 样本 = 自身 + 关联维护合计。
		own := runDisplayOwn(row.CostEstimate, agg)
		acc.row.DisplayCost = botSumOptional(acc.row.DisplayCost, own)
		if display := botSumOptional(own, maintByParent[row.ID]); display != nil {
			acc.display = append(acc.display, *display)
		}
	}
	for _, m := range maintRows {
		agg := maintAgg[m.ID]
		kind, taskID := model.BotUsageDayMaintenance, (*string)(nil)
		if m.ParentRunID != nil {
			// 桶跟父 run 走；父 run 行不存在或属于其他 bot 才落 maintenance。
			if parent, ok := parentByID[*m.ParentRunID]; ok && parent.BotID == bot.ID {
				kind, taskID = usageDayBucketOf(parent, taskByRefRun)
			}
		}
		acc := accOf(kind, taskID)
		acc.row.Requests += agg.Total
		acc.row.UnpricedRequests += agg.Total - agg.Counted
		acc.row.PromptTokens += agg.PromptSum
		acc.row.CachedTokens += agg.CachedSum
		acc.row.CompletionTokens += agg.CompletionSum
		acc.row.ReasoningTokens = optInt64Sum(acc.row.ReasoningTokens, agg.ReasoningSum)
		acc.row.DisplayCost = botSumOptional(acc.row.DisplayCost, agg.DisplaySum)
	}

	// 任务名快照在收集完 task 集合后一次补齐；已删除任务留空串。
	taskNames, err := roll.ScheduleTaskNamesByIDs(func() []string {
		ids := make([]string, 0, len(taskIDs))
		for id := range taskIDs {
			ids = append(ids, id)
		}
		return ids
	}())
	if err != nil {
		return nil, err
	}
	keys := make([]bucketKey, 0, len(accs))
	for k := range accs {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].kind != keys[j].kind {
			return keys[i].kind < keys[j].kind
		}
		return keys[i].taskID < keys[j].taskID
	})
	rows := make([]model.BotUsageDaily, 0, len(accs))
	for _, k := range keys {
		acc := accs[k]
		if k.taskID != "" {
			acc.row.TaskName = taskNames[k.taskID]
		}
		if len(acc.display) > 0 {
			enc, err := json.Marshal(acc.display)
			if err != nil {
				return nil, err
			}
			acc.row.DisplayCosts = string(enc)
		}
		rows = append(rows, *acc.row)
	}
	return rows, nil
}

// rollupBotDayTx 在事务内替换一个 (bot,date) 的全部桶行并写覆盖标记：
// 明细指纹用与判脏同一函数（DayFingerprints）在事务内现算——写入的口径
// 就是判定的口径。
func (s *BotService) rollupBotDayTx(tx *gorm.DB, bot *model.Bot, tzName string, span repository.UsageDaySpan, now time.Time) error {
	repo := repository.NewBotUsageRollupRepository(tx)
	rows, err := s.usageDayRows(tx, bot, span, now)
	if err != nil {
		return err
	}
	// 任务名快照补漏：任务已删时活名查不到会写成空串——先沿用同日上一版
	// 快照，再用删除时写下的名字墓碑覆盖（墓碑是删除时刻的名字，比旧快照
	// 新；整表清空重建时旧快照已没了，墓碑是唯一的名字来源）。
	needsPrev := false
	for i := range rows {
		if rows[i].ScheduleTaskID != nil && rows[i].TaskName == "" {
			needsPrev = true
			break
		}
	}
	if needsPrev {
		var taskIDs []string
		for i := range rows {
			if rows[i].ScheduleTaskID != nil && rows[i].TaskName == "" {
				taskIDs = append(taskIDs, *rows[i].ScheduleTaskID)
			}
		}
		prev, err := repo.PrevTaskNamesTx(tx, bot.ID, span.Date)
		if err != nil {
			return err
		}
		tomb, err := repo.TaskNameTombstonesTx(tx, taskIDs)
		if err != nil {
			return err
		}
		for i := range rows {
			if rows[i].ScheduleTaskID == nil {
				continue
			}
			if rows[i].TaskName == "" {
				rows[i].TaskName = prev[*rows[i].ScheduleTaskID]
			}
			if name := tomb[*rows[i].ScheduleTaskID]; name != "" {
				rows[i].TaskName = name
			}
		}
	}
	fps, err := repo.DayFingerprints(bot.ID, []repository.UsageDaySpan{span})
	if err != nil {
		return err
	}
	fp := fps[span.Date]
	mark := &model.BotUsageRollupDay{
		UserID:            bot.UserID,
		BotID:             bot.ID,
		Date:              span.Date,
		Timezone:          tzName,
		RolledAt:          now,
		RunCount:          fp.RunCount,
		MaintRunCount:     fp.MaintRunCount,
		RequestCount:      fp.RequestCount,
		MaintRequestCount: fp.MaintRequestCount,
		LastWriteNs:       fp.LastWriteNs,
	}
	return repo.ReplaceDayTx(tx, rows, mark)
}

// rollupBotDaysTx 在既有事务内补齐该 bot 的汇总缺口（含今天）——删除事务
// 用：全部查询走 tx（生产单连接，混用 s.db 会自锁）。计划/清空/逐日替换
// 都在调用方的事务里提交。
func (s *BotService) rollupBotDaysTx(tx *gorm.DB, bot *model.Bot, now time.Time) error {
	plan, err := s.usageRollupPlan(tx, bot, now, true)
	if err != nil {
		return err
	}
	repo := repository.NewBotUsageRollupRepository(tx)
	if plan.wipe {
		if err := repo.WipeBotTx(tx, bot.ID); err != nil {
			return err
		}
	}
	for _, span := range plan.spans {
		if err := s.rollupBotDayTx(tx, bot, plan.tzName, span, now); err != nil {
			return err
		}
	}
	return nil
}

// rollupBotUsageRange 在事务外补齐该 bot 的全部汇总缺口（含今天）：删除
// 路径在正式删除事务之前调用——逐日各走一个小事务，补齐失败中止删除。
func (s *BotService) rollupBotUsageRange(botID string) error {
	return s.rollupBotUsage(botID, s.now(), true, false)
}

// rollupBotUsage 是扫描与删除路径共用的单 bot 汇总流程：bot 已消失直接
// 返回；时区变更先整表清空再重算；逐日一个小事务，提交前复检 bot 仍存在
// （与删除竞速）。keepGoing=true（周期扫描）单日失败记 warn 继续下一天；
// false（删除前补齐）首个失败即返回。
func (s *BotService) rollupBotUsage(botID string, now time.Time, includeToday, keepGoing bool) error {
	bot, err := s.bots.Get(botID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		return err
	}
	plan, err := s.usageRollupPlan(s.db, bot, now, includeToday)
	if err != nil {
		return err
	}
	if plan.wipe {
		err := s.db.Transaction(func(tx *gorm.DB) error {
			// 与逐日写同一复检：清空若抢在删除归档后提交，会把刚归档的
			// 历史行抹掉——bot 已删直接收工，不动它的汇总。
			exists, err := repository.NewBotUsageRollupRepository(tx).BotExistsTx(tx, bot.ID)
			if err != nil {
				return err
			}
			if !exists {
				return errBotRolledOut
			}
			return repository.NewBotUsageRollupRepository(tx).WipeBotTx(tx, bot.ID)
		})
		if errors.Is(err, errBotRolledOut) {
			return nil
		}
		if err != nil {
			return err
		}
	}
	for _, span := range plan.spans {
		err := s.db.Transaction(func(tx *gorm.DB) error {
			exists, err := repository.NewBotUsageRollupRepository(tx).BotExistsTx(tx, bot.ID)
			if err != nil {
				return err
			}
			if !exists {
				return errBotRolledOut
			}
			return s.rollupBotDayTx(tx, bot, plan.tzName, span, now)
		})
		if errors.Is(err, errBotRolledOut) {
			return nil // bot 在补齐期间被删：剩余日期没意义，直接收工
		}
		if err != nil {
			s.logger.Warn("按天花费汇总失败",
				zap.String("bot_id", bot.ID), zap.String("date", span.Date), zap.Error(err))
			if !keepGoing {
				return err
			}
		}
	}
	return nil
}

// scanUsageRollupOnce 扫一遍明细账本里有行的现存 bot，逐个补齐汇总缺口
// （截至各 bot 归属用户时区的昨天）。单 (bot,date) 失败记日志继续——汇总
// 只是缓存，永不能让一次失败拖住其他 bot 或机器人运行本身。
func (s *BotService) scanUsageRollupOnce() {
	now := s.now()
	ids, err := repository.NewBotUsageRollupRepository(s.db).BotIDsWithUsage()
	if err != nil {
		s.logger.Warn("按天花费汇总扫描失败", zap.Error(err))
		return
	}
	for _, botID := range ids {
		if err := s.rollupBotUsage(botID, now, false, true); err != nil {
			s.logger.Warn("按天花费汇总失败", zap.String("bot_id", botID), zap.Error(err))
		}
	}
}

// usageRollupLoop 是周期汇总入口（INT-164）：60s 一跳调用
// scanUsageRollupOnce，写法对齐 nightlyLoop。Start() 里 go 起来，首个
// tick 前的启动补齐由启动方另外 go 一次 scanUsageRollupOnce（首次上线
// 的全量回填就是「所有日期都未覆盖」的同一条路径）。
func (s *BotService) usageRollupLoop() {
	ticker := time.NewTicker(60 * time.Second)
	defer ticker.Stop()
	for range ticker.C {
		s.scanUsageRollupOnce()
	}
}
