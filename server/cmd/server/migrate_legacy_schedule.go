package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/godbobo/fast_ship/server/internal/repository"
	"github.com/google/uuid"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

// legacyScheduleReviewDedup 是存量任务复核提示的幂等键：一个机器人至多收到
// 一条（重启重跑迁移不会重复提示）。
const legacyScheduleReviewDedup = "legacy_schedule_review:int136"

// migrateLegacyScheduleNotice 给有「未经确认卡复核」任务的机器人发一次性主线
// 提示（INT-136 的发布策略：只提示不阻断）。确认机制上线前创建的任务，其
// instruction 从未经用户确认，部署后仍按原 cron 触发、运行内默认放行写操作；
// 提示列出仍在武装的任务请用户复核/停用，不改任何任务的运行行为。
// 必须在 AutoMigrate 之后（依赖 bot_schedule_tasks.confirmed 列）。
func migrateLegacyScheduleNotice(db *gorm.DB, logger *zap.Logger) error {
	// join bots 只为仍在的机器人发提示：被删机器人若残留任务行，消息外键
	// 会让插入失败，拖垮整个启动迁移。
	var botIDs []string
	if err := db.Model(&model.BotScheduleTask{}).
		Distinct("bot_schedule_tasks.bot_id").
		Joins("JOIN bots ON bots.id = bot_schedule_tasks.bot_id").
		Where("bot_schedule_tasks.confirmed = ?", false).
		Pluck("bot_schedule_tasks.bot_id", &botIDs).Error; err != nil {
		return err
	}
	sort.Strings(botIDs)
	byBot := map[string][]model.BotScheduleTask{}
	for _, botID := range botIDs {
		var tasks []model.BotScheduleTask
		if err := db.Where("bot_id = ? AND confirmed = ?", botID, false).
			Order("created_at, id").Find(&tasks).Error; err != nil {
			return err
		}
		byBot[botID] = tasks
	}
	order := botIDs
	notified := 0
	for _, botID := range order {
		tasks := byBot[botID]
		err := db.Transaction(func(tx *gorm.DB) error {
			dedup := legacyScheduleReviewDedup
			if existing, err := repository.NewBotMessageRepository(tx).FindByDedupKey(botID, dedup); err == nil && existing != nil {
				return nil
			}
			seq, err := repository.NewBotRepository(tx).NextMessageSeqTx(tx, botID)
			if err != nil {
				// 取号失败的行（如消息水位异常为 0）跳过提示即可：这是
				// 告知性迁移，不该为一个机器人拖垮整个启动。
				if errors.Is(err, gorm.ErrRecordNotFound) {
					return nil
				}
				return err
			}
			content, err := json.Marshal(map[string]interface{}{
				"event":  "legacy_schedule_review",
				"detail": legacyScheduleReviewDetail(tasks),
			})
			if err != nil {
				return err
			}
			return repository.NewBotMessageRepository(tx).AppendTx(tx, &model.BotMessage{
				ID: uuid.NewString(), BotID: botID, Seq: seq,
				Kind: model.BotMessageKindLifecycle, ContentJSON: string(content),
				DedupKey: &dedup, CreatedAt: time.Now().UTC(),
			})
		})
		if err != nil {
			return err
		}
		notified++
	}
	if notified > 0 {
		logger.Info("已向机器人主线发送存量定时任务复核提示", zap.Int("bots", notified))
	}
	return nil
}

// legacyScheduleReviewDetail 组提示的 detail：note 是用户可读正文（前端对
// lifecycle 消息直接渲染 detail.note），tasks 给模型在答复里引用任务用。
func legacyScheduleReviewDetail(tasks []model.BotScheduleTask) map[string]interface{} {
	enabled := make([]model.BotScheduleTask, 0, len(tasks))
	disabled := 0
	for _, t := range tasks {
		if t.Enabled {
			enabled = append(enabled, t)
		} else {
			disabled++
		}
	}
	listed := enabled
	if len(listed) > 8 {
		listed = listed[:8]
	}
	names := make([]string, 0, len(listed))
	for _, t := range listed {
		names = append(names, "「"+t.Name+"」")
	}
	var note string
	switch {
	case len(enabled) == 0:
		note = fmt.Sprintf("安全提示：本机器人的 %d 个已停用定时任务创建于「写操作确认」上线之前，其指令未经确认卡复核；重新启用时会先弹确认卡核对指令，无需立即处理。", disabled)
	case disabled == 0:
		note = fmt.Sprintf("安全提示：本机器人的 %d 个定时任务创建于「写操作确认」上线之前，其指令从未经确认卡复核，目前仍按计划触发：%s。请核对任务指令，不认识的任务请停用或删除。", len(enabled), joinTaskNames(names, len(enabled)))
	default:
		note = fmt.Sprintf("安全提示：本机器人的 %d 个定时任务创建于「写操作确认」上线之前，其指令从未经确认卡复核，目前仍按计划触发：%s。请核对任务指令，不认识的任务请停用或删除；另有 %d 个已停用任务未经复核，重新启用时会先弹确认卡。", len(enabled), joinTaskNames(names, len(enabled)), disabled)
	}
	items := make([]map[string]interface{}, 0, len(tasks))
	for _, t := range tasks {
		items = append(items, map[string]interface{}{"id": t.ID, "name": t.Name, "enabled": t.Enabled})
	}
	return map[string]interface{}{
		"note":           note,
		"tasks":          items,
		"enabled_count":  len(enabled),
		"disabled_count": disabled,
	}
}

// joinTaskNames 拼任务名清单；超出展示上限时以「等 N 个」收尾。
func joinTaskNames(names []string, total int) string {
	out := strings.Join(names, "、")
	if total > len(names) {
		out += fmt.Sprintf(" 等 %d 个", total)
	}
	return out
}
