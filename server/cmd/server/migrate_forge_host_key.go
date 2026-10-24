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

// migrateForgeAPIDerived 把存量 api_base_url 的展开值收成意图存储（INT-166
// 审查修复）：列语义改为「空串 = 按 base_url 推导，非空 = 显式钉死」。早期
// 写入把推导结果（{base}/api/v1）原样落库，分不清推导与显式——一次性把与
// 推导值相等的存量展开值清回空串。本就是显式配置却恰好等于推导值的行会被
// 一并收成推导（原始意图已不可恢复，取多数情形），其生效 URL 不变。
//
// 幂等：只动仍等于推导值的非空列，重复启动无副作用。
func migrateForgeAPIDerived(db *gorm.DB, logger *zap.Logger) error {
	res := db.Exec(`UPDATE forge_integrations SET api_base_url = ''
		WHERE api_base_url <> '' AND api_base_url = RTRIM(base_url, '/') || '/api/v1'`)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected > 0 && logger != nil {
		logger.Info("代码集成 api_base_url 展开值已收成推导存储",
			zap.Int64("rows", res.RowsAffected))
	}
	return nil
}
