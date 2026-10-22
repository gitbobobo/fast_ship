package service

import (
	"strings"
	"time"

	"github.com/godbobo/fast_ship/server/internal/model"
)

// 用户时区解析（INT-138）：用户设置优先，未声明或非法时回退部署配置
// bots.memory.timezone，再回退 UTC。机器人侧所有「当地」语义——运行时上下文、
// 定时任务默认时区、夜间整理与 daily 文件日期——都从这里取时区，保证同一台
// 部署上各处口径一致。

// botEffectiveTimezone 把任意时区原文解析成 (有效时区名, Location)：空串与
// 非法名称都按回退链处理，不返回错误——时区只影响展示与默认值，不值得让一次
// 运行因为一个坏值时区失败。
func (s *BotService) botEffectiveTimezone(raw string) (string, *time.Location) {
	if name := strings.TrimSpace(raw); name != "" {
		if loc, err := time.LoadLocation(name); err == nil {
			return name, loc
		}
	}
	if fallback := strings.TrimSpace(s.cfg.Bots.Memory.Timezone); fallback != "" {
		if loc, err := time.LoadLocation(fallback); err == nil {
			return fallback, loc
		}
	}
	return "UTC", time.UTC
}

// botUserTimezoneOf 返回用户的有效时区；用户行读不到时按未声明处理。
func (s *BotService) botUserTimezoneOf(userID string) (string, *time.Location) {
	if userID == "" {
		return s.botEffectiveTimezone("")
	}
	user, err := s.users.FindByID(userID)
	if err != nil || user == nil {
		return s.botEffectiveTimezone("")
	}
	return s.botEffectiveTimezone(user.Timezone)
}

// botOwnerTimezoneOf 返回机器人归属用户的有效时区。
func (s *BotService) botOwnerTimezoneOf(bot *model.Bot) (string, *time.Location) {
	if bot == nil {
		return s.botEffectiveTimezone("")
	}
	return s.botUserTimezoneOf(bot.UserID)
}

// botLocalStamp 是运行时上下文里的本地时间投影：带偏移的 RFC3339，让模型
// 不必自己做时区换算就能说清「现在当地几点」。
func botLocalStamp(now time.Time, loc *time.Location) string {
	return now.In(loc).Format(time.RFC3339)
}
