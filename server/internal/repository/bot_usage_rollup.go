package repository

import (
	"time"

	"github.com/godbobo/fast_ship/server/internal/model"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// BotUsageRollupRepository 是花费页按天汇总（INT-164）的读写层：
// bot_usage_daily 的整日替换写入与窗口读取、bot_usage_rollup_days 覆盖标记的
// 读写、以及「当前明细对某个当地日的指纹」计算。
//
// 与 BotUsageRepository 的分工：后者只读实时账本做窗口聚合；本仓库维护
// 汇总表的落库与新鲜度判定。汇总只是缓存，账本仍是事实来源——指纹不符
// 或时区变更的日期一律回退实时重算。
//
// 事务纪律：ReplaceDayTx / WipeBotTx / StampBotNameTx 等写方法一律走传入
// tx（生产连接池单连接，事务内任何走 s.db 的读写都会自锁）。读方法按
// 调用方所在的连接构造（事务内 NewBotUsageRollupRepository(tx)）。
type BotUsageRollupRepository struct {
	db *gorm.DB
}

func NewBotUsageRollupRepository(db *gorm.DB) *BotUsageRollupRepository {
	return &BotUsageRollupRepository{db: db}
}

// UsageDaySpan 是一个当地日的 UTC 闭开区间与日期标签。
type UsageDaySpan struct {
	Date  string
	Start time.Time
	End   time.Time
}

// UsageFingerprint 是一个当地日的明细指纹：当日 run 数、当日 run 名下请求
// 数、触及当日的维护运行数（当日创建 ∪ 父 run 属当日）、这些维护名下请求数、
// 相关行最近一次 created/finished 写入（unix nano）。迟到 INSERT 改变计数、
// 收尾 UPDATE 推进 LastWriteNs——两类迟写都能把该日判脏。维护同时计入自身
// 创建日与父 run 日：它的成本按创建日折进父桶，同时刷新父日的显示成本样本，
// 两边缺一边都会留下过期汇总。
type UsageFingerprint struct {
	RunCount          int64
	MaintRunCount     int64
	RequestCount      int64
	MaintRequestCount int64
	LastWriteNs       int64
}

// Equal 判定指纹与一条覆盖标记记录的指纹完全一致；零值指纹等价于「该日
// 没有任何明细」。
func (f UsageFingerprint) Equal(m model.BotUsageRollupDay) bool {
	return f.RunCount == m.RunCount &&
		f.MaintRunCount == m.MaintRunCount &&
		f.RequestCount == m.RequestCount &&
		f.MaintRequestCount == m.MaintRequestCount &&
		f.LastWriteNs == m.LastWriteNs
}

// BotIDsWithUsage 返回明细账本里出现过的全部 bot_id（现存 bot 去重）。
// 已删除 bot 的明细已级联清理，不会出现在结果里。
func (r *BotUsageRollupRepository) BotIDsWithUsage() ([]string, error) {
	var ids []string
	err := r.db.Raw(
		"SELECT bot_id FROM bot_runs UNION SELECT bot_id FROM bot_maintenance_runs ORDER BY bot_id",
	).Scan(&ids).Error
	return ids, err
}

// EarliestUsageAt 返回该 bot 明细账本的最早创建时刻（bot_runs 与
// bot_maintenance_runs 的 MIN(created_at) 并集）；无任何明细返回 nil。
// 聚合结果不带列类型，驱动按文本返回——strftime 规整后与指纹同一套解析。
func (r *BotUsageRollupRepository) EarliestUsageAt(botID string) (*time.Time, error) {
	var raw *string
	err := r.db.Raw(
		"SELECT strftime('%Y-%m-%d %H:%M:%f', MIN(created_at)) FROM ("+
			"SELECT created_at FROM bot_runs WHERE bot_id = ? "+
			"UNION ALL SELECT created_at FROM bot_maintenance_runs WHERE bot_id = ?)",
		botID, botID).Scan(&raw).Error
	if err != nil {
		return nil, err
	}
	if ts, ok := usageParseWriteStamp(raw); ok {
		return &ts, nil
	}
	return nil, nil
}

// DayFingerprints 计算 spans 内每个当地日当前的明细指纹，只返回 spans 覆盖
// 的日期。汇总任务拿它与覆盖标记比对判脏；当日行落库时把同一函数的结果写进
// 标记——判定与落库同源，不会出现「判脏口径与写入口径不一致」的无限重算。
func (r *BotUsageRollupRepository) DayFingerprints(botID string, spans []UsageDaySpan) (map[string]UsageFingerprint, error) {
	out := map[string]UsageFingerprint{}
	if len(spans) == 0 {
		return out, nil
	}
	// spans 是连续的当地日区间，按包含关系把 created_at 落到日期标签。
	labelOf := func(t time.Time) (string, bool) {
		for _, s := range spans {
			if !t.Before(s.Start) && t.Before(s.End) {
				return s.Date, true
			}
		}
		return "", false
	}
	bump := func(date string, finished, started time.Time) {
		fp := out[date]
		for _, t := range []time.Time{started, finished} {
			if !t.IsZero() && t.UnixNano() > fp.LastWriteNs {
				fp.LastWriteNs = t.UnixNano()
			}
		}
		out[date] = fp
	}

	rangeStart, rangeEnd := spans[0].Start, spans[len(spans)-1].End
	type runRow struct {
		ID         string     `gorm:"column:id"`
		CreatedAt  time.Time  `gorm:"column:created_at"`
		FinishedAt *time.Time `gorm:"column:finished_at"`
	}
	var runs []runRow
	if err := r.db.Model(&model.BotRun{}).
		Select("id, created_at, finished_at").
		Where("bot_id = ? AND created_at >= ? AND created_at < ?", botID, rangeStart, rangeEnd).
		Scan(&runs).Error; err != nil {
		return nil, err
	}
	runDay := make(map[string]string, len(runs))
	runIDs := make([]string, 0, len(runs))
	for _, row := range runs {
		date, ok := labelOf(row.CreatedAt)
		if !ok {
			continue
		}
		runDay[row.ID] = date
		runIDs = append(runIDs, row.ID)
		fp := out[date]
		fp.RunCount++
		out[date] = fp
		if row.FinishedAt != nil {
			bump(date, *row.FinishedAt, row.CreatedAt)
		} else {
			bump(date, time.Time{}, row.CreatedAt)
		}
	}
	// 当日 run 名下的请求计数与最近写入（请求创建/收尾时刻都在窗口外也计入：
	// 计数语义是「属当日 run 的全部请求」）。聚合表达式结果不带列类型，驱动
	// 返回的是文本——strftime 先规整成固定 UTC 毫秒串再回 Go 解析。
	type reqRow struct {
		RunID     string  `gorm:"column:run_id"`
		Cnt       int64   `gorm:"column:cnt"`
		LastWrite *string `gorm:"column:last_write"`
	}
	for _, batch := range ChunkIDs(runIDs, UsageQueryBatch) {
		var rows []reqRow
		err := r.db.Model(&model.BotModelRequest{}).
			Select("run_id, COUNT(*) AS cnt, MAX(COALESCE(finished_at, started_at)) AS last_write").
			Where("run_id IN ?", batch).
			Group("run_id").
			Scan(&rows).Error
		if err != nil {
			return nil, err
		}
		for _, row := range rows {
			date, ok := runDay[row.RunID]
			if !ok {
				continue
			}
			fp := out[date]
			fp.RequestCount += row.Cnt
			out[date] = fp
			if ts, ok := usageParseWriteStampNs(row.LastWrite); ok {
				bump(date, ts, time.Time{})
			}
		}
	}

	// 维护运行：当日创建 ∪ 父 run 属当日（两条都计入对应日的指纹）。
	var maint []struct {
		ID          string     `gorm:"column:id"`
		ParentRunID *string    `gorm:"column:parent_run_id"`
		CreatedAt   time.Time  `gorm:"column:created_at"`
		FinishedAt  *time.Time `gorm:"column:finished_at"`
	}
	// 条件：当日创建 ∪ 父 run 属当日。父集合用子查询而不是 IN runIDs——
	// runIDs 是区间内全部 run，绑进参数列表会随历史增长顶爆 SQLite 变量
	// 上限（删除路径的失败还会让 bot 删不掉）；子查询只绑一个 bot_id。
	parentRuns := r.db.Model(&model.BotRun{}).
		Select("id").
		Where("bot_id = ? AND created_at >= ? AND created_at < ?", botID, rangeStart, rangeEnd)
	scope := r.db.Where("created_at >= ? AND created_at < ?", rangeStart, rangeEnd).
		Or("parent_run_id IN (?)", parentRuns)
	maintQuery := r.db.Model(&model.BotMaintenanceRun{}).
		Select("id, parent_run_id, created_at, finished_at").
		Where("bot_id = ?", botID).
		Where(scope)
	if err := maintQuery.Scan(&maint).Error; err != nil {
		return nil, err
	}
	maintDays := map[string][]string{} // maint id → 触及的日期集合
	maintSet := map[string]struct{}{}  // 全部触及维护 id（请求计数用）
	for _, m := range maint {
		var days []string
		if date, ok := labelOf(m.CreatedAt); ok {
			days = append(days, date)
		}
		if m.ParentRunID != nil {
			if date, ok := runDay[*m.ParentRunID]; ok {
				if len(days) == 0 || days[0] != date {
					days = append(days, date)
				}
			}
		}
		if len(days) == 0 {
			continue
		}
		maintDays[m.ID] = days
		maintSet[m.ID] = struct{}{}
		for _, date := range days {
			fp := out[date]
			fp.MaintRunCount++
			out[date] = fp
			if m.FinishedAt != nil {
				bump(date, *m.FinishedAt, m.CreatedAt)
			} else {
				bump(date, time.Time{}, m.CreatedAt)
			}
		}
	}
	maintIDs := make([]string, 0, len(maintSet))
	for id := range maintSet {
		maintIDs = append(maintIDs, id)
	}
	type maintReqRow struct {
		MaintID   string  `gorm:"column:maintenance_run_id"`
		Cnt       int64   `gorm:"column:cnt"`
		LastWrite *string `gorm:"column:last_write"`
	}
	for _, batch := range ChunkIDs(maintIDs, UsageQueryBatch) {
		var rows []maintReqRow
		err := r.db.Model(&model.BotMaintenanceRequest{}).
			Select("maintenance_run_id, COUNT(*) AS cnt, MAX(COALESCE(finished_at, started_at)) AS last_write").
			Where("maintenance_run_id IN ?", batch).
			Group("maintenance_run_id").
			Scan(&rows).Error
		if err != nil {
			return nil, err
		}
		for _, row := range rows {
			for _, date := range maintDays[row.MaintID] {
				fp := out[date]
				fp.MaintRequestCount += row.Cnt
				out[date] = fp
				if ts, ok := usageParseWriteStampNs(row.LastWrite); ok {
					bump(date, ts, time.Time{})
				}
			}
		}
	}
	return out, nil
}

// SpanWriteMax 返回指纹统计范围内全部明细行的最大写入时刻（与指纹取数
// 口径一致：run/维护按 COALESCE(finished_at, created_at)，请求按
// COALESCE(finished_at, started_at)；维护集合是「区间内创建 ∪ 父 run 属
// 区间」）。汇总扫描用它做廉价闸：探针值不超标记所记最大写入时，已覆盖
// 日不可能变脏，不必跑全历史 DayFingerprints。范围内无行返回 nil。
func (r *BotUsageRollupRepository) SpanWriteMax(botID string, start, end time.Time) (*time.Time, error) {
	parentRuns := "SELECT id FROM bot_runs WHERE bot_id = ? AND created_at >= ? AND created_at < ?"
	maintScope := "(created_at >= ? AND created_at < ? OR parent_run_id IN (" + parentRuns + "))"
	maintInScope := "SELECT id FROM bot_maintenance_runs WHERE bot_id = ? AND " + maintScope
	var raw *string
	err := r.db.Raw(
		"SELECT MAX(ts) FROM ("+
			"SELECT COALESCE(finished_at, created_at) AS ts FROM bot_runs "+
			"WHERE bot_id = ? AND created_at >= ? AND created_at < ? "+
			"UNION ALL SELECT COALESCE(finished_at, started_at) FROM bot_model_requests "+
			"WHERE run_id IN ("+parentRuns+") "+
			"UNION ALL SELECT COALESCE(finished_at, created_at) FROM bot_maintenance_runs "+
			"WHERE bot_id = ? AND "+maintScope+" "+
			"UNION ALL SELECT COALESCE(finished_at, started_at) FROM bot_maintenance_requests "+
			"WHERE maintenance_run_id IN ("+maintInScope+"))",
		botID, start, end,
		botID, start, end,
		botID, start, end, botID, start, end,
		botID, start, end, botID, start, end,
	).Scan(&raw).Error
	if err != nil {
		return nil, err
	}
	if ts, ok := usageParseWriteStampNs(raw); ok {
		return &ts, nil
	}
	return nil, nil
}

// usageParseWriteStamp 解析指纹 SQL 里 strftime 规整出的 UTC 时刻
// （"2006-01-02 15:04:05.000"，毫秒精度）。毫秒内的同刻二次写入靠「最近 2
// 个当地日无条件重算」兜底——指纹只是脏判定的第一道。
func usageParseWriteStamp(s *string) (time.Time, bool) {
	if s == nil {
		return time.Time{}, false
	}
	t, err := time.Parse("2006-01-02 15:04:05.000", *s)
	return t, err == nil
}

// usageParseWriteStampNs 解析账本里直接存出的时刻文本（驱动落库格式
// "2006-01-02 15:04:05.999999+00:00"，纳秒精度），同时兼容 RFC3339 的
// T 分隔与 Z 后缀形态。与标记 LastWriteNs 的比值必须是同一精度——毫秒
// 截断会让同毫秒的迟到写入永远压不过水位。
func usageParseWriteStampNs(s *string) (time.Time, bool) {
	if s == nil {
		return time.Time{}, false
	}
	for _, layout := range []string{
		"2006-01-02 15:04:05.999999999Z07:00",
		"2006-01-02T15:04:05.999999999Z07:00",
	} {
		if t, err := time.Parse(layout, *s); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

// MarksByBot 返回该 bot 的全部覆盖标记（历史无界但单行极小，整取）。
func (r *BotUsageRollupRepository) MarksByBot(botID string) ([]model.BotUsageRollupDay, error) {
	var rows []model.BotUsageRollupDay
	err := r.db.Where("bot_id = ?", botID).Find(&rows).Error
	return rows, err
}

// MarksForBotsDates 返回一组 bot 在指定日期集合上的覆盖标记（混合查询的
// 新鲜度判定）。
func (r *BotUsageRollupRepository) MarksForBotsDates(botIDs, dates []string) ([]model.BotUsageRollupDay, error) {
	if len(botIDs) == 0 || len(dates) == 0 {
		return nil, nil
	}
	var rows []model.BotUsageRollupDay
	err := r.db.Where("bot_id IN ? AND date IN ?", botIDs, dates).Find(&rows).Error
	return rows, err
}

// ListDailyRows 返回某用户窗口日期内的全部汇总行（含已删除 bot 的行），
// 按 (bot_id, date, kind) 稳定排序供混合折叠。
func (r *BotUsageRollupRepository) ListDailyRows(userID string, dates []string) ([]model.BotUsageDaily, error) {
	if len(dates) == 0 {
		return nil, nil
	}
	var rows []model.BotUsageDaily
	err := r.db.Where("user_id = ? AND date IN ?", userID, dates).
		Order("bot_id ASC, date ASC, kind ASC").Find(&rows).Error
	return rows, err
}

// ReplaceDayTx 在事务内整日替换一个 (bot,date) 的桶行并 upsert 覆盖标记：
// 先删后插当日行 + 标记——同一事务提交，读者要么看到旧日要么看到新日。
func (r *BotUsageRollupRepository) ReplaceDayTx(tx *gorm.DB, rows []model.BotUsageDaily, mark *model.BotUsageRollupDay) error {
	if err := tx.Where("bot_id = ? AND date = ?", mark.BotID, mark.Date).
		Delete(&model.BotUsageDaily{}).Error; err != nil {
		return err
	}
	if len(rows) > 0 {
		if err := tx.Create(&rows).Error; err != nil {
			return err
		}
	}
	return tx.Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "user_id"}, {Name: "bot_id"}, {Name: "date"}},
		DoUpdates: clause.AssignmentColumns([]string{
			"timezone", "rolled_at", "run_count", "maint_run_count",
			"request_count", "maint_request_count", "last_write_ns",
		}),
	}).Create(mark).Error
}

// WipeBotTx 清掉某 bot 的全部汇总行与覆盖标记（有效时区变更后的整表重算；
// 明细账本不动）。
func (r *BotUsageRollupRepository) WipeBotTx(tx *gorm.DB, botID string) error {
	if err := tx.Where("bot_id = ?", botID).Delete(&model.BotUsageDaily{}).Error; err != nil {
		return err
	}
	return tx.Where("bot_id = ?", botID).Delete(&model.BotUsageRollupDay{}).Error
}

// StampBotNameTx 把某 bot 全部汇总行的名称快照刷成当前名：删除路径在补齐
// 汇总后调用，让「已删除的机器人（原名）」显示删除时的最终名，而不是各日
// 写入时刻的名字混合。
func (r *BotUsageRollupRepository) StampBotNameTx(tx *gorm.DB, botID, name string) error {
	return tx.Model(&model.BotUsageDaily{}).
		Where("bot_id = ?", botID).
		Update("bot_name", name).Error
}

// TombstoneTaskNameTx 在事务内为将删任务写下名字墓碑：汇总行的任务名快照
// 随整日替换/整表清空重建，删除后只有这张独立表还能提供名字（墓碑是删除
// 时刻的名字，比任何旧快照新——重算填充时让它覆盖旧快照）。
func (r *BotUsageRollupRepository) TombstoneTaskNameTx(tx *gorm.DB, taskID, botID, name string) error {
	if name == "" {
		return nil
	}
	return tx.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "task_id"}},
		DoUpdates: clause.AssignmentColumns([]string{"task_name", "updated_at"}),
	}).Create(&model.BotScheduleTaskName{
		TaskID: taskID, BotID: botID, TaskName: name, UpdatedAt: time.Now().UTC(),
	}).Error
}

// TaskNameTombstonesTx 批量取任务名墓碑（task ID → 名字）。只查了无活名
// 的桶——名字墓碑只在任务删除时写下，活着的任务由活名查询覆盖。
func (r *BotUsageRollupRepository) TaskNameTombstonesTx(tx *gorm.DB, taskIDs []string) (map[string]string, error) {
	out := map[string]string{}
	if len(taskIDs) == 0 {
		return out, nil
	}
	var rows []model.BotScheduleTaskName
	if err := tx.Model(&model.BotScheduleTaskName{}).
		Select("task_id, task_name").Where("task_id IN ?", taskIDs).
		Scan(&rows).Error; err != nil {
		return nil, err
	}
	for _, row := range rows {
		out[row.TaskID] = row.TaskName
	}
	return out, nil
}

// PrevTaskNamesTx 取某 bot 某日已落库的任务名快照（task ID → 非空名字）：
// 重滚时任务可能已删（活名查不到），沿用上一版快照避免把名字冲掉。
func (r *BotUsageRollupRepository) PrevTaskNamesTx(tx *gorm.DB, botID, date string) (map[string]string, error) {
	var rows []model.BotUsageDaily
	if err := tx.Model(&model.BotUsageDaily{}).
		Select("schedule_task_id, task_name").
		Where("bot_id = ? AND date = ? AND schedule_task_id IS NOT NULL AND task_name != ''", botID, date).
		Scan(&rows).Error; err != nil {
		return nil, err
	}
	out := make(map[string]string, len(rows))
	for _, row := range rows {
		out[*row.ScheduleTaskID] = row.TaskName
	}
	return out, nil
}

// BotExistsTx 事务内确认 bot 行仍存在（后台汇总与删除竞速的提交前复检）。
func (r *BotUsageRollupRepository) BotExistsTx(tx *gorm.DB, botID string) (bool, error) {
	var n int64
	err := tx.Model(&model.Bot{}).Where("id = ?", botID).Count(&n).Error
	return n > 0, err
}

// ScheduleTaskNamesByIDs 批量取任务名快照；已删除任务取不到行，调用方按空串
// 处理（与总览「已删除任务名空串」契约一致，快照只保留写入时能看到的名字）。
func (r *BotUsageRollupRepository) ScheduleTaskNamesByIDs(taskIDs []string) (map[string]string, error) {
	out := map[string]string{}
	for _, batch := range ChunkIDs(taskIDs, UsageQueryBatch) {
		var rows []struct {
			ID   string `gorm:"column:id"`
			Name string `gorm:"column:name"`
		}
		if err := r.db.Model(&model.BotScheduleTask{}).
			Select("id, name").Where("id IN ?", batch).Scan(&rows).Error; err != nil {
			return nil, err
		}
		for _, row := range rows {
			out[row.ID] = row.Name
		}
	}
	return out, nil
}
