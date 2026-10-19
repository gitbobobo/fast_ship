package main

import (
	"fmt"
	"os"

	"github.com/godbobo/fast_ship/server/internal/model"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

// dropUserIntegrationsEnv 是「确认放弃用户级 App 配置」的显式开关。
const dropUserIntegrationsEnv = "FAST_SHIP_DROP_USER_INTEGRATIONS"

// migrateDropIntegrations（INT-129）：GitHub App 身份从「每个用户自己建」改为
// 「每套部署一个」（deployment_settings），用户级 integrations 表退出历史。
//
// 本仓库的部署没有配置过用户级 App，所以正常路径就是整表 DROP。但 upgrade 会
// 不可逆地丢掉别人的加密私钥与 webhook 密钥，不能默认静默执行：表里还有行时
// 直接拒绝启动，要求运维显式设置 dropUserIntegrationsEnv 再重启。这不是数据
// 迁移——一套部署只能有一个 App 身份，多个用户各配一个时该选谁属于产品决策，
// 不在升级里替用户挑；确认后重新配置一次的量级就是「点一下创建 App」。
//
// 幂等：表不存在（或已是空表）时不动，重复启动无副作用。
func migrateDropIntegrations(db *gorm.DB, logger *zap.Logger) error {
	exists, err := sqliteTableExists(db, "integrations")
	if err != nil {
		return err
	}
	if !exists {
		return nil
	}

	var rows int64
	if err := db.Table("integrations").Count(&rows).Error; err != nil {
		return err
	}
	if rows > 0 && os.Getenv(dropUserIntegrationsEnv) != "1" {
		return fmt.Errorf(
			"integrations 表还有 %d 条用户级 GitHub App 配置；INT-129 起 App 身份改为部署级，删除该表会丢掉这些加密凭证。"+
				"请先备份数据库，确认改由「设置 → 集成」重新配置后，设置 %s=1 再启动",
			rows, dropUserIntegrationsEnv)
	}

	if err := db.Exec("DROP TABLE integrations").Error; err != nil {
		return err
	}
	logger.Info("已删除用户级 integrations 表（INT-129 部署级 GitHub App 取代）",
		zap.Int64("dropped_rows", rows),
		zap.String("successor", (&model.DeploymentSettings{}).TableName()))
	return nil
}
