package service

import (
	"errors"
	"time"

	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/godbobo/fast_ship/server/internal/pkg/errs"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

// MarkIssueRead 把用户在某个 GitHub Issue 上的已读水位推到本地评论的最新时间。
// 详情页在 Issue 与评论都加载成功后调用；内部 Issue 没有未读概念，直接成功返回。
func (s *IssueService) MarkIssueRead(issueID, userID string) error {
	issue, err := s.issueRepo.FindByID(issueID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return errs.ErrIssueNotFound
		}
		return errs.ErrInternal
	}
	if _, err := s.projectRepo.FindByID(issue.ProjectID, userID); err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return errs.ErrProjectNotFound
		}
		return errs.ErrNotOwner
	}
	if issue.Source != model.IssueSourceGitHub {
		return nil
	}

	latest, err := s.commentRepo.MaxCreatedAt(issueID)
	if err != nil {
		return errs.ErrInternal
	}
	// 没有评论时用当前时间兜底；有评论时对齐最新评论，避免本地时钟与
	// GitHub 时间戳的偏差把刚同步进来的评论算成未读。
	at := time.Now().UTC()
	if latest != nil {
		at = *latest
	}
	if err := s.readStateRepo.Advance(userID, issueID, at); err != nil {
		return errs.ErrInternal
	}
	return nil
}

// advanceReadWatermark 在用户通过 Fast Ship 发评论后把水位推到该评论的时间。
// 发货钩子评论不走这里：它们本身不算未读，也不该替用户消掉真正的未读。
func (s *IssueService) advanceReadWatermark(issueID, userID string, at time.Time) {
	if err := s.readStateRepo.Advance(userID, issueID, at); err != nil {
		s.logger.Warn("推进已读水位失败",
			zap.String("issue_id", issueID),
			zap.String("user_id", userID),
			zap.Error(err),
		)
	}
}

// unreadCountsByIssueIDs 返回 GitHub Issue 的未读评论数；内部 Issue 恒为 0。
func (s *IssueService) unreadCountsByIssueIDs(userID string, issues []model.Issue) (map[string]int64, error) {
	ids := make([]string, 0, len(issues))
	for _, issue := range issues {
		if issue.Source == model.IssueSourceGitHub {
			ids = append(ids, issue.ID)
		}
	}
	counts, err := s.readStateRepo.CountUnreadByIssueIDs(userID, ids)
	if err != nil {
		return nil, err
	}
	return counts, nil
}
