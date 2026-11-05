package service

import (
	"context"
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
// operation_key 用服务端调用 ID 幂等。动作注册表 spec 组见文件末尾（INT-195）。

func botIsMemoryWriteCall(call *model.BotToolCall) bool {
	if call == nil {
		return false
	}
	spec := botActionSpecOf(call.ToolName, call.Action)
	return spec != nil && spec.MemoryWrite
}

const (
	botMemoryReadDefaultLimit = 4000
	botMemoryReadMaxLimit     = 16000
	botMemoryHistoryLimit     = 20
	botMemoryHistoryMaxLimit  = 50
	botMemorySearchMaxLimit   = 50
	botMemoryListMaxLimit     = 200
)

// botMemoryExecArgs 解出 fs_memory 执行参数；needPath 的动作先做路径校验
// （status/list 无路径概念，与拆分前 execMemoryTool 的免检口径一致）。
func botMemoryExecArgs(call *model.BotToolCall, needPath bool) (*botMemoryArgs, error) {
	var args botMemoryArgs
	if err := json.Unmarshal([]byte(call.ArgumentsJSON), &args); err != nil {
		return nil, errs.ErrInvalidParams
	}
	if needPath {
		if err := botValidateMemoryPath(args.Path); err != nil {
			return nil, err
		}
	}
	return &args, nil
}

// fs_memory 各动作的入口（注册表 SyncExec 指向这些方法表达式）：解析参数后
// 转交执行体；写动作共享 execMemoryWrite 的事务实现。

// execCapabilityStatusAction 是 fs_memory/status 与 fs_schedule/status（v2
// 墓碑动作）的注册表入口：能力查询的 available 按本 run 冻结的工具集水位
// 判定（与会话工具面同一事实源），不读 bots 行活值。bot 由 executeSyncCall
// 透传；nil 属编程错误（生产路径不存在），防御性拒绝而不是解引用 panic
// （面板审查 R1：旧聚合兼容入口曾把 nil bot 送进这里）。
func (s *BotService) execCapabilityStatusAction(bot *model.Bot, _ context.Context, call *model.BotToolCall) (string, error) {
	if bot == nil {
		return "", errs.New(errs.ErrInvalidParams.Code, "能力查询需要本 run 的工具集水位: "+call.ToolName+"/"+call.Action)
	}
	return s.execCapabilityStatus(call, bot.ToolsetVersion)
}

func (s *BotService) execMemoryListAction(_ *model.Bot, _ context.Context, call *model.BotToolCall) (string, error) {
	args, err := botMemoryExecArgs(call, false)
	if err != nil {
		return "", err
	}
	return s.execMemoryList(call, args)
}

func (s *BotService) execMemoryReadAction(_ *model.Bot, _ context.Context, call *model.BotToolCall) (string, error) {
	args, err := botMemoryExecArgs(call, true)
	if err != nil {
		return "", err
	}
	return s.execMemoryRead(call, args)
}

func (s *BotService) execMemoryCreateAction(_ *model.Bot, _ context.Context, call *model.BotToolCall) (string, error) {
	args, err := botMemoryExecArgs(call, true)
	if err != nil {
		return "", err
	}
	return s.execMemoryWrite(call, args)
}

func (s *BotService) execMemoryReplaceAction(_ *model.Bot, _ context.Context, call *model.BotToolCall) (string, error) {
	args, err := botMemoryExecArgs(call, true)
	if err != nil {
		return "", err
	}
	return s.execMemoryWrite(call, args)
}

func (s *BotService) execMemoryAppendAction(_ *model.Bot, _ context.Context, call *model.BotToolCall) (string, error) {
	args, err := botMemoryExecArgs(call, true)
	if err != nil {
		return "", err
	}
	return s.execMemoryWrite(call, args)
}

func (s *BotService) execMemoryDeleteAction(_ *model.Bot, _ context.Context, call *model.BotToolCall) (string, error) {
	args, err := botMemoryExecArgs(call, true)
	if err != nil {
		return "", err
	}
	return s.execMemoryWrite(call, args)
}

func (s *BotService) execMemoryHistoryAction(_ *model.Bot, _ context.Context, call *model.BotToolCall) (string, error) {
	args, err := botMemoryExecArgs(call, true)
	if err != nil {
		return "", err
	}
	return s.execMemoryHistory(call, args)
}

func (s *BotService) execMemoryRestoreAction(_ *model.Bot, _ context.Context, call *model.BotToolCall) (string, error) {
	args, err := botMemoryExecArgs(call, true)
	if err != nil {
		return "", err
	}
	return s.execMemoryWrite(call, args)
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
	// version 合法性先于文件查找：缺失路径 + version<=0 与存在路径同判
	// 非法参数，不得落进「记忆文件不存在」。
	if args.Version != nil && *args.Version <= 0 {
		return "", errs.ErrInvalidParams
	}
	file, err := repo.FindFile(call.BotID, args.Path)
	if err != nil {
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return "", errs.ErrInternal
		}
		// 只读对「不存在」返回空结果而不是报错（INT-124）：先读后写的探测
		// 不再产生红色失败。显式读历史版本仍是非法操作，保持报错。
		if args.Version != nil {
			return "", errs.New(errs.ErrInvalidParams.Code, "记忆文件不存在: "+args.Path)
		}
		return botMarshalResult(map[string]interface{}{
			"path": args.Path, "exists": false, "deleted": false,
			"content": "", "version": int64(0), "total_chars": int64(0),
			"offset": int64(0), "has_more": false,
		})
	}
	var content string
	var version int64
	// deleted 描述被读对象自身：head 读取永远是存活文件（墓碑已提前返回），
	// 显式历史版本则取该版本自己的删除标记。
	deleted := false
	if args.Version != nil {
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
		deleted = ver.Deleted
	} else {
		if file.DeletedAt != nil {
			// 墓碑不算存在：exists=false + deleted=true，不吐墓碑正文。
			return botMarshalResult(map[string]interface{}{
				"path": file.Path, "exists": false, "deleted": true,
				"content": "", "version": int64(0), "total_chars": int64(0),
				"offset": int64(0), "has_more": false,
			})
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
		// exists 与 head/history 同义：当前是否有存活文件；deleted 是被读
		// 版本自身的删除标记（历史版本行）。
		"path": file.Path, "exists": file.DeletedAt == nil, "deleted": deleted,
		"version": version, "total_chars": total,
		"offset": offset, "content": string(runes[offset:end]), "has_more": end < total,
	})
}

// execMemoryWrite 处理 create/replace/append/delete/restore：全部走
// WriteVersionTx（幂等 operation_key=调用 ID），返回路径/版本/字符数/超限标记。
func (s *BotService) execMemoryWrite(call *model.BotToolCall, args *botMemoryArgs) (string, error) {
	now := time.Now().UTC()
	sourceRunID := s.runIDOfCall(call)
	// 写入者按所在 run 的会话判别（INT-139 R8）：定时任务运行里写的记忆
	// 记 scheduled，不再一律记成 main。
	writer := model.BotMemoryWriterMain
	if s.runs.SessionKindOf(sourceRunID) == model.BotSessionKindSchedule {
		writer = model.BotMemoryWriterScheduled
	}

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
			args.ExpectedVersion, writer, sourceRunID, "", call.ID, now)
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
			// 只读对「不存在」返回空结果而不是报错（INT-124）。
			return botMarshalResult(map[string]interface{}{
				"path": args.Path, "exists": false, "current_version": int64(0),
				"deleted": false, "versions": []interface{}{}, "has_more": false,
			})
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
	// exists 与 read 同义，表示「有存活文件」：墓碑记 exists=false +
	// deleted=true，历史版本仍照常列出（INT-124）。
	out := map[string]interface{}{
		"path": file.Path, "exists": file.DeletedAt == nil, "current_version": file.CurrentVersion,
		"deleted": file.DeletedAt != nil, "versions": items, "has_more": hasMore,
	}
	return botMarshalResult(out)
}

func (s *BotService) execMemorySearch(_ *model.Bot, _ context.Context, call *model.BotToolCall) (string, error) {
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

// botMemoryActionSpecs 是 fs_memory 的动作声明（INT-195）：写操作全部同步、
// 无需确认（只管机器人自己的记忆）；平台自有回执（元数据与写 ack）豁免围栏，
// 带正文的 read 与检索结果不豁免、照常进围栏。status 是能力查询（SyncExec
// 消费 bot.ToolsetVersion）。
func botMemoryActionSpecs() []*botActionSpec {
	return []*botActionSpec{
		{
			Tool: "fs_memory", Action: "status",
			Description: "查询记忆能力状态与注入上限",
			Mode:        model.BotExecutionSync, ReadOnly: true, FenceExempt: true,
			SyncExec: (*BotService).execCapabilityStatusAction,
		},
		{
			Tool: "fs_memory", Action: "list",
			Description: "列出记忆文件元数据（prefix 可选）",
			Mode:        model.BotExecutionSync, ReadOnly: true, FenceExempt: true,
			SyncExec: (*BotService).execMemoryListAction,
		},
		{
			Tool: "fs_memory", Action: "read",
			Description: "读取当前正文或指定版本（offset/limit 按 Unicode 字符）",
			Mode:        model.BotExecutionSync, ReadOnly: true,
			Required: req("path"),
			SyncExec: (*BotService).execMemoryReadAction,
		},
		{
			Tool: "fs_memory", Action: "create",
			Description: "新建记忆文件（已存在则冲突；墓碑可重建）",
			// 同步写、不经确认：只管机器人自己的记忆，expected_version 乐观锁
			// 控并发，误写可靠版本历史恢复。
			Mode: model.BotExecutionSync, Confirmation: confirmNever, MemoryWrite: true, FenceExempt: true,
			Required: append(req("path"), reqAllowEmpty("content")...),
			SyncExec: (*BotService).execMemoryCreateAction,
		},
		{
			Tool: "fs_memory", Action: "replace",
			Description: "整体替换正文（需 expected_version）",
			Mode:        model.BotExecutionSync, Confirmation: confirmNever, MemoryWrite: true, FenceExempt: true,
			Required: append(append(req("path"), reqAllowEmpty("content")...), req("expected_version")...),
			SyncExec: (*BotService).execMemoryReplaceAction,
		},
		{
			Tool: "fs_memory", Action: "append",
			Description: "追加正文（需 expected_version）",
			Mode:        model.BotExecutionSync, Confirmation: confirmNever, MemoryWrite: true, FenceExempt: true,
			Required: append(append(req("path"), reqAllowEmpty("content")...), req("expected_version")...),
			SyncExec: (*BotService).execMemoryAppendAction,
		},
		{
			Tool: "fs_memory", Action: "delete",
			Description: "删除文件（写墓碑版本，历史保留；需 expected_version）",
			Mode:        model.BotExecutionSync, Confirmation: confirmNever, MemoryWrite: true, FenceExempt: true,
			Required: req("path", "expected_version"),
			SyncExec: (*BotService).execMemoryDeleteAction,
		},
		{
			Tool: "fs_memory", Action: "history",
			Description: "查看最近版本元数据",
			Mode:        model.BotExecutionSync, ReadOnly: true, FenceExempt: true,
			Required: req("path"),
			SyncExec: (*BotService).execMemoryHistoryAction,
		},
		{
			Tool: "fs_memory", Action: "restore",
			Description: "把指定旧版本恢复为新当前版本（需 expected_version）",
			Mode:        model.BotExecutionSync, Confirmation: confirmNever, MemoryWrite: true, FenceExempt: true,
			Required: req("path", "version", "expected_version"),
			SyncExec: (*BotService).execMemoryRestoreAction,
		},
	}
}

// botMemorySearchActionSpecs 是 fs_memory_search 的动作声明：命中片段可能是
// 模型抄进来的外部文本，不豁免围栏。
func botMemorySearchActionSpecs() []*botActionSpec {
	return []*botActionSpec{
		{
			Tool: "fs_memory_search", Action: "search",
			Description: "关键词全文检索本机器人记忆",
			Mode:        model.BotExecutionSync, ReadOnly: true,
			Required: req("query"),
			SyncExec: (*BotService).execMemorySearch,
		},
	}
}
