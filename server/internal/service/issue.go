package service

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/godbobo/fast_ship/server/internal/pkg/errs"
	ghclient "github.com/godbobo/fast_ship/server/internal/pkg/github"
	"github.com/google/uuid"
	"go.uber.org/zap"
	"gorm.io/gorm"
	"io"
	"net/http"
	"strings"
	"time"
)

func (s *IssueService) CleanupExpiredPendingIssueAssets() error {
	cutoff := time.Now().UTC().Add(-issueAssetPendingTTL)
	assets, err := s.assetRepo.ListPendingCreatedBefore(cutoff)
	if err != nil {
		return err
	}
	if len(assets) == 0 {
		return nil
	}

	idsByIssue := make(map[string][]string)
	for _, asset := range assets {
		idsByIssue[asset.IssueID] = append(idsByIssue[asset.IssueID], asset.ID)
	}

	for issueID, ids := range idsByIssue {
		if err := s.deleteIssueAssets(issueID, ids); err != nil {
			return err
		}
	}

	draftAssets, err := s.draftAssetRepo.ListCreatedBefore(cutoff)
	if err != nil {
		return err
	}
	if len(draftAssets) == 0 {
		return nil
	}

	idsByProject := make(map[string][]string)
	for _, asset := range draftAssets {
		idsByProject[asset.ProjectID] = append(idsByProject[asset.ProjectID], asset.ID)
	}

	for projectID, ids := range idsByProject {
		if err := s.deleteDraftIssueAssets(projectID, ids); err != nil {
			return err
		}
	}
	return nil
}

func (s *IssueService) CreateInternalIssue(projectID, userID string, req CreateInternalIssueRequest) (*IssueResponse, error) {
	title := strings.TrimSpace(req.Title)
	if title == "" || !model.IsValidIssueWorkflowStatus(req.WorkflowStatus) {
		return nil, errs.ErrInvalidParams
	}

	if _, err := s.projectRepo.FindByID(projectID, userID); err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errs.ErrProjectNotFound
		}
		return nil, errs.ErrInternal
	}

	user, err := s.userRepo.FindByID(userID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errs.ErrUserNotFound
		}
		return nil, errs.ErrInternal
	}

	sequenceNumber, err := s.issueRepo.NextSequenceNumber(projectID)
	if err != nil {
		return nil, errs.ErrInternal
	}

	now := time.Now().UTC()
	issue := &model.Issue{
		ID:              uuid.NewString(),
		ProjectID:       projectID,
		Source:          model.IssueSourceInternal,
		SequenceNumber:  sequenceNumber,
		State:           model.IssueStateOpen,
		Title:           title,
		Body:            req.Body,
		AuthorUserID:    user.ID,
		AuthorLogin:     user.Username,
		AuthorAvatarURL: "",
		CreatedAt:       now,
		UpdatedAt:       now,
	}

	var meta *model.IssueInternalMeta
	if req.WorkflowStatus != "" {
		meta = buildInternalIssueMeta(issue.ID, userID, req.WorkflowStatus, now)
	}

	if err := s.issueRepo.Transaction(func(tx *gorm.DB) error {
		if err := s.issueRepo.CreateTx(tx, issue); err != nil {
			return err
		}
		if err := s.attachDraftAssetsToIssueTx(tx, projectID, issue.ID, req.Body); err != nil {
			return err
		}
		if meta != nil {
			if err := s.internalMetaRepo.UpsertTx(tx, meta); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		var appErr *errs.AppError
		if errors.As(err, &appErr) {
			return nil, appErr
		}
		return nil, errs.ErrInternal
	}

	stored, err := s.issueRepo.FindByID(issue.ID)
	if err != nil {
		return nil, errs.ErrInternal
	}
	meta, err = s.loadInternalMeta(stored.ID)
	if err != nil {
		return nil, err
	}

	resp := s.toIssueResponse(*stored, meta, nil, nil, nil)
	return &resp, nil
}

func (s *IssueService) CreateGitHubIssue(projectID, userID string, req CreateInternalIssueRequest) (*IssueResponse, error) {
	title := strings.TrimSpace(req.Title)
	if title == "" {
		return nil, errs.ErrInvalidParams
	}

	project, err := s.projectRepo.FindByID(projectID, userID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errs.ErrProjectNotFound
		}
		return nil, errs.ErrInternal
	}

	tokenBytes, appErr := s.decryptGitHubToken(project)
	if appErr != nil {
		return nil, appErr
	}

	client := s.newClient(string(tokenBytes), project.GithubOwner, project.GithubRepo)

	ctx := context.Background()
	if err := s.validateIssueAssetReferences(projectID, "", req.Body); err != nil {
		return nil, err
	}

	createdIssue, err := client.CreateIssue(ctx, title, req.Body)
	if err != nil {
		return nil, errs.New(errs.ErrGitHubAPI.Code, fmt.Sprintf("创建 GitHub Issue 失败: %v", err))
	}

	stored, err := s.upsertGitHubIssue(projectID, createdIssue)
	if err != nil {
		return nil, err
	}
	var assetPathsToDelete []string
	if err := s.issueRepo.Transaction(func(tx *gorm.DB) error {
		var err error
		assetPathsToDelete, err = s.syncIssueAssetsTx(tx, projectID, stored.ID, req.Body)
		return err
	}); err != nil {
		return nil, mapIssueAssetReferenceError(err)
	}
	s.deleteIssueAssetFiles(assetPathsToDelete)

	if _, err := s.syncTimeline(ctx, client, stored, createdIssue.GetNumber()); err != nil {
		s.logger.Warn("sync timeline after creating github issue failed", zap.String("issue_id", stored.ID), zap.Error(err))
	}

	meta, err := s.loadInternalMeta(stored.ID)
	if err != nil {
		return nil, err
	}

	resp := s.toIssueResponse(*stored, meta, nil, nil, nil)
	return &resp, nil
}

func (s *IssueService) UpdateInternalIssue(issueID, userID string, req UpdateInternalIssueRequest) (*IssueResponse, error) {
	if req.Title == nil && req.Body == nil && req.State == nil && req.StateReason == nil && req.Labels == nil {
		return nil, errs.ErrInvalidParams
	}
	if req.State == nil && req.StateReason != nil {
		return nil, errs.ErrInvalidParams
	}

	issue, err := s.issueRepo.FindByID(issueID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errs.ErrIssueNotFound
		}
		return nil, errs.ErrInternal
	}
	if _, err := s.projectRepo.FindByID(issue.ProjectID, userID); err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errs.ErrProjectNotFound
		}
		return nil, errs.ErrNotOwner
	}
	if issue.Source == model.IssueSourceGitHub {
		if req.Title == nil && req.Body == nil && req.State == nil && req.Labels == nil {
			return nil, errs.ErrIssueReadOnly
		}
		if issue.GitHubMeta == nil {
			return nil, errs.ErrIssueReadOnly
		}
		if req.State != nil && !isValidIssueState(*req.State) {
			return nil, errs.ErrInvalidParams
		}

		stateReason := ""
		if req.State != nil {
			var appErr *errs.AppError
			stateReason, appErr = normalizeIssueStateReason(req.State, req.StateReason)
			if appErr != nil {
				return nil, appErr
			}
		}

		var labelsToUpdate *[]string
		if req.Labels != nil {
			normalizedLabels, appErr := normalizeGitHubLabels(*req.Labels)
			if appErr != nil {
				return nil, appErr
			}
			labelsToUpdate = &normalizedLabels
		}

		project, err := s.projectRepo.FindByID(issue.ProjectID, userID)
		if err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil, errs.ErrProjectNotFound
			}
			return nil, errs.ErrInternal
		}
		tokenBytes, appErr := s.decryptGitHubToken(project)
		if appErr != nil {
			return nil, appErr
		}

		client := s.newClient(string(tokenBytes), project.GithubOwner, project.GithubRepo)
		updateReq := ghclient.UpdateIssueRequest{}
		if req.Title != nil {
			title := strings.TrimSpace(*req.Title)
			if title == "" {
				return nil, errs.ErrInvalidParams
			}
			updateReq.Title = &title
		}
		if req.Body != nil {
			if err := s.validateIssueAssetReferences(project.ID, issue.ID, *req.Body); err != nil {
				return nil, err
			}
			updateReq.Body = req.Body
		}
		if req.State != nil {
			state := string(*req.State)
			updateReq.State = &state
			if stateReason != "" {
				updateReq.StateReason = &stateReason
			}
		}
		if labelsToUpdate != nil {
			updateReq.Labels = labelsToUpdate
		}
		updatedIssue, err := client.UpdateIssue(context.Background(), issue.GitHubMeta.Number, updateReq)
		if err != nil {
			return nil, errs.New(errs.ErrGitHubAPI.Code, fmt.Sprintf("更新 GitHub Issue 失败: %v", err))
		}

		stored, err := s.upsertGitHubIssue(project.ID, updatedIssue)
		if err != nil {
			return nil, err
		}
		if req.Body != nil {
			var assetPathsToDelete []string
			if err := s.issueRepo.Transaction(func(tx *gorm.DB) error {
				var err error
				assetPathsToDelete, err = s.syncIssueAssetsTx(tx, project.ID, stored.ID, *req.Body)
				return err
			}); err != nil {
				return nil, mapIssueAssetReferenceError(err)
			}
			s.deleteIssueAssetFiles(assetPathsToDelete)
		}
		if _, err := s.syncTimeline(context.Background(), client, stored, issue.GitHubMeta.Number); err != nil {
			return nil, err
		}
		meta, err := s.loadInternalMeta(stored.ID)
		if err != nil {
			return nil, err
		}
		resp := s.toIssueResponse(*stored, meta, nil, nil, nil)
		return &resp, nil
	}
	if issue.Source != model.IssueSourceInternal {
		return nil, errs.ErrIssueReadOnly
	}
	var internalMeta *model.IssueInternalMeta
	if req.Labels != nil {
		project, err := s.projectRepo.FindByID(issue.ProjectID, userID)
		if err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil, errs.ErrProjectNotFound
			}
			return nil, errs.ErrInternal
		}
		repoLabels, err := s.GetRepositoryLabels(project.ID, userID)
		if err != nil {
			return nil, err
		}
		resolvedLabels, appErr := resolveInternalLabels(*req.Labels, repoLabels)
		if appErr != nil {
			return nil, appErr
		}
		meta, err := s.loadInternalMeta(issue.ID)
		if err != nil {
			return nil, err
		}
		now := time.Now().UTC()
		if meta == nil {
			meta = &model.IssueInternalMeta{
				IssueID:   issue.ID,
				CreatedAt: now,
			}
		}
		meta.LabelsJSON = toJSONString(resolvedLabels)
		meta.UpdatedByUserID = userID
		meta.UpdatedAt = now
		internalMeta = meta
	}

	if req.Title != nil {
		title := strings.TrimSpace(*req.Title)
		if title == "" {
			return nil, errs.ErrInvalidParams
		}
		issue.Title = title
	}
	if req.Body != nil {
		issue.Body = *req.Body
		issue.BodyHTML = ""
	}
	if req.State != nil {
		if !isValidIssueState(*req.State) {
			return nil, errs.ErrInvalidParams
		}
		stateReason, appErr := normalizeIssueStateReason(req.State, req.StateReason)
		if appErr != nil {
			return nil, appErr
		}
		issue.State = *req.State
		issue.StateReason = stateReason
		if *req.State == model.IssueStateClosed {
			now := time.Now().UTC()
			issue.ClosedAt = &now
		} else {
			issue.ClosedAt = nil
		}
	}
	issue.UpdatedAt = time.Now().UTC()

	if err := s.issueRepo.Transaction(func(tx *gorm.DB) error {
		if err := s.issueRepo.SaveTx(tx, issue); err != nil {
			return err
		}
		if req.Labels != nil && internalMeta != nil {
			if err := s.internalMetaRepo.UpsertTx(tx, internalMeta); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		return nil, errs.ErrInternal
	}
	if req.Body != nil {
		if err := s.reconcileIssueAssets(issue.ID, *req.Body); err != nil {
			s.logger.Warn("reconcile issue assets failed", zap.String("issue_id", issue.ID), zap.Error(err))
		}
	}

	meta, err := s.loadInternalMeta(issue.ID)
	if err != nil {
		return nil, err
	}
	resp := s.toIssueResponse(*issue, meta, nil, nil, nil)
	return &resp, nil
}

func (s *IssueService) UploadInternalIssueAsset(issueID, userID, fileName string, fileSize int64, reader io.Reader, actor string) (*IssueAssetResponse, error) {
	_ = fileSize

	issue, err := s.issueRepo.FindByID(issueID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errs.ErrIssueNotFound
		}
		return nil, errs.ErrInternal
	}
	if _, err := s.projectRepo.FindByID(issue.ProjectID, userID); err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errs.ErrProjectNotFound
		}
		return nil, errs.ErrNotOwner
	}
	if issue.Source != model.IssueSourceInternal {
		return nil, errs.ErrIssueReadOnly
	}

	readFrom := reader
	if s.cfg.Upload.MaxFileSize > 0 {
		readFrom = io.LimitReader(reader, s.cfg.Upload.MaxFileSize+1)
	}

	head := make([]byte, issueAssetSniffBytes)
	headSize, err := io.ReadFull(readFrom, head)
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
		return nil, errs.ErrInternal
	}
	head = head[:headSize]
	if len(head) == 0 {
		return nil, errs.ErrInvalidParams
	}

	mimeType := http.DetectContentType(head)
	if !strings.HasPrefix(mimeType, "image/") {
		return nil, errs.ErrInvalidParams
	}

	assetID := uuid.NewString()
	storagePath := buildIssueAssetStoragePath(issue.ProjectID, issue.ID, assetID, fileName, mimeType)
	uploadReader := io.MultiReader(bytes.NewReader(head), readFrom)
	countedReader := &countingReader{reader: uploadReader}
	if err := s.storage.Save(storagePath, countedReader); err != nil {
		_ = s.storage.Delete(storagePath)
		return nil, errs.ErrInternal
	}
	if s.cfg.Upload.MaxFileSize > 0 && countedReader.n > s.cfg.Upload.MaxFileSize {
		_ = s.storage.Delete(storagePath)
		return nil, errs.ErrInvalidParams
	}

	asset := &model.IssueAsset{
		ID:              assetID,
		IssueID:         issue.ID,
		FileName:        normalizeIssueAssetFileName(fileName, mimeType),
		FilePath:        storagePath,
		MimeType:        mimeType,
		FileSize:        countedReader.n,
		Status:          model.IssueAssetStatusPending,
		CreatedByUserID: userID,
		CreatedAt:       time.Now().UTC(),
	}

	if err := s.assetRepo.Create(asset); err != nil {
		_ = s.storage.Delete(storagePath)
		return nil, errs.ErrInternal
	}

	s.logger.Info("issue asset uploaded",
		zap.String("action", "upload_issue_asset"),
		zap.String("issue_id", issueID),
		zap.String("user_id", userID),
		zap.String("actor", actor),
	)
	resp := toIssueAssetResponse(*asset)
	return &resp, nil
}

func (s *IssueService) UploadDraftInternalIssueAsset(projectID, userID, fileName string, fileSize int64, reader io.Reader) (*IssueAssetResponse, error) {
	_ = fileSize

	if _, err := s.projectRepo.FindByID(projectID, userID); err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errs.ErrProjectNotFound
		}
		return nil, errs.ErrInternal
	}

	asset, err := s.storeDraftIssueAsset(projectID, userID, fileName, reader)
	if err != nil {
		return nil, err
	}

	resp := toDraftIssueAssetResponse(*asset)
	return &resp, nil
}

func (s *IssueService) GetIssueAssetContent(assetID, userID string) (io.ReadCloser, string, int64, error) {
	asset, err := s.assetRepo.FindByID(assetID)
	if err != nil {
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, "", 0, errs.ErrInternal
		}
		draftAsset, draftErr := s.draftAssetRepo.FindByID(assetID)
		if draftErr != nil {
			if errors.Is(draftErr, gorm.ErrRecordNotFound) {
				return nil, "", 0, errs.ErrIssueAssetNotFound
			}
			return nil, "", 0, errs.ErrInternal
		}
		if _, err := s.projectRepo.FindByID(draftAsset.ProjectID, userID); err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil, "", 0, errs.ErrProjectNotFound
			}
			return nil, "", 0, errs.ErrNotOwner
		}
		reader, err := s.storage.Get(draftAsset.FilePath)
		if err != nil {
			return nil, "", 0, errs.ErrIssueAssetNotFound
		}
		return reader, draftAsset.MimeType, draftAsset.FileSize, nil
	}

	issue, err := s.issueRepo.FindByID(asset.IssueID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, "", 0, errs.ErrIssueNotFound
		}
		return nil, "", 0, errs.ErrInternal
	}
	if _, err := s.projectRepo.FindByID(issue.ProjectID, userID); err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, "", 0, errs.ErrProjectNotFound
		}
		return nil, "", 0, errs.ErrNotOwner
	}

	reader, err := s.storage.Get(asset.FilePath)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, "", 0, errs.ErrIssueAssetNotFound
		}
		return nil, "", 0, errs.ErrInternal
	}

	return reader, asset.MimeType, asset.FileSize, nil
}
