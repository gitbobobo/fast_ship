package service

import (
	"encoding/json"
	"fmt"
	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/godbobo/fast_ship/server/internal/pkg/errs"
	ghclient "github.com/godbobo/fast_ship/server/internal/pkg/github"
	"github.com/godbobo/fast_ship/server/internal/pkg/githubmedia"
	gh "github.com/google/go-github/v62/github"
	"github.com/google/uuid"
	"math"
	"sort"
	"strconv"
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
		ID:                comment.ID,
		IssueID:           comment.IssueID,
		Source:            comment.Source,
		GitHubCommentID:   comment.GitHubCommentID,
		GitHubNodeID:      comment.GitHubNodeID,
		Body:              comment.Body,
		BodyHTML:          githubmedia.RewriteHTMLMediaSources(comment.BodyHTML),
		HTMLURL:           comment.HTMLURL,
		Author:            IssueActorResponse{Login: comment.AuthorLogin, AvatarURL: githubmedia.RewriteMediaURL(comment.AuthorAvatarURL)},
		AuthorAssociation: comment.AuthorAssociation,
		Reactions:         reactions,
		CreatedAt:         formatTime(comment.GitHubCreatedAt),
		UpdatedAt:         formatTime(comment.GitHubUpdatedAt),
	}
}

func buildGitHubIssueCommentModel(issueID string, item *ghclient.IssueComment) *model.IssueComment {
	comment := &model.IssueComment{
		ID:                uuid.NewString(),
		IssueID:           issueID,
		Source:            model.IssueSourceGitHub,
		GitHubCommentID:   item.GetID(),
		GitHubNodeID:      item.GetNodeID(),
		Body:              item.GetBody(),
		BodyHTML:          item.GetBodyHTML(),
		HTMLURL:           item.GetHTMLURL(),
		AuthorLogin:       item.GetUser().GetLogin(),
		AuthorAvatarURL:   item.GetUser().GetAvatarURL(),
		AuthorAssociation: item.GetAuthorAssociation(),
		ReactionsJSON:     toJSONString(mapReactions(item.Reactions)),
		RawJSON:           toJSONString(item),
	}
	if createdAt := item.GetCreatedAt(); !createdAt.IsZero() {
		comment.GitHubCreatedAt = createdAt.UTC()
	}
	if updatedAt := item.GetUpdatedAt(); !updatedAt.IsZero() {
		comment.GitHubUpdatedAt = updatedAt.UTC()
	}
	if comment.GitHubCreatedAt.IsZero() {
		comment.GitHubCreatedAt = time.Now().UTC()
	}
	if comment.GitHubUpdatedAt.IsZero() {
		comment.GitHubUpdatedAt = comment.GitHubCreatedAt
	}
	return comment
}

func toIssueTimelineResponse(event model.IssueTimelineEvent) IssueTimelineEventResponse {
	return IssueTimelineEventResponse{
		ID:            event.ID,
		IssueID:       event.IssueID,
		EventKey:      event.EventKey,
		EventType:     event.EventType,
		GitHubEventID: event.GitHubEventID,
		Actor:         IssueActorResponse{Login: event.ActorLogin, AvatarURL: githubmedia.RewriteMediaURL(event.ActorAvatarURL)},
		Body:          event.Body,
		Summary:       event.Summary,
		Payload:       parseJSON[map[string]any](event.PayloadJSON),
		CreatedAt:     formatTime(event.GitHubCreatedAt),
	}
}

func mapUsers(users []*gh.User) []issueUserPayload {
	out := make([]issueUserPayload, 0, len(users))
	for _, user := range users {
		if user == nil || user.GetLogin() == "" {
			continue
		}
		out = append(out, issueUserPayload{
			Login:     user.GetLogin(),
			AvatarURL: user.GetAvatarURL(),
		})
	}
	return out
}

func mapLabelNames(labels []*gh.Label) []string {
	out := make([]string, 0, len(labels))
	for _, label := range labels {
		if label == nil {
			continue
		}
		name := strings.TrimSpace(label.GetName())
		if name != "" {
			out = append(out, name)
		}
	}
	return out
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

func mapMilestone(m *gh.Milestone) *issueMilestonePayload {
	if m == nil || m.GetTitle() == "" {
		return nil
	}
	return &issueMilestonePayload{
		Number:      m.GetNumber(),
		Title:       m.GetTitle(),
		State:       m.GetState(),
		Description: m.GetDescription(),
	}
}

func mapReactions(r *gh.Reactions) IssueReactionSummaryResponse {
	if r == nil {
		return IssueReactionSummaryResponse{}
	}
	return IssueReactionSummaryResponse{
		TotalCount: r.GetTotalCount(),
		PlusOne:    r.GetPlusOne(),
		MinusOne:   r.GetMinusOne(),
		Laugh:      r.GetLaugh(),
		Hooray:     r.GetHooray(),
		Confused:   r.GetConfused(),
		Heart:      r.GetHeart(),
		Rocket:     r.GetRocket(),
		Eyes:       r.GetEyes(),
	}
}

func summarizeTimeline(item *ghclient.TimelineEvent) string {
	eventType := item.GetEvent()
	switch eventType {
	case "labeled":
		return fmt.Sprintf("添加了标签 %s", item.GetLabel().GetName())
	case "unlabeled":
		return fmt.Sprintf("移除了标签 %s", item.GetLabel().GetName())
	case "assigned":
		return fmt.Sprintf("指派给 %s", item.GetAssignee().GetLogin())
	case "unassigned":
		return fmt.Sprintf("取消指派 %s", item.GetAssignee().GetLogin())
	case "milestoned":
		return fmt.Sprintf("加入里程碑 %s", item.GetMilestone().GetTitle())
	case "demilestoned":
		return fmt.Sprintf("移出里程碑 %s", item.GetMilestone().GetTitle())
	case "renamed":
		return fmt.Sprintf("标题从 %s 改为 %s", item.GetRename().GetFrom(), item.GetRename().GetTo())
	case "closed":
		return "关闭了问题"
	case "reopened":
		return "重新打开了问题"
	case "locked":
		return "锁定了讨论"
	case "unlocked":
		return "解锁了讨论"
	case "cross-referenced":
		source := item.GetSource()
		if source != nil && source.Issue != nil {
			return fmt.Sprintf("被 #%d 交叉引用", source.Issue.GetNumber())
		}
		return "发生了交叉引用"
	case "referenced":
		if item.GetCommitID() != "" {
			return fmt.Sprintf("被提交 %s 引用", shortSHA(item.GetCommitID()))
		}
		return "被提交引用"
	case "commented":
		return "添加了评论"
	case "subscribed":
		return "订阅了此问题"
	case "unsubscribed":
		return "取消订阅此问题"
	case "added_type", "issue_type_added":
		if item.GetIssueType() != nil {
			return fmt.Sprintf("添加了问题类型 %s", item.GetIssueType().GetName())
		}
		return "添加了问题类型"
	case "removed_type", "issue_type_removed":
		if item.GetIssueType() != nil {
			return fmt.Sprintf("移除了问题类型 %s", item.GetIssueType().GetName())
		}
		return "移除了问题类型"
	default:
		if eventType == "" {
			return "发生了更新"
		}
		return eventType
	}
}

func buildTimelineEventKey(item *ghclient.TimelineEvent) string {
	if item.GetID() != 0 {
		return fmt.Sprintf("gh:%d", item.GetID())
	}

	parts := []string{
		item.GetEvent(),
		formatTime(item.GetCreatedAt().UTC()),
		firstNonEmpty(item.GetActor().GetLogin(), item.GetUser().GetLogin()),
		item.GetLabel().GetName(),
		item.GetMilestone().GetTitle(),
		item.GetCommitID(),
		item.GetBody(),
	}
	return "fallback:" + strings.Join(parts, "|")
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

		id := strings.TrimSpace(item.ID)
		if id == "" {
			id = uuid.NewString()
		}

		result = append(result, model.IssueChecklistItem{
			ID:              id,
			IssueID:         issueID,
			Title:           title,
			IsCompleted:     item.IsCompleted,
			SortOrder:       index,
			CreatedByUserID: userID,
			CreatedAt:       now,
			UpdatedAt:       now,
		})
		if item.IsCompleted {
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

func shortSHA(sha string) string {
	if len(sha) <= 7 {
		return sha
	}
	return sha[:7]
}

func parseIssueNumberQuery(query string) (int, bool) {
	num, err := strconv.Atoi(strings.TrimSpace(query))
	return num, err == nil
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

func issueCommentCount(issue model.Issue) int {
	if issue.GitHubMeta == nil {
		return 0
	}
	return issue.GitHubMeta.CommentsCount
}

func issueSortNumber(issue model.Issue) int {
	if issue.Source == model.IssueSourceGitHub && issue.GitHubMeta != nil {
		return issue.GitHubMeta.Number
	}
	return issue.SequenceNumber
}

func sortIssues(items []model.Issue, sortKey string) {
	switch sortKey {
	case "updated_asc":
		sort.Slice(items, func(i, j int) bool {
			if items[i].UpdatedAt.Equal(items[j].UpdatedAt) {
				return issueSortNumber(items[i]) < issueSortNumber(items[j])
			}
			return items[i].UpdatedAt.Before(items[j].UpdatedAt)
		})
	case "created_desc":
		sort.Slice(items, func(i, j int) bool {
			if items[i].CreatedAt.Equal(items[j].CreatedAt) {
				return issueSortNumber(items[i]) > issueSortNumber(items[j])
			}
			return items[i].CreatedAt.After(items[j].CreatedAt)
		})
	case "created_asc":
		sort.Slice(items, func(i, j int) bool {
			if items[i].CreatedAt.Equal(items[j].CreatedAt) {
				return issueSortNumber(items[i]) < issueSortNumber(items[j])
			}
			return items[i].CreatedAt.Before(items[j].CreatedAt)
		})
	case "comments_desc":
		sort.Slice(items, func(i, j int) bool {
			if issueCommentCount(items[i]) == issueCommentCount(items[j]) {
				return items[i].UpdatedAt.After(items[j].UpdatedAt)
			}
			return issueCommentCount(items[i]) > issueCommentCount(items[j])
		})
	case "comments_asc":
		sort.Slice(items, func(i, j int) bool {
			if issueCommentCount(items[i]) == issueCommentCount(items[j]) {
				return items[i].UpdatedAt.Before(items[j].UpdatedAt)
			}
			return issueCommentCount(items[i]) < issueCommentCount(items[j])
		})
	default:
		sort.Slice(items, func(i, j int) bool {
			if items[i].UpdatedAt.Equal(items[j].UpdatedAt) {
				return issueSortNumber(items[i]) > issueSortNumber(items[j])
			}
			return items[i].UpdatedAt.After(items[j].UpdatedAt)
		})
	}
}

func matchesIssueFilters(issue model.Issue, gitHubMeta *model.IssueGitHubMeta, meta *model.IssueInternalMeta, filters IssueListFilters) bool {
	if filters.State != "" && string(issue.State) != filters.State {
		return false
	}

	switch filters.Workflow {
	case "":
	case "unset":
		if meta != nil && meta.WorkflowStatus != "" {
			return false
		}
	default:
		if meta == nil || string(meta.WorkflowStatus) != filters.Workflow {
			return false
		}
	}

	query := strings.TrimSpace(filters.Query)
	if query != "" {
		queryLower := strings.ToLower(query)
		matches := strings.Contains(strings.ToLower(issue.Title), queryLower) ||
			strings.Contains(strings.ToLower(issue.Body), queryLower) ||
			strings.Contains(strings.ToLower(issue.AuthorLogin), queryLower) ||
			strings.Contains(strings.ToLower(buildIssueReference(issue)), queryLower)
		if num, ok := parseIssueNumberQuery(query); ok {
			if issue.SequenceNumber == num {
				matches = true
			}
			if gitHubMeta != nil && gitHubMeta.Number == num {
				matches = true
			}
		}
		if !matches {
			return false
		}
	}

	if filters.Label != "" {
		matched := false
		if gitHubMeta != nil {
			for _, name := range extractLabelNames(gitHubMeta.LabelsJSON) {
				if strings.EqualFold(name, filters.Label) {
					matched = true
					break
				}
			}
		}
		if !matched && meta != nil && meta.LabelsJSON != "" {
			for _, name := range extractLabelNames(meta.LabelsJSON) {
				if strings.EqualFold(name, filters.Label) {
					matched = true
					break
				}
			}
		}
		if !matched {
			return false
		}
	}

	if filters.Source != "" && string(issue.Source) != filters.Source {
		return false
	}

	if filters.Assignee != "" {
		if gitHubMeta == nil {
			return false
		}
		matched := false
		for _, assignee := range parseJSON[[]issueUserPayload](gitHubMeta.AssigneesJSON) {
			if strings.EqualFold(assignee.Login, filters.Assignee) {
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	}

	if filters.Milestone != "" {
		if gitHubMeta == nil {
			return false
		}
		milestone := parseJSON[*issueMilestonePayload](gitHubMeta.MilestoneJSON)
		if milestone == nil || !strings.EqualFold(milestone.Title, filters.Milestone) {
			return false
		}
	}

	return true
}
