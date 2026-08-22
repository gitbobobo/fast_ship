package service

import (
	"bytes"
	"errors"
	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/godbobo/fast_ship/server/internal/pkg/errs"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"io"
	"net/http"
	neturl "net/url"
	"sort"
	"strings"
	"time"
)

func (s *IssueService) reconcileIssueAssets(issueID, body string) error {
	pathsToDelete, err := s.reconcileIssueAssetsTx(nil, issueID, body)
	if err != nil {
		return err
	}
	s.deleteIssueAssetFiles(pathsToDelete)
	return nil
}

func (s *IssueService) reconcileIssueAssetsTx(tx *gorm.DB, issueID, body string) ([]string, error) {
	if tx == nil {
		tx = s.issueRepo.DB()
	}

	assets, err := s.assetRepo.ListByIssueIDTx(tx, issueID)
	if err != nil {
		return nil, err
	}

	referencedIDs := extractIssueAssetIDs(body)
	if len(assets) == 0 {
		return nil, nil
	}

	idsToAttach := make([]string, 0)
	idsToDelete := make([]string, 0)
	for _, asset := range assets {
		if _, ok := referencedIDs[asset.ID]; ok {
			if asset.Status != model.IssueAssetStatusAttached {
				idsToAttach = append(idsToAttach, asset.ID)
			}
			continue
		}
		idsToDelete = append(idsToDelete, asset.ID)
	}

	if err := s.assetRepo.UpdateStatusByIssueIDAndIDsTx(tx, issueID, idsToAttach, model.IssueAssetStatusAttached); err != nil {
		return nil, err
	}
	pathsToDelete, err := s.deleteIssueAssetsTx(tx, issueID, idsToDelete)
	if err != nil {
		return nil, err
	}
	return pathsToDelete, nil
}

func (s *IssueService) deleteIssueAssets(issueID string, ids []string) error {
	pathsToDelete, err := s.deleteIssueAssetsTx(nil, issueID, ids)
	if err != nil {
		return err
	}
	s.deleteIssueAssetFiles(pathsToDelete)
	return nil
}

func (s *IssueService) deleteIssueAssetsTx(tx *gorm.DB, issueID string, ids []string) ([]string, error) {
	if len(ids) == 0 {
		return nil, nil
	}

	if tx == nil {
		tx = s.issueRepo.DB()
	}

	assets, err := s.assetRepo.ListByIssueIDTx(tx, issueID)
	if err != nil {
		return nil, err
	}

	pathsToDelete := make([]string, 0, len(ids))
	pathByID := make(map[string]string, len(assets))
	for _, asset := range assets {
		pathByID[asset.ID] = asset.FilePath
	}

	for _, id := range ids {
		if path := pathByID[id]; path != "" {
			pathsToDelete = append(pathsToDelete, path)
		}
	}

	if err := s.assetRepo.DeleteByIssueIDAndIDsTx(tx, issueID, ids); err != nil {
		return nil, err
	}
	return pathsToDelete, nil
}

func (s *IssueService) deleteIssueAssetFiles(paths []string) {
	for _, path := range paths {
		if path == "" {
			continue
		}
		_ = s.storage.Delete(path)
	}
}

func (s *IssueService) deleteDraftIssueAssets(projectID string, ids []string) error {
	if len(ids) == 0 {
		return nil
	}

	assets, err := s.draftAssetRepo.ListByProjectIDAndIDs(projectID, ids)
	if err != nil {
		return err
	}

	pathByID := make(map[string]string, len(assets))
	for _, asset := range assets {
		pathByID[asset.ID] = asset.FilePath
	}

	if err := s.draftAssetRepo.DeleteByProjectIDAndIDs(projectID, ids); err != nil {
		return err
	}
	for _, id := range ids {
		if path := pathByID[id]; path != "" {
			_ = s.storage.Delete(path)
		}
	}
	return nil
}

func (s *IssueService) syncIssueAssetsTx(tx *gorm.DB, projectID, issueID, body string) ([]string, error) {
	if err := s.validateIssueAssetReferencesTx(tx, projectID, issueID, body); err != nil {
		return nil, err
	}
	if err := s.attachDraftAssetsToIssueTx(tx, projectID, issueID, body); err != nil {
		return nil, err
	}
	return s.reconcileIssueAssetsTx(tx, issueID, body)
}

func (s *IssueService) attachDraftAssetsToIssueTx(tx *gorm.DB, projectID, issueID, body string) error {
	referencedIDs := extractIssueAssetIDs(body)
	if len(referencedIDs) == 0 {
		return nil
	}

	ids := make([]string, 0, len(referencedIDs))
	for id := range referencedIDs {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	if tx == nil {
		tx = s.issueRepo.DB()
	}

	existingAssets, err := s.assetRepo.ListByIssueIDTx(tx, issueID)
	if err != nil {
		return err
	}

	existingIDs := make(map[string]struct{}, len(existingAssets))
	for _, asset := range existingAssets {
		existingIDs[asset.ID] = struct{}{}
	}

	draftIDs := make([]string, 0, len(ids))
	for _, id := range ids {
		if _, ok := existingIDs[id]; ok {
			continue
		}
		draftIDs = append(draftIDs, id)
	}
	if len(draftIDs) == 0 {
		return nil
	}

	draftAssets, err := s.draftAssetRepo.ListByProjectIDAndIDsTx(tx, projectID, draftIDs)
	if err != nil {
		return err
	}
	if len(draftAssets) != len(draftIDs) {
		return errs.ErrInvalidParams
	}

	attachedIDs := make([]string, 0, len(draftAssets))
	for _, asset := range draftAssets {
		issueAsset := &model.IssueAsset{
			ID:              asset.ID,
			IssueID:         issueID,
			FileName:        asset.FileName,
			FilePath:        asset.FilePath,
			MimeType:        asset.MimeType,
			FileSize:        asset.FileSize,
			Status:          model.IssueAssetStatusAttached,
			CreatedByUserID: asset.CreatedByUserID,
			CreatedAt:       asset.CreatedAt,
		}
		if err := s.assetRepo.CreateTx(tx, issueAsset); err != nil {
			return err
		}
		attachedIDs = append(attachedIDs, asset.ID)
	}

	return s.draftAssetRepo.DeleteByProjectIDAndIDsTx(tx, projectID, attachedIDs)
}

func (s *IssueService) validateIssueAssetReferences(projectID, issueID, body string) error {
	return s.validateIssueAssetReferencesTx(nil, projectID, issueID, body)
}

func (s *IssueService) validateIssueAssetReferencesTx(tx *gorm.DB, projectID, issueID, body string) error {
	referencedIDs := extractIssueAssetIDs(body)
	if len(referencedIDs) == 0 {
		return nil
	}

	if tx == nil {
		tx = s.issueRepo.DB()
	}

	existingIDs := make(map[string]struct{}, len(referencedIDs))
	if strings.TrimSpace(issueID) != "" {
		issueAssets, err := s.assetRepo.ListByIssueIDTx(tx, issueID)
		if err != nil {
			return err
		}
		for _, asset := range issueAssets {
			existingIDs[asset.ID] = struct{}{}
		}
	}

	draftIDs := make([]string, 0, len(referencedIDs))
	for id := range referencedIDs {
		if _, ok := existingIDs[id]; ok {
			continue
		}
		draftIDs = append(draftIDs, id)
	}
	if len(draftIDs) == 0 {
		return nil
	}

	sort.Strings(draftIDs)
	draftAssets, err := s.draftAssetRepo.ListByProjectIDAndIDsTx(tx, projectID, draftIDs)
	if err != nil {
		return err
	}
	if len(draftAssets) != len(draftIDs) {
		return errs.ErrInvalidParams
	}
	return nil
}

func mapIssueAssetReferenceError(err error) error {
	if err == nil {
		return nil
	}

	var appErr *errs.AppError
	if errors.As(err, &appErr) {
		return appErr
	}
	return errs.ErrInternal
}

func resolveIssueAssetIDFromURL(value string) string {
	if strings.TrimSpace(value) == "" {
		return ""
	}
	parsed, err := neturl.Parse(value)
	if err == nil {
		value = parsed.Path
	}
	match := issueAssetContentPattern.FindStringSubmatch(value)
	if len(match) < 2 {
		return ""
	}
	return match[1]
}

type countingReader struct {
	reader io.Reader
	n      int64
}

func (r *countingReader) Read(p []byte) (int, error) {
	n, err := r.reader.Read(p)
	r.n += int64(n)
	return n, err
}

func (s *IssueService) storeDraftIssueAsset(projectID, userID, fileName string, reader io.Reader) (*model.IssueDraftAsset, error) {
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
	storagePath := buildIssueDraftAssetStoragePath(projectID, assetID, fileName, mimeType)
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

	asset := &model.IssueDraftAsset{
		ID:              assetID,
		ProjectID:       projectID,
		FileName:        normalizeIssueAssetFileName(fileName, mimeType),
		FilePath:        storagePath,
		MimeType:        mimeType,
		FileSize:        countedReader.n,
		CreatedByUserID: userID,
		CreatedAt:       time.Now().UTC(),
	}

	if err := s.draftAssetRepo.Create(asset); err != nil {
		_ = s.storage.Delete(storagePath)
		return nil, errs.ErrInternal
	}

	return asset, nil
}
