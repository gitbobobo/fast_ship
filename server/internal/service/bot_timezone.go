package service

import (
	"errors"
	"strings"
	"time"

	"github.com/godbobo/fast_ship/server/internal/model"
	"gorm.io/gorm"
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
// 只用于「读失败也不该影响本条路径」的场景（运行时上下文、夜间整理、
// daily 路径）——把回退当成唯一结果。需要区分「用户没声明」与「查询失败」
// 的写入路径（创建定时任务）必须用 botUserTimezoneOfErr。
func (s *BotService) botUserTimezoneOf(userID string) (string, *time.Location) {
	resolved, err := s.botUserTimezoneOfErr(userID)
	if err != nil {
		return s.botEffectiveTimezone("")
	}
	return resolved.name, resolved.loc
}

// botTimezoneResolution 是一次时区解析的结果。
type botTimezoneResolution struct {
	name string
	loc  *time.Location
}

// botUserTimezoneOfErr 同 botUserTimezoneOf，但把读库失败与「用户没声明时区」
// 区分开：用户行不存在（机器人随用户级联删除，属可预期状态）按未声明处理，
// 其余错误原样返回。把瞬时读库失败当成未声明，会把回退时区当成用户的选择
// 永久写进任务（INT-138 复审）。
func (s *BotService) botUserTimezoneOfErr(userID string) (botTimezoneResolution, error) {
	if userID == "" {
		name, loc := s.botEffectiveTimezone("")
		return botTimezoneResolution{name: name, loc: loc}, nil
	}
	user, err := s.users.FindByID(userID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			name, loc := s.botEffectiveTimezone("")
			return botTimezoneResolution{name: name, loc: loc}, nil
		}
		return botTimezoneResolution{}, err
	}
	name, loc := s.botEffectiveTimezone(user.Timezone)
	return botTimezoneResolution{name: name, loc: loc}, nil
}

// botOwnerTimezoneOf 返回机器人归属用户的有效时区。
func (s *BotService) botOwnerTimezoneOf(bot *model.Bot) (string, *time.Location) {
	resolved, err := s.botOwnerTimezoneOfErr(bot)
	if err != nil {
		return s.botEffectiveTimezone("")
	}
	return resolved.name, resolved.loc
}

// botOwnerTimezoneOfErr 同 botOwnerTimezoneOf，但把读库失败原样返回；写入
// 路径（创建定时任务）用它，不要用静默回退。
func (s *BotService) botOwnerTimezoneOfErr(bot *model.Bot) (botTimezoneResolution, error) {
	if bot == nil {
		name, loc := s.botEffectiveTimezone("")
		return botTimezoneResolution{name: name, loc: loc}, nil
	}
	return s.botUserTimezoneOfErr(bot.UserID)
}

// botOwnerTimezoneOfID 同 botOwnerTimezoneOf，但按 bot ID 查：工具执行路径
// 手里只有 bot ID，没有 bot 行。读不到时按未声明处理。
func (s *BotService) botOwnerTimezoneOfID(botID string) (string, *time.Location) {
	bot, err := s.bots.Get(botID)
	if err != nil {
		return s.botEffectiveTimezone("")
	}
	return s.botOwnerTimezoneOf(bot)
}

// botLocalStamp 是运行时上下文里的本地时间投影：带偏移的 RFC3339，让模型
// 不必自己做时区换算就能说清「现在当地几点」。
func botLocalStamp(now time.Time, loc *time.Location) string {
	return now.In(loc).Format(time.RFC3339)
}
