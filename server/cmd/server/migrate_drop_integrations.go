package main

import (
	"github.com/godbobo/fast_ship/server/internal/model"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

// migrateDropIntegrations（INT-129 B 轮）：GitHub App 身份从「每个用户自己建」
// 改为「每套部署一个」（deployment_settings），用户级 integrations 表退出历史。
// 已确认目前没有任何用户配置过 App，无需迁移数据，整表 DROP（表上的索引随之
// 消失）。幂等：表不存在时不动。重复启动无副作用。
func migrateDropIntegrations(db *gorm.DB, logger *zap.Logger) error {
	exists, err := sqliteTableExists(db, "integrations")
	if err != nil {
		return err
	}
	if !exists {
		return nil
	}
	if err := db.Exec("DROP TABLE integrations").Error; err != nil {
		return err
	}
	logger.Info("已删除用户级 integrations 表（INT-129 部署级 GitHub App 取代）",
		zap.String("successor", (&model.DeploymentSettings{}).TableName()))
	return nil
}
