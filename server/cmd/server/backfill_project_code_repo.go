package main

import (
	"go.uber.org/zap"
	"gorm.io/gorm"
)

// backfillProjectCodeRepo 把 projects 新增的 code_github_owner/code_github_repo
// 两列在存量行上的 NULL 回填为空串（INT-140）。
//
// AutoMigrate 给已有表加列时，SQLite 的存量行在这两列上是 NULL，而模型字段是
// Go string：读侧虽然能扫码（实测 glebarez/sqlite 把 NULL 折成零值），但 SQL
// 里 `code_github_owner <> ”` 对 NULL 求值为 UNKNOWN，一旦将来把这两个列放进
// 判定条件就会静默漏行。「空 = 与公开仓库相同」要求这两列要么是 ”、要么是
// 有效 owner/repo，所以在启动时补齐，保持不变量显式且与驱动实现无关。
//
// 幂等：只更新 IS NULL 的行，重复启动无副作用。
func backfillProjectCodeRepo(db *gorm.DB, logger *zap.Logger) error {
	res := db.Exec(`UPDATE projects
		SET code_github_owner = COALESCE(code_github_owner, ''),
		    code_github_repo = COALESCE(code_github_repo, '')
		WHERE code_github_owner IS NULL OR code_github_repo IS NULL`)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected > 0 && logger != nil {
		logger.Info("项目代码仓库列已回填空串", zap.Int64("rows", res.RowsAffected))
	}
	return nil
}
