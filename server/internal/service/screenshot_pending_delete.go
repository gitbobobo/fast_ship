package service

import (
	"errors"
	"fmt"
	"os"

	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/godbobo/fast_ship/server/internal/pkg/storage"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

// DrainScreenshotPendingDeletes 出队待删截图文件：删除成功或文件已不存在即
// 出队；其他删除失败打错误日志并把行留在队列里等下次启动重试。只返回数据
// 库层的错误；单个文件删除失败不算错误（幂等重试），按返回的成功数统计。
func DrainScreenshotPendingDeletes(db *gorm.DB, fileStorage storage.Storage, logger *zap.Logger) (int, error) {
	var pending []model.ScreenshotPendingDelete
	if err := db.Find(&pending).Error; err != nil {
		return 0, fmt.Errorf("读取待删截图文件队列失败: %w", err)
	}
	removed := 0
	for _, row := range pending {
		if err := fileStorage.Delete(row.Path); err != nil && !isAbsentScreenshotFile(err) {
			logger.Error("删除旧截图文件失败，留下队列行下次启动重试",
				zap.String("path", row.Path), zap.Error(err))
			continue
		}
		if err := db.Where("path = ?", row.Path).Delete(&model.ScreenshotPendingDelete{}).Error; err != nil {
			return removed, fmt.Errorf("移除待删队列行 %s 失败: %w", row.Path, err)
		}
		removed++
	}
	return removed, nil
}

// isAbsentScreenshotFile 把「文件已经不在」当成清理成功：进程可能在 Delete
// 成功后、出队提交前退出，下次启动再删会得到 ENOENT，不应永久报错占着队列。
func isAbsentScreenshotFile(err error) bool {
	return errors.Is(err, os.ErrNotExist)
}
