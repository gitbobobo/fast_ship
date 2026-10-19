package main

import (
	"github.com/godbobo/fast_ship/server/internal/model"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

// migrateUserRoles（INT-129 升级）：users.role 列由 AutoMigrate 补齐（存量行
// 全部落默认值 member），存量部署可能因此一个 admin 都没有——把 created_at
// 最早的用户置为 admin，保证升级后仍有人能管理成员、邀请与部署设置。
// 幂等：已有 admin（或库为空）时不动；重复启动无副作用。任何一步失败返回
// 错误 → 启动失败，宁可起不来也不带错跑。整段只用传入句柄顺序执行
// （单连接池，不在嵌套事务里借池根）。
func migrateUserRoles(db *gorm.DB, logger *zap.Logger) error {
	return db.Transaction(func(tx *gorm.DB) error {
		var admins int64
		if err := tx.Model(&model.User{}).Where("role = ?", model.UserRoleAdmin).Count(&admins).Error; err != nil {
			return err
		}
		if admins > 0 {
			return nil
		}

		// 没有 admin：把最早创建的用户提为 admin。role 列可能存在历史 NULL
		// （极旧中间形态），扫全表排序即可，不用按 role 过滤。
		var user model.User
		if err := tx.Order("created_at ASC, id ASC").First(&user).Error; err != nil {
			if err == gorm.ErrRecordNotFound {
				return nil // 全新库：首个注册用户由注册路径直接授 admin
			}
			return err
		}
		if err := tx.Model(&model.User{}).Where("id = ?", user.ID).
			Update("role", model.UserRoleAdmin).Error; err != nil {
			return err
		}

		logger.Info("存量库无管理员，已把最早创建的用户置为 admin",
			zap.String("user_id", user.ID), zap.String("username", user.Username))
		return nil
	})
}
