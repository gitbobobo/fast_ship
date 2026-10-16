package service

import (
	"context"
	"sync"
	"time"

	"github.com/godbobo/fast_ship/server/internal/config"
	"github.com/godbobo/fast_ship/server/internal/repository"
	"go.uber.org/zap"
)

// QuotaScheduler 是额度抓取的后台循环（INT-109）：每轮抓 next_retry_at 到期
// （或为空）的账号，并发受 max_concurrency 限制，单账号失败不影响其它账号。
// 下一次唤醒取「now + interval」与最早的未来 next_retry_at 的较小值——
// 失败退避到期与窗口重置预抓都能按自己的时刻唤醒，不用等满一轮。
type QuotaScheduler struct {
	svc    *QuotaService
	repo   *repository.QuotaAccountRepository
	cfg    *config.Config
	logger *zap.Logger
	now    func() time.Time

	stopOnce sync.Once
	stopCh   chan struct{}
	doneCh   chan struct{}
}

func NewQuotaScheduler(svc *QuotaService, repo *repository.QuotaAccountRepository, cfg *config.Config, logger *zap.Logger) *QuotaScheduler {
	return &QuotaScheduler{
		svc:    svc,
		repo:   repo,
		cfg:    cfg,
		logger: logger,
		now:    time.Now,
		stopCh: make(chan struct{}),
		doneCh: make(chan struct{}),
	}
}

// Start 启动调度循环；run_on_startup 为真时先同步跑一轮（在后台 goroutine 里）。
func (s *QuotaScheduler) Start() {
	go s.loop()
}

// Stop 停止调度并等循环退出；进行中的单次抓取不打断（进程退出时由调用方兜底）。
func (s *QuotaScheduler) Stop() {
	s.stopOnce.Do(func() { close(s.stopCh) })
	<-s.doneCh
}

func (s *QuotaScheduler) loop() {
	defer close(s.doneCh)
	if s.cfg.Quota.RunOnStartup {
		s.runRound(s.now().UTC())
	}
	for {
		timer := time.NewTimer(s.nextWait(s.now().UTC()))
		select {
		case <-s.stopCh:
			timer.Stop()
			return
		case t := <-timer.C:
			s.runRound(t.UTC())
		}
	}
}

// nextWait 算距下一轮的实际等待：常规 interval 与最早的未来 next_retry_at 取小；
// 保底正数避免忙循环（已有到期账号时下一轮立刻跑）。
func (s *QuotaScheduler) nextWait(now time.Time) time.Duration {
	wait := s.cfg.Quota.Interval()
	if earliest, err := s.repo.EarliestNextRetryAt(now); err == nil && earliest != nil {
		if d := earliest.Sub(now); d < wait {
			wait = d
		}
	} else if err != nil {
		s.logger.Warn("查询最早 next_retry_at 失败，按常规间隔等待", zap.Error(err))
	}
	if wait <= 0 {
		wait = time.Second
	}
	return wait
}

// runRound 抓一轮到期账号：并发上限内各账号独立抓取，单账号失败互不影响。
func (s *QuotaScheduler) runRound(now time.Time) {
	due, err := s.repo.ListDue(now)
	if err != nil {
		s.logger.Error("查询到期额度账号失败", zap.Error(err))
		return
	}
	if len(due) == 0 {
		return
	}
	sem := make(chan struct{}, s.cfg.Quota.MaxConcurrencyV())
	var wg sync.WaitGroup
	for i := range due {
		wg.Add(1)
		go func(accountID string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			s.svc.FetchAccountNow(context.Background(), accountID)
		}(due[i].ID)
	}
	wg.Wait()
}
