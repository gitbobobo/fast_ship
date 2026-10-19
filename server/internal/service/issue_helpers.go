package service

import (
	"encoding/json"
	"fmt"
	"github.com/godbobo/fast_ship/server/internal/api"
	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/godbobo/fast_ship/server/internal/pkg/errs"
	"github.com/godbobo/fast_ship/server/internal/pkg/githubmedia"
	"github.com/google/uuid"
	"math"
	"sort"
	"strings"
	"time"
)

func buildInternalIssueMeta(issueID, userID string, workflowStatus model.IssueWorkflowStatus, now time.Time) *model.IssueInternalMeta {
	meta := &model.IssueInternalMeta{
		IssueID:         issueID,
		UpdatedByUserID: userID,
		CreatedAt:       now,
		UpdatedAt:       now,
	}
	applyExplicitWorkflowStatus(meta, workflowStatus, now)
	return meta
}

func (s *IssueService) internalMetaByIssueIDs(issues []model.Issue) (map[string]*model.IssueInternalMeta, error) {
	issueIDs := make([]string, 0, len(issues))
	for _, issue := range issues {
		issueIDs = append(issueIDs, issue.ID)
	}

	raw, err := s.internalMetaRepo.ListByIssueIDs(issueIDs)
	if err != nil {
		return nil, err
	}

	result := make(map[string]*model.IssueInternalMeta, len(raw))
	for issueID, meta := range raw {
		current := meta
		result[issueID] = &current
	}
	return result, nil
}

func toIssueSyncStateResponse(state *model.IssueSyncState) *IssueSyncStateResponse {
	if state == nil {
		return &IssueSyncStateResponse{Status: model.IssueSyncStatusIdle}
	}

	resp := &IssueSyncStateResponse{
		Status:    state.Status,
		LastError: state.LastError,
	}
	if state.LastIssueUpdatedAt != nil {
		value := formatTime(state.LastIssueUpdatedAt.UTC())
		resp.LastIssueUpdatedAt = &value
	}
	if state.LastSyncedAt != nil {
		value := formatTime(state.LastSyncedAt.UTC())
		resp.LastSyncedAt = &value
	}
	if state.LastSuccessfulSyncAt != nil {
		value := formatTime(state.LastSuccessfulSyncAt.UTC())
		resp.LastSuccessfulSyncAt = &value
	}
	return resp
}

func toIssueCommentResponse(comment model.IssueComment) IssueCommentResponse {
	reactions := parseJSON[IssueReactionSummaryResponse](comment.ReactionsJSON)
	return IssueCommentResponse{
		Id:                comment.ID,
		IssueId:           comment.IssueID,
		Source:            comment.Source,
		GithubCommentId:   comment.GitHubCommentID,
		GithubNodeId:      comment.GitHubNodeID,
		Body:              comment.Body,
		BodyHtml:          githubmedia.RewriteHTMLMediaSources(comment.BodyHTML),
		HtmlUrl:           comment.HTMLURL,
		Author:            IssueActorResponse{Login: comment.AuthorLogin, AvatarUrl: githubmedia.RewriteMediaURL(comment.AuthorAvatarURL)},
		AuthorAssociation: comment.AuthorAssociation,
		Reactions:         reactions,
		CreatedAt:         formatTime(comment.GitHubCreatedAt),
		UpdatedAt:         formatTime(comment.GitHubUpdatedAt),
	}
}

func toIssueTimelineResponse(event model.IssueTimelineEvent) IssueTimelineEventResponse {
	return IssueTimelineEventResponse{
		Id:            event.ID,
		IssueId:       event.IssueID,
		EventKey:      event.EventKey,
		EventType:     event.EventType,
		GithubEventId: event.GitHubEventID,
		Actor:         IssueActorResponse{Login: event.ActorLogin, AvatarUrl: githubmedia.RewriteMediaURL(event.ActorAvatarURL)},
		Body:          event.Body,
		Summary:       event.Summary,
		Payload:       parseJSON[map[string]any](event.PayloadJSON),
		CreatedAt:     formatTime(event.GitHubCreatedAt),
	}
}

func extractLabelNames(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}

	var names []string
	if err := json.Unmarshal([]byte(raw), &names); err == nil {
		return names
	}

	var payloads []issueLabelPayload
	if err := json.Unmarshal([]byte(raw), &payloads); err == nil {
		result := make([]string, 0, len(payloads))
		for _, p := range payloads {
			if p.Name != "" {
				result = append(result, p.Name)
			}
		}
		return result
	}

	return nil
}

func (s *IssueService) resolveLabels(projectID string, labelNames []string, labelMap map[string]model.GitHubRepoLabel) []IssueLabelResponse {
	if len(labelNames) == 0 {
		return nil
	}
	if labelMap == nil {
		allLabels, err := s.githubRepoLabelRepo.ListByProject(projectID)
		if err == nil {
			labelMap = make(map[string]model.GitHubRepoLabel, len(allLabels))
			for _, l := range allLabels {
				labelMap[l.Name] = l
			}
		}
	}
	result := make([]IssueLabelResponse, 0, len(labelNames))
	for _, name := range labelNames {
		if repoLabel, ok := labelMap[name]; ok {
			result = append(result, IssueLabelResponse{
				Name:        repoLabel.Name,
				Color:       repoLabel.Color,
				Description: repoLabel.Description,
			})
		} else {
			result = append(result, IssueLabelResponse{
				Name:  name,
				Color: "999999",
			})
		}
	}
	return result
}

func (s *IssueService) buildLabelMap(projectID string) map[string]model.GitHubRepoLabel {
	allLabels, err := s.githubRepoLabelRepo.ListByProject(projectID)
	if err != nil {
		return nil
	}
	m := make(map[string]model.GitHubRepoLabel, len(allLabels))
	for _, l := range allLabels {
		m[l.Name] = l
	}
	return m
}

func buildIssueReference(issue model.Issue) string {
	if issue.Source == model.IssueSourceGitHub && issue.GitHubMeta != nil {
		return fmt.Sprintf("GH-%d", issue.GitHubMeta.Number)
	}
	return fmt.Sprintf("INT-%d", issue.SequenceNumber)
}

func toJSONString(v any) string {
	if v == nil {
		return ""
	}
	data, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	return string(data)
}

func parseJSON[T any](raw string) T {
	var target T
	if strings.TrimSpace(raw) == "" {
		return target
	}
	_ = json.Unmarshal([]byte(raw), &target)
	return target
}

type checklistSnapshot struct {
	ProgressPercent *int
	Total           int
	Done            int
}

func buildChecklistSnapshot(issueID, userID string, items []IssueChecklistItemInput) ([]model.IssueChecklistItem, checklistSnapshot, error) {
	now := time.Now().UTC()
	result := make([]model.IssueChecklistItem, 0, len(items))
	done := 0

	for index, item := range items {
		title := strings.TrimSpace(item.Title)
		if title == "" {
			return nil, checklistSnapshot{}, errs.ErrInvalidParams
		}

		id := strings.TrimSpace(api.Deref(item.Id))
		if id == "" {
			id = uuid.NewString()
		}

		result = append(result, model.IssueChecklistItem{
			ID:              id,
			IssueID:         issueID,
			Title:           title,
			IsCompleted:     api.Deref(item.IsCompleted),
			SortOrder:       index,
			CreatedByUserID: userID,
			CreatedAt:       now,
			UpdatedAt:       now,
		})
		if api.Deref(item.IsCompleted) {
			done++
		}
	}

	snapshot := checklistSnapshot{
		Total: len(result),
		Done:  done,
	}
	if len(result) > 0 {
		value := int(math.Round(float64(done*100) / float64(len(result))))
		snapshot.ProgressPercent = &value
	}
	return result, snapshot, nil
}

func applyExplicitWorkflowStatus(meta *model.IssueInternalMeta, workflowStatus model.IssueWorkflowStatus, now time.Time) {
	meta.WorkflowStatus = workflowStatus

	switch workflowStatus {
	case "", model.IssueWorkflowStatusTodo:
		if meta.CompletedAt == nil {
			meta.StartedAt = nil
		}
	case model.IssueWorkflowStatusInProgress:
		if meta.StartedAt == nil {
			value := now
			meta.StartedAt = &value
		}
	case model.IssueWorkflowStatusDone:
		if meta.StartedAt == nil {
			value := now
			meta.StartedAt = &value
		}
		if meta.CompletedAt == nil {
			value := now
			meta.CompletedAt = &value
		}
	}
}

func formatTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func isValidIssueState(state model.IssueState) bool {
	switch state {
	case model.IssueStateOpen, model.IssueStateClosed:
		return true
	default:
		return false
	}
}

func normalizeIssueStateReason(state *model.IssueState, reason *string) (string, *errs.AppError) {
	if state == nil {
		return "", nil
	}

	value := ""
	if reason != nil {
		value = strings.TrimSpace(*reason)
	}
	if value == "" {
		return "", nil
	}

	switch *state {
	case model.IssueStateClosed:
		if value == "completed" || value == "not_planned" {
			return value, nil
		}
	case model.IssueStateOpen:
		if value == "reopened" {
			return value, nil
		}
	}
	return "", errs.ErrInvalidParams
}

func normalizeGitHubLabels(labels []string) ([]string, *errs.AppError) {
	normalized := make([]string, 0, len(labels))
	seen := make(map[string]struct{}, len(labels))

	for _, label := range labels {
		value := strings.TrimSpace(label)
		if value == "" {
			return nil, errs.ErrInvalidParams
		}
		key := strings.ToLower(value)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		normalized = append(normalized, value)
	}

	return normalized, nil
}

func resolveInternalLabels(names []string, repoLabels []IssueLabelResponse) ([]string, *errs.AppError) {
	labelMap := make(map[string]IssueLabelResponse, len(repoLabels))
	for _, l := range repoLabels {
		labelMap[strings.ToLower(l.Name)] = l
	}
	result := make([]string, 0, len(names))
	seen := make(map[string]struct{}, len(names))
	for _, name := range names {
		value := strings.TrimSpace(name)
		if value == "" {
			return nil, errs.ErrInvalidParams
		}
		key := strings.ToLower(value)
		if _, ok := seen[key]; ok {
			continue
		}
		if _, ok := labelMap[key]; !ok {
			return nil, errs.New(errs.ErrInvalidParams.Code, fmt.Sprintf("标签不存在: %s", value))
		}
		seen[key] = struct{}{}
		result = append(result, value)
	}
	return result, nil
}

func sortedKeys(values map[string]struct{}) []string {
	items := make([]string, 0, len(values))
	for value := range values {
		items = append(items, value)
	}
	sort.Strings(items)
	return items
}
