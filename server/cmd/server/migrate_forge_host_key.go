package main

import (
	"go.uber.org/zap"
	"gorm.io/gorm"

	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/godbobo/fast_ship/server/internal/pkg/forges"
)

// migrateForgeHostKeys 给存量 forge_integrations 行回填/重算 host_key（INT-166，
// INT-181 扩为重算）。
//
// host_key 是 base_url 的归一键（forges.HostKey：小写 host + scheme 缺省端口 +
// 数字端口统一拼写），同归一主机的部分唯一索引 idx_forge_integrations_host_key
// 靠它兜应用层查重（ensureHostAvailable）的并发窗口。AutoMigrate 给存量库加出
// 的列是 NULL，这里按 base_url 现算写回；解析不出主机键的脏行写空串（部分唯
// 一索引排除空串，不会因两条脏行撞索引而挡启动）。HostKey 口径升级（如端口
// 归一）后，存量行与现算键不一致的也一并重算，避免「项目能绑定、PR 挂不上」
// 的查空。
//
// 幂等：只更新 host_key 为 NULL/空串或与现算键不一致的行；目标键已被别的行
// 占用时跳过并记日志（唯一索引兜底），重复启动无副作用。必须在
// createBootIndexes 之前执行（索引建在回填完的数据上）。
func migrateForgeHostKeys(db *gorm.DB, logger *zap.Logger) error {
	var rows []model.ForgeIntegration
	if err := db.Model(&model.ForgeIntegration{}).
		Select("id, base_url, host_key").
		Find(&rows).Error; err != nil {
		return err
	}
	backfilled := 0
	for _, row := range rows {
		want := forges.HostKey(row.BaseURL)
		if want == row.HostKey {
			continue
		}
		// 目标键被其他行占用：跳过让唯一索引与人工介入兜底，不硬写。
		var taken int64
		if err := db.Model(&model.ForgeIntegration{}).
			Where("host_key = ? AND id <> ?", want, row.ID).
			Count(&taken).Error; err != nil {
			return err
		}
		if taken > 0 {
			if logger != nil {
				// 跳过时给出可操作路径：被跳过行的 base_url 与目标键都在日志
				// 里，运维删除或改绑重复集成为同一端点后重启即收敛。
				logger.Warn("代码集成 host_key 重算冲突，跳过该行（请删除或改绑重复集成为同一端点后重启）",
					zap.String("id", row.ID),
					zap.String("base_url", row.BaseURL),
					zap.String("host_key", row.HostKey), zap.String("want", want))
			}
			continue
		}
		if err := db.Model(&model.ForgeIntegration{}).
			Where("id = ?", row.ID).
			Update("host_key", want).Error; err != nil {
			return err
		}
		backfilled++
	}
	if logger != nil && backfilled > 0 {
		logger.Info("代码集成 host_key 已回填/重算", zap.Int("rows", backfilled))
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

// migrateProjectGithubHosts 给存量 projects 行回填 github_host（INT-166）。
//
// github_host 列上线前写入的项目没有主机标识：那时的 code_repo_url 投影恒按
// github.com 渲染（无 GHE/Forgejo 概念），所以存量行的真实意图全是「公共
// github.com」。新语义下空串 = 本部署 GitHub——GHE 部署上存量行会被渲染成
// {ghe}/o/r（大概率不存在），AutoMigrate 给存量行留的 NULL 正好区分「旧行」
// 与新写入的空串，一次性把 NULL 收成 'github.com' 保住旧渲染。
//
// 幂等：只动 github_host IS NULL 的行，重复启动无副作用。
func migrateProjectGithubHosts(db *gorm.DB, logger *zap.Logger) error {
	res := db.Exec(`UPDATE projects SET github_host = 'github.com'
		WHERE github_host IS NULL`)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected > 0 && logger != nil {
		logger.Info("存量项目 github_host 已回填",
			zap.Int64("rows", res.RowsAffected))
	}
	return nil
}
