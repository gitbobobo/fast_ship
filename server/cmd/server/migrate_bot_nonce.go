package main

import (
	"crypto/rand"
	"encoding/hex"

	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/google/uuid"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

// migrateBotUntrustedNonce 给存量机器人补齐围栏 nonce（INT-136）：只为空值
// 行生成一次，已有的 nonce 永不改写（前缀与围栏标记必须稳定）。必须在
// AutoMigrate 之后（依赖新列）。
func migrateBotUntrustedNonce(db *gorm.DB, logger *zap.Logger) error {
	var ids []string
	if err := db.Model(&model.Bot{}).
		Where("untrusted_nonce = ''").
		Pluck("id", &ids).Error; err != nil {
		return err
	}
	for _, id := range ids {
		// CAS 只写空值行：并发启动的两个进程也只有先生成的值生效，
		// 后写者命中 0 行不改写。
		if err := db.Model(&model.Bot{}).
			Where("id = ? AND untrusted_nonce = ''", id).
			Update("untrusted_nonce", botMigrationNonce()).Error; err != nil {
			return err
		}
	}
	if len(ids) > 0 {
		logger.Info("已为存量机器人补齐围栏 nonce", zap.Int("count", len(ids)))
	}
	return nil
}

// botMigrationNonce 与 service.botNewNonce 同规格（16 字节随机数 hex）；
// 未导出函数跨包不可见，迁移自实现一份，失败回退 uuid。
func botMigrationNonce() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return uuid.NewString()
	}
	return hex.EncodeToString(b[:])
}
