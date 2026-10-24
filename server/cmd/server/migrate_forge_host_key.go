package main

import (
	"go.uber.org/zap"
	"gorm.io/gorm"

	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/godbobo/fast_ship/server/internal/pkg/forges"
)

// migrateForgeHostKeys 给存量 forge_integrations 行回填 host_key（INT-166）。
//
// host_key 是 base_url 的归一键（forges.HostKey：小写 host + scheme 缺省端口），
// 同归一主机的部分唯一索引 idx_forge_integrations_host_key 靠它兜应用层查重
// （ensureHostAvailable）的并发窗口。AutoMigrate 给存量库加出的列是 NULL，这里
// 按 base_url 现算写回；解析不出主机键的脏行写空串（部分唯一索引排除空串，
// 不会因两条脏行撞索引而挡启动）。
//
// 幂等：只更新 host_key 为 NULL 或空串的行，重复启动无副作用。必须在
// createBootIndexes 之前执行（索引建在回填完的数据上）。
func migrateForgeHostKeys(db *gorm.DB, logger *zap.Logger) error {
	var rows []model.ForgeIntegration
	if err := db.Model(&model.ForgeIntegration{}).
		Select("id, base_url").
		Where("host_key IS NULL OR host_key = ''").
		Find(&rows).Error; err != nil {
		return err
	}
	if len(rows) == 0 {
		return nil
	}
	for _, row := range rows {
		if err := db.Model(&model.ForgeIntegration{}).
			Where("id = ?", row.ID).
			Update("host_key", forges.HostKey(row.BaseURL)).Error; err != nil {
			return err
		}
	}
	if logger != nil {
		logger.Info("代码集成 host_key 已回填", zap.Int("rows", len(rows)))
	}
	return nil
}
