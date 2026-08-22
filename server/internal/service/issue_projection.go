package service

import (
	"errors"
	"fmt"
	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/godbobo/fast_ship/server/internal/pkg/errs"
	"github.com/godbobo/fast_ship/server/internal/pkg/githubmedia"
	"gorm.io/gorm"
	"mime"
	"path/filepath"
	"strings"
)

func (s *IssueService) loadInternalMeta(issueID string) (*model.IssueInternalMeta, error) {
	meta, err := s.internalMetaRepo.Get(issueID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, errs.ErrInternal
	}
	return meta, nil
}

// InternalMetaWorkflowStatus 返回 Issue 当前内部工作流状态，无记录时返回 ""。
func (s *IssueService) InternalMetaWorkflowStatus(issueID string) (model.IssueWorkflowStatus, error) {
	meta, err := s.loadInternalMeta(issueID)
	if err != nil {
		return "", err
	}
	if meta == nil {
		return "", nil
	}
	return meta.WorkflowStatus, nil
}

func (s *IssueService) loadChecklist(issueID string) ([]model.IssueChecklistItem, error) {
	items, err := s.checklistRepo.ListByIssueID(issueID)
	if err != nil {
		return nil, errs.ErrInternal
	}
	return items, nil
}

func (s *IssueService) toIssueResponse(issue model.Issue, meta *model.IssueInternalMeta, checklist []model.IssueChecklistItem, labelMap map[string]model.GitHubRepoLabel, shipHook *model.IssueShipHook) IssueResponse {
	resp := IssueResponse{
		ID:             issue.ID,
		ProjectID:      issue.ProjectID,
		Source:         issue.Source,
		SequenceNumber: issue.SequenceNumber,
		Reference:      buildIssueReference(issue),
		State:          issue.State,
		StateReason:    issue.StateReason,
		Title:          issue.Title,
		Body:           issue.Body,
		BodyHTML:       githubmedia.RewriteHTMLMediaSources(issue.BodyHTML),
		Author: IssueActorResponse{
			Login:     issue.AuthorLogin,
			AvatarURL: githubmedia.RewriteMediaURL(issue.AuthorAvatarURL),
		},
		CreatedAt:    formatTime(issue.CreatedAt),
		UpdatedAt:    formatTime(issue.UpdatedAt),
		InternalMeta: s.toIssueInternalMetaResponse(issue.ProjectID, meta, checklist, labelMap),
		ShipHook:     s.shipHookService.toIssueShipHookResponse(shipHook),
	}
	if issue.ClosedAt != nil {
		value := formatTime(issue.ClosedAt.UTC())
		resp.ClosedAt = &value
	}
	if issue.GitHubMeta != nil {
		resp.GitHub = s.toIssueGitHubResponse(issue.ProjectID, issue.GitHubMeta, labelMap)
	}
	return resp
}

func (s *IssueService) toIssueGitHubResponse(projectID string, meta *model.IssueGitHubMeta, labelMap map[string]model.GitHubRepoLabel) *IssueGitHubResponse {
	if meta == nil {
		return nil
	}

	assignees := parseJSON[[]issueUserPayload](meta.AssigneesJSON)
	labelNames := extractLabelNames(meta.LabelsJSON)
	milestone := parseJSON[*issueMilestonePayload](meta.MilestoneJSON)
	reactions := parseJSON[IssueReactionSummaryResponse](meta.ReactionsJSON)

	resp := &IssueGitHubResponse{
		GitHubIssueID:     meta.GitHubIssueID,
		GitHubNodeID:      meta.GitHubNodeID,
		Number:            meta.Number,
		HTMLURL:           meta.HTMLURL,
		AuthorAssociation: meta.AuthorAssociation,
		Assignees:         make([]IssueActorResponse, 0, len(assignees)),
		Labels:            s.resolveLabels(projectID, labelNames, labelMap),
		Reactions:         reactions,
		CommentsCount:     meta.CommentsCount,
		Locked:            meta.Locked,
		ActiveLockReason:  meta.ActiveLockReason,
		SyncedAt:          formatTime(meta.SyncedAt),
	}
	for _, assignee := range assignees {
		resp.Assignees = append(resp.Assignees, IssueActorResponse{
			Login:     assignee.Login,
			AvatarURL: githubmedia.RewriteMediaURL(assignee.AvatarURL),
		})
	}
	if milestone != nil {
		resp.Milestone = &IssueMilestoneResponse{
			Number:      milestone.Number,
			Title:       milestone.Title,
			State:       milestone.State,
			Description: milestone.Description,
		}
	}
	return resp
}

func (s *IssueService) toIssueInternalMetaResponse(projectID string, meta *model.IssueInternalMeta, checklist []model.IssueChecklistItem, labelMap map[string]model.GitHubRepoLabel) *IssueInternalMetaResponse {
	if meta == nil && len(checklist) == 0 {
		return nil
	}

	resp := &IssueInternalMetaResponse{}
	if meta != nil {
		resp.WorkflowStatus = meta.WorkflowStatus
		resp.ProgressPercent = meta.ProgressPercent
		resp.ChecklistTotal = meta.ChecklistTotal
		resp.ChecklistDone = meta.ChecklistDone
		if meta.StartedAt != nil {
			value := formatTime(meta.StartedAt.UTC())
			resp.StartedAt = &value
		}
		if meta.CompletedAt != nil {
			value := formatTime(meta.CompletedAt.UTC())
			resp.CompletedAt = &value
		}
		if meta.ChecklistUpdatedAt != nil {
			value := formatTime(meta.ChecklistUpdatedAt.UTC())
			resp.ChecklistUpdatedAt = &value
		}
		if !meta.UpdatedAt.IsZero() {
			value := formatTime(meta.UpdatedAt.UTC())
			resp.UpdatedAt = &value
		}
		resp.Labels = s.resolveLabels(projectID, extractLabelNames(meta.LabelsJSON), labelMap)
	}
	if len(checklist) > 0 {
		resp.Checklist = make([]IssueChecklistItemResponse, 0, len(checklist))
		for _, item := range checklist {
			resp.Checklist = append(resp.Checklist, IssueChecklistItemResponse{
				ID:          item.ID,
				Title:       item.Title,
				IsCompleted: item.IsCompleted,
				SortOrder:   item.SortOrder,
			})
		}
	}
	return resp
}

func toIssueAssetResponse(asset model.IssueAsset) IssueAssetResponse {
	contentURL := buildIssueAssetContentURL(asset.ID)
	return IssueAssetResponse{
		ID:         asset.ID,
		IssueID:    asset.IssueID,
		FileName:   asset.FileName,
		MimeType:   asset.MimeType,
		FileSize:   asset.FileSize,
		ContentURL: contentURL,
		Markdown:   fmt.Sprintf("![%s](%s)", issueAssetAltText(asset.FileName), contentURL),
		CreatedAt:  asset.CreatedAt.UTC().Format("2006-01-02T15:04:05Z"),
	}
}

func toDraftIssueAssetResponse(asset model.IssueDraftAsset) IssueAssetResponse {
	contentURL := buildIssueAssetContentURL(asset.ID)
	return IssueAssetResponse{
		ID:         asset.ID,
		IssueID:    "",
		FileName:   asset.FileName,
		MimeType:   asset.MimeType,
		FileSize:   asset.FileSize,
		ContentURL: contentURL,
		Markdown:   fmt.Sprintf("![%s](%s)", issueAssetAltText(asset.FileName), contentURL),
		CreatedAt:  asset.CreatedAt.UTC().Format("2006-01-02T15:04:05Z"),
	}
}

func buildIssueAssetContentURL(assetID string) string {
	return fmt.Sprintf("/api/issues/assets/%s/content", assetID)
}

func issueAssetAltText(fileName string) string {
	trimmed := strings.TrimSpace(strings.TrimSuffix(fileName, filepath.Ext(fileName)))
	if trimmed == "" {
		return "image"
	}
	return trimmed
}

func normalizeIssueAssetFileName(fileName, mimeType string) string {
	name := strings.TrimSpace(fileName)
	if name == "" {
		name = "image"
	}
	ext := strings.ToLower(filepath.Ext(name))
	if ext != "" {
		return name
	}
	if exts, err := mime.ExtensionsByType(mimeType); err == nil && len(exts) > 0 {
		return name + exts[0]
	}
	return name
}

func buildIssueAssetStoragePath(projectID, issueID, assetID, fileName, mimeType string) string {
	name := normalizeIssueAssetFileName(fileName, mimeType)
	ext := strings.ToLower(filepath.Ext(name))
	if ext == "" {
		ext = ".bin"
	}
	return fmt.Sprintf("%s/issues/%s/assets/%s%s", projectID, issueID, assetID, ext)
}

func buildIssueDraftAssetStoragePath(projectID, assetID, fileName, mimeType string) string {
	name := normalizeIssueAssetFileName(fileName, mimeType)
	ext := strings.ToLower(filepath.Ext(name))
	if ext == "" {
		ext = ".bin"
	}
	return fmt.Sprintf("%s/issues/drafts/assets/%s%s", projectID, assetID, ext)
}

func extractIssueAssetIDs(body string) map[string]struct{} {
	matches := issueAssetContentPattern.FindAllStringSubmatch(body, -1)
	result := make(map[string]struct{}, len(matches))
	for _, match := range matches {
		if len(match) < 2 {
			continue
		}
		id := strings.TrimSpace(match[1])
		if id == "" {
			continue
		}
		result[id] = struct{}{}
	}
	return result
}
