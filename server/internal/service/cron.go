package service

import (
	"strings"
	"time"

	"github.com/godbobo/fast_ship/server/internal/pkg/errs"
	"github.com/robfig/cron/v3"
)

// cronScheduleParser 只接受标准五字段 cron（分 时 日 月 周）。
// 禁止秒字段（不含 Second 位）、@every 等描述符（不含 Descriptor 位）与
// 表达式内时区前缀（CRON_TZ/TZ 解析器不识别，落到解析错误）。
// 机器人定时任务（INT-103）与原通用自动化共用同一解析纪律；INT-135 起归
// 机器人调度独占使用。
var cronScheduleParser = cron.NewParser(
	cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow,
)

// parseScheduleCron 解析五字段 cron + IANA 时区，返回时区与 now 之后的下一次
// 触发时刻（UTC）。时区走独立字段：robfig 解析器会无条件剥离表达式内的
// TZ=/CRON_TZ= 前缀，规范不允许内嵌，这里显式拒绝。
func parseScheduleCron(expression, timezone string, now time.Time) (*time.Location, time.Time, error) {
	trimmed := strings.TrimSpace(expression)
	if strings.HasPrefix(trimmed, "TZ=") || strings.HasPrefix(trimmed, "CRON_TZ=") {
		return nil, time.Time{}, errs.New(errs.ErrInvalidParams.Code, "cron 表达式不允许内嵌时区前缀，请使用独立的时区字段")
	}
	schedule, err := cronScheduleParser.Parse(trimmed)
	if err != nil {
		return nil, time.Time{}, errs.New(errs.ErrInvalidParams.Code, "cron 表达式无效（须为标准五字段 分 时 日 月 周）: "+err.Error())
	}
	loc := time.UTC
	if timezone != "" {
		loaded, err := time.LoadLocation(timezone)
		if err != nil {
			return nil, time.Time{}, errs.New(errs.ErrInvalidParams.Code, "时区无效（须为 IANA 名称，如 Asia/Shanghai）")
		}
		loc = loaded
	}
	next := schedule.Next(now.In(loc)).UTC()
	return loc, next, nil
}
