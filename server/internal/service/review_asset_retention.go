package service

import (
	"time"

	"github.com/godbobo/fast_ship/server/internal/model"
	"go.uber.org/zap"
)

// review 截图资产行账保留策略（INT-184）：review-screenshot-cache 面的
// 持久占用此前只增不减，本文件按「以 review_assets 行为账」回收：
//
//   - 行退役：created_at 超过保留期的行删除。关闭 PR 的行、旧 head 的行、
//     探测过但没有视觉运行的行（探测落账见 review_assets.go）按同一条年龄
//     规则退出，不为触发条件引入 PR 关闭态这样的第二状态源。
//   - 字节回收：存活行的 storage_path 集合交给 githubmedia 的
//     SweepUnreferencedEntries（面内唯一的 sanctioned 删除路径），引用计数
//     + mtime 宽限判定；崩溃窗口（文件已 rename、行未插入）与「行插入前的
//     命中」由宽限覆盖（行插入路径的 mtime 必新：新下载 rename / 命中
//     touch，见 proxy.go touchReviewEntry）。
//
// 每步幂等：任意时点被杀，下一轮从同样的判定重新收敛。删除与行插入之间
// 的残余竞态由 ensureAssets 的文件复核自愈（缺失按失败行重下）。

// reviewAssetRetention 资产行保留期：审查截图作为持久视觉证据保留 30 天，
// 超期行退役、其字节在下一轮扫描回收。
const reviewAssetRetention = 30 * 24 * time.Hour

// reviewAssetSweepGrace 无引用字节的 mtime 宽限：覆盖「文件已落盘、行还没
// 插入」的崩溃窗口与引用快照到删除之间的行插入；宽限必须显著大于探测与
// 视觉运行之间的排队间隔（每用户在途上限可能压行数小时），24h 取保守值。
const reviewAssetSweepGrace = 24 * time.Hour

// reviewAssetSweepEvery 保留策略扫描周期：与 githubmedia 过期扫描同节奏，
// 孤儿字节最坏一个周期 + 一轮退役延迟内回收。
const reviewAssetSweepEvery = 6 * time.Hour

// reviewAssetSweeper 下载面的行账回收能力（可选）：生产注入的
// *githubmedia.ProxyService 实现；测试桩可不实现，扫描自动跳过文件回收。
type reviewAssetSweeper interface {
	SweepUnreferencedEntries(isReferenced func(binPath string) bool, grace time.Duration) int
}

// assetRetentionLoop 保留策略的低频循环：不与 10s 调度 tick 抢执行权，与
// redeliveryLoop 同款独立 goroutine，共用 cancel。启动即先扫一轮（进程刚
// 起来时最需要收敛停机期间遗留的孤儿字节）。
func (s *ReviewService) assetRetentionLoop(cancel <-chan struct{}) {
	ticker := time.NewTicker(reviewAssetSweepEvery)
	defer ticker.Stop()
	s.sweepAssetRetention(time.Now().UTC())
	for {
		select {
		case <-cancel:
			return
		case now := <-ticker.C:
			s.sweepAssetRetention(now.UTC())
		}
	}
}

// sweepAssetRetention 一轮保留策略扫描：先退役超龄行，再按存活行的
// storage_path 集合回收无引用字节。行退役失败或引用集合读失败时直接返回，
// 不动文件——账不确定时宁可保守（下轮重试）。
func (s *ReviewService) sweepAssetRetention(now time.Time) {
	res := s.db.Where("created_at < ?", now.Add(-reviewAssetRetention)).
		Delete(&model.ReviewAsset{})
	if res.Error != nil {
		s.logger.Warn("截图资产行退役失败", zap.Error(res.Error))
		return
	}
	retired := res.RowsAffected

	// 下载面未配置时没有文件可回收，行退役照常推进（失败行与成功行的账都
	// 在这张表）。
	if s.media == nil {
		if retired > 0 {
			s.logger.Info("截图资产行退役", zap.Int64("retired_rows", retired))
		}
		return
	}
	sweeper, ok := s.media.(reviewAssetSweeper)
	if !ok {
		return
	}
	var paths []string
	if err := s.db.Model(&model.ReviewAsset{}).Where("storage_path <> ''").
		Distinct().Pluck("storage_path", &paths).Error; err != nil {
		s.logger.Warn("截图资产引用集合读取失败", zap.Error(err))
		return
	}
	referenced := make(map[string]struct{}, len(paths))
	for _, p := range paths {
		referenced[p] = struct{}{}
	}
	removed := sweeper.SweepUnreferencedEntries(func(binPath string) bool {
		_, ok := referenced[binPath]
		return ok
	}, reviewAssetSweepGrace)
	if retired > 0 || removed > 0 {
		s.logger.Info("截图资产保留策略扫描",
			zap.Int64("retired_rows", retired), zap.Int("removed_files", removed))
	}
}
