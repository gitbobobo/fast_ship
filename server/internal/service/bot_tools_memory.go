package service

import (
	"encoding/json"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/godbobo/fast_ship/server/internal/pkg/errs"
	"github.com/godbobo/fast_ship/server/internal/repository"
	"gorm.io/gorm"
)

// 记忆工具执行（INT-102）：全部同步、无需确认（只管机器人自己的记忆）。
// 写入在短事务内完成（文件更新、版本取号、50 版清理、FTS 同步同一事务）；
// operation_key 用服务端调用 ID 幂等。

func botIsMemoryWriteCall(call *model.BotToolCall) bool {
	if call == nil || call.ToolName != "fs_memory" {
		return false
	}
	switch call.Action {
	case "create", "replace", "append", "delete", "restore":
		return true
	}
	return false
}

const (
	botMemoryReadDefaultLimit = 4000
	botMemoryReadMaxLimit     = 16000
	botMemoryHistoryLimit     = 20
	botMemoryHistoryMaxLimit  = 50
	botMemorySearchMaxLimit   = 50
	botMemoryListMaxLimit     = 200
)

func (s *BotService) execMemoryTool(call *model.BotToolCall) (string, error) {
	var args botMemoryArgs
	if err := json.Unmarshal([]byte(call.ArgumentsJSON), &args); err != nil {
		return "", errs.ErrInvalidParams
	}
	if args.Action != "status" && args.Action != "list" {
		if err := botValidateMemoryPath(args.Path); err != nil {
			return "", err
		}
	}
	switch args.Action {
	case "list":
		return s.execMemoryList(call, &args)
	case "read":
		return s.execMemoryRead(call, &args)
	case "create", "replace", "append", "delete", "restore":
		return s.execMemoryWrite(call, &args)
	case "history":
		return s.execMemoryHistory(call, &args)
	}
	return "", errs.New(errs.ErrInvalidParams.Code, "不支持的 fs_memory 动作: "+args.Action)
}

func (s *BotService) execMemoryList(call *model.BotToolCall, args *botMemoryArgs) (string, error) {
	repo := repository.NewBotMemoryRepository(s.db)
	files, err := repo.ListFiles(call.BotID, strings.TrimSpace(args.Prefix), false, botMemoryListMaxLimit)
	if err != nil {
		return "", errs.ErrInternal
	}
	total, err := repo.CountFiles(call.BotID, strings.TrimSpace(args.Prefix), false)
	if err != nil {
		return "", errs.ErrInternal
	}
	items := make([]map[string]interface{}, 0, len(files))
	for i := range files {
		f := &files[i]
		items = append(items, map[string]interface{}{
			"path": f.Path, "chars": utf8.RuneCountInString(f.Content),
			"version": f.CurrentVersion, "updated_at": f.UpdatedAt.UTC().Format(time.RFC3339),
		})
	}
	return botMarshalResult(map[string]interface{}{"files": items, "total": total})
}

func (s *BotService) execMemoryRead(call *model.BotToolCall, args *botMemoryArgs) (string, error) {
	repo := repository.NewBotMemoryRepository(s.db)
	file, err := repo.FindFile(call.BotID, args.Path)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return "", errs.New(errs.ErrInvalidParams.Code, "记忆文件不存在: "+args.Path)
		}
		return "", errs.ErrInternal
	}
	var content string
	var version int64
	if args.Version != nil {
		if *args.Version <= 0 {
			return "", errs.ErrInvalidParams
		}
		var ver *model.BotMemoryVersion
		err := s.db.Transaction(func(tx *gorm.DB) error {
			var txErr error
			ver, txErr = repository.NewBotMemoryRepository(tx).FindVersionTx(tx, file.ID, *args.Version)
			return txErr
		})
		if err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return "", errs.New(errs.ErrInvalidParams.Code, "版本不存在")
			}
			return "", errs.ErrInternal
		}
		content = ver.Content
		version = ver.Version
	} else {
		if file.DeletedAt != nil {
			return "", errs.New(errs.ErrInvalidParams.Code, "记忆文件已删除：可用 read version=N 读旧版本或 restore 恢复")
		}
		content = file.Content
		version = file.CurrentVersion
	}
	offset := int64(args.Offset)
	if offset < 0 {
		offset = 0
	}
	limit := int64(args.Limit)
	if limit <= 0 {
		limit = botMemoryReadDefaultLimit
	}
	if limit > botMemoryReadMaxLimit {
		limit = botMemoryReadMaxLimit
	}
	runes := []rune(content)
	total := int64(len(runes))
	if offset > total {
		offset = total
	}
	end := offset + limit
	if end > total {
		end = total
	}
	return botMarshalResult(map[string]interface{}{
		"path": file.Path, "version": version, "total_chars": total,
		"offset": offset, "content": string(runes[offset:end]), "has_more": end < total,
	})
}

// execMemoryWrite 处理 create/replace/append/delete/restore：全部走
// WriteVersionTx（幂等 operation_key=调用 ID），返回路径/版本/字符数/超限标记。
func (s *BotService) execMemoryWrite(call *model.BotToolCall, args *botMemoryArgs) (string, error) {
	now := time.Now().UTC()
	sourceRunID := s.runIDOfCall(call)

	var op model.BotMemoryOperation
	switch args.Action {
	case "create":
		op = model.BotMemoryOpCreate
	case "replace":
		op = model.BotMemoryOpReplace
	case "append":
		op = model.BotMemoryOpAppend
	case "delete":
		op = model.BotMemoryOpDelete
	case "restore":
		op = model.BotMemoryOpRestore
	}

	var file *model.BotMemoryFile
	var version *model.BotMemoryVersion
	txErr := s.db.Transaction(func(tx *gorm.DB) error {
		txRepo := repository.NewBotMemoryRepository(tx)
		content := ""
		if args.Content != nil {
			content = *args.Content
		}
		// restore：先取目标旧版本正文，再以 restore 动作写新版本。
		if op == model.BotMemoryOpRestore {
			existing, err := txRepo.FindFileTx(tx, call.BotID, args.Path)
			if err != nil {
				if errors.Is(err, gorm.ErrRecordNotFound) {
					return errs.New(errs.ErrInvalidParams.Code, "记忆文件不存在: "+args.Path)
				}
				return err
			}
			ver, err := txRepo.FindVersionTx(tx, existing.ID, *args.Version)
			if err != nil {
				if errors.Is(err, gorm.ErrRecordNotFound) {
					return errs.New(errs.ErrInvalidParams.Code, "版本不存在")
				}
				return err
			}
			content = ver.Content
		}
		// append：读到旧正文在同一事务内拼接。
		if op == model.BotMemoryOpAppend {
			existing, err := txRepo.FindFileTx(tx, call.BotID, args.Path)
			if err != nil {
				if errors.Is(err, gorm.ErrRecordNotFound) {
					return errs.New(errs.ErrInvalidParams.Code, "记忆文件不存在: "+args.Path)
				}
				return err
			}
			if existing.DeletedAt != nil {
				return errs.New(errs.ErrInvalidParams.Code, "记忆文件已删除")
			}
			content = existing.Content + content
		}
		deleted := op == model.BotMemoryOpDelete
		if deleted {
			existing, err := txRepo.FindFileTx(tx, call.BotID, args.Path)
			if err == nil {
				content = existing.Content // 墓碑记删除前正文
			}
		}
		f, v, err := txRepo.WriteVersionTx(tx, call.BotID, args.Path, op, content, deleted,
			args.ExpectedVersion, model.BotMemoryWriterMain, sourceRunID, "", call.ID, now)
		if err != nil {
			return err
		}
		file, version = f, v
		return nil
	})
	if txErr != nil {
		switch {
		case errors.Is(txErr, repository.ErrMemoryFileExists):
			return "", errs.New(errs.ErrInvalidParams.Code, "记忆文件已存在：用 replace/append 或先 delete")
		case errors.Is(txErr, repository.ErrMemoryFileDeleted):
			return "", errs.New(errs.ErrInvalidParams.Code, "记忆文件已删除：用 create 重建或 restore 恢复")
		case errors.Is(txErr, repository.ErrMemoryFileNotFound):
			return "", errs.New(errs.ErrInvalidParams.Code, "记忆文件不存在: "+args.Path)
		case errors.Is(txErr, repository.ErrMemoryVersionConflict):
			return "", errs.New(errs.ErrBotCallStateConflict.Code, "expected_version 与当前版本不符，请先 read 最新版本")
		default:
			if appErr, ok := txErr.(*errs.AppError); ok {
				return "", appErr
			}
			return "", errs.ErrInternal
		}
	}

	chars := int64(utf8.RuneCountInString(file.Content))
	limit := s.botMemoryPathLimit(file.Path)
	out := map[string]interface{}{
		"ok": true, "path": file.Path, "version": file.CurrentVersion, "chars": chars,
		"note": "记忆已保存；请向用户简短告知已记住的要点",
	}
	if version != nil {
		out["operation"] = string(version.Operation)
	}
	if limit > 0 {
		out["injection_limit"] = limit
		out["over_limit"] = chars > limit
	}
	return botMarshalResult(out)
}

func (s *BotService) execMemoryHistory(call *model.BotToolCall, args *botMemoryArgs) (string, error) {
	file, err := repository.NewBotMemoryRepository(s.db).FindFile(call.BotID, args.Path)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return "", errs.New(errs.ErrInvalidParams.Code, "记忆文件不存在: "+args.Path)
		}
		return "", errs.ErrInternal
	}
	limit := args.Limit
	if limit <= 0 {
		limit = botMemoryHistoryLimit
	}
	if limit > botMemoryHistoryMaxLimit {
		limit = botMemoryHistoryMaxLimit
	}
	before := int64(0)
	if args.Version != nil && *args.Version > 0 {
		before = *args.Version
	}
	var rows []model.BotMemoryVersion
	txErr := s.db.Transaction(func(tx *gorm.DB) error {
		var txErr error
		rows, txErr = repository.NewBotMemoryRepository(tx).ListVersionsTx(tx, file.ID, before, limit+1)
		return txErr
	})
	if txErr != nil {
		return "", errs.ErrInternal
	}
	hasMore := len(rows) > limit
	if hasMore {
		rows = rows[:limit]
	}
	items := make([]map[string]interface{}, 0, len(rows))
	for i := range rows {
		v := &rows[i]
		items = append(items, map[string]interface{}{
			"version": v.Version, "operation": string(v.Operation),
			"writer_kind": string(v.WriterKind), "deleted": v.Deleted,
			"chars":      utf8.RuneCountInString(v.Content),
			"created_at": v.CreatedAt.UTC().Format(time.RFC3339),
		})
	}
	out := map[string]interface{}{
		"path": file.Path, "current_version": file.CurrentVersion,
		"deleted": file.DeletedAt != nil, "versions": items, "has_more": hasMore,
	}
	return botMarshalResult(out)
}

func (s *BotService) execMemorySearch(call *model.BotToolCall) (string, error) {
	var args botMemorySearchArgs
	if err := json.Unmarshal([]byte(call.ArgumentsJSON), &args); err != nil {
		return "", errs.ErrInvalidParams
	}
	query := strings.TrimSpace(args.Query)
	if query == "" {
		return "", errs.New(errs.ErrInvalidParams.Code, "query 必填")
	}
	limit := args.Limit
	if limit <= 0 {
		limit = 10
	}
	if limit > botMemorySearchMaxLimit {
		limit = botMemorySearchMaxLimit
	}
	offset := args.Offset
	if offset < 0 {
		offset = 0
	}
	hits, err := repository.NewBotMemoryRepository(s.db).SearchFTS(
		call.BotID, query, strings.TrimSpace(args.PathPrefix), offset, limit)
	if err != nil {
		return "", errs.ErrInternal
	}
	items := make([]map[string]interface{}, 0, len(hits))
	var total int64 = int64(len(hits))
	for i := range hits {
		if i == 0 {
			total = hits[i].Total
		}
		// 命中行的当前版本：FTS 表没存版本号，回查文件表。
		var ver int64
		if f, ferr := repository.NewBotMemoryRepository(s.db).FindFile(call.BotID, hits[i].Path); ferr == nil {
			ver = f.CurrentVersion
		}
		items = append(items, map[string]interface{}{
			"path": hits[i].Path, "version": ver,
			"fragment": hits[i].Fragment, "rank": hits[i].Rank,
		})
	}
	return botMarshalResult(map[string]interface{}{
		"hits": items, "total": total, "offset": offset, "limit": limit,
		"note": "正文用 fs_memory action=read path=<path> 取回",
	})
}
