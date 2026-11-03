package service

import (
	"fmt"
	"strings"
	"time"

	"github.com/godbobo/fast_ship/server/internal/model"
	"go.uber.org/zap"
)

// review 截图资产行账保留策略（INT-184）：review-screenshot-cache 面的
// 持久占用此前只增不减，本文件按「以 review_assets 行为账」回收：
//
//   - 行退役：created_at 超过保留期的行删除，但 head 上仍有非终态运行的
//     （queued/dispatched/running）除外——在途运行不能丢自己的材料。关闭
//     PR 的行、旧 head 的行、探测过但没有视觉运行的行（探测落账见
//     review_assets.go）按同一条年龄规则退出，不为触发条件引入 PR 关闭态
//     这样的第二状态源。
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
// 插入」的崩溃窗口与引用快照到删除之间的行插入（行插入路径的 mtime 必新：
// 新下载 rename / 命中 touch，见 proxy.go touchReviewEntry；探测落账即时同
// 步执行后，R12 时代的「探测与视觉运行间隔数小时」场景已不存在，宽限不再
// 需要为它留量）。24h 取保守值，显著大于任何插入路径从落盘到落账的耗时。
const reviewAssetSweepGrace = 24 * time.Hour

// reviewAssetSweepEvery 保留策略扫描周期：与 githubmedia 过期扫描同节奏，
// 孤儿字节最坏一个周期 + 一轮退役延迟内回收。
const reviewAssetSweepEvery = 6 * time.Hour

// reviewAssetRetireBatch 单批退役行数：每批一条独立 DELETE，批间释放
// SQLite 写锁，让调度/webhook 的写请求插得进来。与
// idx_review_assets_created_at / idx_review_runs_asset_head 配合，单批耗
// 时有界。var 是为了让测试注入小值验证多批循环的终止条件。
var reviewAssetRetireBatch = 2000

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

// sweepAssetRetention 一轮保留策略扫描：先快照存活行的 storage_path 集合，
// 再退役超龄行，再按**退役前**的快照回收无引用字节——本轮被退役行释放的
// 字节下一轮再收（宽限从退役时刻起算，R5 审查意见）：退役守卫只在 DELETE
// 一刻判定，若快照在删除之后，删除与快照之间新建的运行会在同轮失去文件。
// 行退役失败或引用集合读失败时直接返回，不动文件——账不确定时宁可保守
// （下轮重试）。
func (s *ReviewService) sweepAssetRetention(now time.Time) {
	// 引用集合先于退役快照（含即将退役的行）：本轮释放的字节留给下一轮，
	// 关闭 DELETE 与快照之间的新建运行丢失窗口；代价是退役释放的字节晚一
	// 个扫描周期回收，探测/崩溃残留的真孤儿不受影响（任何快照里都没有它
	// 们，同轮即收）。
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

	// 行退役：跳过仍有非终态运行在身的 head。重开/手动重跑/评论唤醒可能重新
	// 审查一个超龄 head，而命中行不刷新 created_at（AgentScreenshotImage 直
	// 接读 storage_path，不经缓存面）——无守卫的话，在途运行会在两次扫描之
	// 间失去行与文件（取图 404、截图列表变空），ensureAssets 的复核发生在
	// 删除之前救不回来。运行进终态后，下一轮恢复按年龄退役。
	// 分批执行：每批一条独立 DELETE，先走 idx_review_assets_created_at 定位
	// 超龄行、守卫走 idx_review_runs_asset_head，批间释放 SQLite 唯一写锁——
	// 长时间停机后待退役行最多，单条大 DELETE 会把调度/webhook 写排队到
	// busy_timeout 超时（INT-184 R2 审查实测）。批失败即停：账不确定时宁可
	// 保守（已删批次的字节下轮扫描收敛），不继续加载引用集合。
	retired := int64(0)
	// 非终态集合从 model 常量拼接：新增非终态状态时不会在 SQL 字面量里静
	// 默漂移（方向审计建议）。常量无注入面。
	activeStatusLiterals := []string{
		"'" + string(model.ReviewRunQueued) + "'",
		"'" + string(model.ReviewRunDispatched) + "'",
		"'" + string(model.ReviewRunRunning) + "'",
	}
	retireSQL := fmt.Sprintf(`DELETE FROM review_assets WHERE id IN (
		SELECT a.id FROM review_assets a
		WHERE a.created_at < ?
			AND NOT EXISTS (
				SELECT 1 FROM review_runs r
				WHERE r.integration_id = a.integration_id
					AND r.repo_full_name = a.repo_full_name
					AND r.pr_number = a.pr_number
					AND r.head_sha = a.head_sha
					AND r.status IN (%s)
			)
		LIMIT ?
	)`, strings.Join(activeStatusLiterals, ", "))
	for {
		res := s.db.Exec(retireSQL, now.Add(-reviewAssetRetention), reviewAssetRetireBatch)
		if res.Error != nil {
			s.logger.Warn("截图资产行退役失败", zap.Error(res.Error))
			return
		}
		retired += res.RowsAffected
		if res.RowsAffected < int64(reviewAssetRetireBatch) {
			break
		}
	}

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
	removed := sweeper.SweepUnreferencedEntries(func(binPath string) bool {
		_, ok := referenced[binPath]
		return ok
	}, reviewAssetSweepGrace)
	if retired > 0 || removed > 0 {
		s.logger.Info("截图资产保留策略扫描",
			zap.Int64("retired_rows", retired), zap.Int("removed_files", removed))
	}
}
