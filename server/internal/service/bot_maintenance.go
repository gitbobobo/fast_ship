package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/godbobo/fast_ship/server/internal/pkg/errs"
	"github.com/godbobo/fast_ship/server/internal/repository"
	"github.com/google/uuid"
	"github.com/openai/openai-go/v2"
	"gorm.io/gorm"
)

// 维护模型请求（INT-102）：摘要 / 静默存档 / 夜间整理的每次实际模型调用
// 单独记 bot_maintenance_requests，不混入主线 bot_model_requests。

type openaiMessage struct {
	Role    string
	Content string
}

func (s *BotService) auxModelRequest(ctx context.Context, bot *model.Bot, maintRun *model.BotMaintenanceRun,
	purpose model.BotMaintenancePurpose, modelName string, msgs []openaiMessage, maxOut int64) (*botModelReply, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	apiKey, err := s.decryptAPIKey(bot)
	if err != nil {
		return nil, err
	}
	params := make([]openai.ChatCompletionMessageParamUnion, 0, len(msgs))
	for _, m := range msgs {
		switch m.Role {
		case "system":
			params = append(params, openai.SystemMessage(m.Content))
		default:
			params = append(params, openai.UserMessage(m.Content))
		}
	}
	prefixRaw, _ := json.Marshal(params)
	prefixSum := sha256.Sum256(prefixRaw)

	var req *model.BotMaintenanceRequest
	startedAt := s.now()
	err = s.db.Transaction(func(tx *gorm.DB) error {
		repo := repository.NewBotMaintenanceRepository(tx)
		ordinal, txErr := repo.NextMaintenanceOrdinalTx(tx, maintRun.ID)
		if txErr != nil {
			return txErr
		}
		req = &model.BotMaintenanceRequest{
			ID:               uuid.NewString(),
			MaintenanceRunID: maintRun.ID,
			Ordinal:          ordinal,
			Purpose:          purpose,
			Model:            modelName,
			Status:           model.BotModelRequestRunning,
			PrefixHash:       hex.EncodeToString(prefixSum[:]),
			ToolsHash:        "",
			ToolsetVersion:   bot.ToolsetVersion,
			StartedAt:        startedAt,
		}
		return repo.CreateRequestTx(tx, req)
	})
	if err != nil {
		return nil, errs.ErrInternal
	}

	client := botNewClient(bot.BaseURL, apiKey)
	callCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	reply, callErr := botStreamChat(callCtx, &client, botStreamRequest{
		Messages:            params,
		Model:               modelName,
		MaxCompletionTokens: maxOut,
	}, nil)

	cols := map[string]interface{}{"finished_at": s.now()}
	if callErr != nil {
		cols["status"] = model.BotModelRequestFailed
		cols["error_code"] = "maintenance_request_failed"
		cols["error_message"] = botErrMessage(callErr)
	} else {
		cols["status"] = model.BotModelRequestSucceeded
		cols["provider_request_id"] = reply.ProviderRequestID
		if reply.UsageComplete {
			cols["usage_complete"] = true
			cols["prompt_tokens"] = reply.PromptTokens
			cols["completion_tokens"] = reply.CompletionTokens
			cols["total_tokens"] = reply.TotalTokens
			cols["cached_tokens"] = reply.CachedTokens
		}
	}
	_, _ = repository.NewBotMaintenanceRepository(s.db).UpdateRequestColumns(req.ID, nil, cols)
	return reply, callErr
}

// ---------- 夜间整理 ----------

type botNightlyConsumed struct {
	Path    string `json:"path"`
	Version int64  `json:"version"`
}

type botNightlyResult struct {
	Consolidated bool                 `json:"consolidated"`
	FilesWritten []string             `json:"files_written,omitempty"`
	Consumed     []botNightlyConsumed `json:"consumed,omitempty"`
	Note         string               `json:"note,omitempty"`
}

type botNightlyFileVer struct {
	Path    string `json:"path"`
	Version int64  `json:"version"`
	Exists  bool   `json:"exists"`
}

func (s *BotService) ensureMaintWorker() {
	s.maintOnce.Do(func() {
		s.maintJobs = make(chan botMaintJob, 64)
		go s.maintWorkerLoop()
	})
}

func (s *BotService) maintWorkerLoop() {
	for job := range s.maintJobs {
		s.runNightlyForBot(job.botID, job.triggerKey)
	}
}

func (s *BotService) nightlyLoop() {
	ticker := time.NewTicker(60 * time.Second)
	defer ticker.Stop()
	for range ticker.C {
		s.scanNightlyOnce()
	}
}

func (s *BotService) scanNightlyOnce() {
	s.ensureMaintWorker()
	ids, err := repository.NewBotMaintenanceRepository(s.db).ListBotIDs()
	if err != nil {
		return
	}
	loc := s.cfg.Bots.Memory.Location()
	hour, minute := s.cfg.Bots.Memory.NightlyClock()
	now := s.now().In(loc)
	todayClock := time.Date(now.Year(), now.Month(), now.Day(), hour, minute, 0, 0, loc)
	if now.Before(todayClock) {
		return
	}
	triggerKey := "nightly:" + now.Format("2006-01-02")
	for _, botID := range ids {
		select {
		case s.maintJobs <- botMaintJob{botID: botID, triggerKey: triggerKey}:
		default:
		}
	}
}

func (s *BotService) runNightlyForBot(botID, triggerKey string) {
	bot, err := s.bots.Get(botID)
	if err != nil {
		return
	}
	now := s.now()
	snap, _ := json.Marshal(map[string]interface{}{
		"nightly_model": s.nightlyModelOf(bot), "timezone": s.cfg.Bots.Memory.Timezone,
	})
	maintRun := &model.BotMaintenanceRun{
		ID:                 uuid.NewString(),
		BotID:              botID,
		Kind:               model.BotMaintenanceNightly,
		TriggerKey:         triggerKey,
		Status:             model.BotMaintenanceRunning,
		ConfigSnapshotJSON: string(snap),
		InputSnapshotJSON:  "{}",
		ResultJSON:         "{}",
		CreatedAt:          now,
		StartedAt:          &now,
	}
	var created bool
	txErr := s.db.Transaction(func(tx *gorm.DB) error {
		var txErr error
		maintRun, created, txErr = repository.NewBotMaintenanceRepository(tx).CreateOrFindTx(tx, maintRun)
		return txErr
	})
	if txErr != nil || !created {
		return
	}
	s.RunMemoryConsolidation(context.Background(), bot, maintRun)
}

func (s *BotService) nightlyModelOf(bot *model.Bot) string {
	if m := strings.TrimSpace(s.cfg.Bots.Memory.NightlyModel); m != "" {
		return m
	}
	return bot.Model
}

type botNightlyPayload struct {
	MemoryMD string `json:"memory_md"`
	UserMD   string `json:"user_md"`
	Sources  []struct {
		Path    string `json:"path"`
		Version int64  `json:"version"`
	} `json:"sources"`
	Note string `json:"note"`
}

type botMemorySnap struct {
	Path    string
	Version int64
	Content string
	Exists  bool
}

func (s *BotService) RunMemoryConsolidation(ctx context.Context, bot *model.Bot, maintRun *model.BotMaintenanceRun) {
	if ctx == nil {
		ctx = context.Background()
	}
	finish := func(status model.BotMaintenanceStatus, result *botNightlyResult, errCode, errMsg string) {
		cols := repository.MarkFinishedCols(status, s.now(), errCode, errMsg)
		if result != nil {
			raw, _ := json.Marshal(result)
			cols["result_json"] = string(raw)
		}
		_, _ = repository.NewBotMaintenanceRepository(s.db).UpdateColumns(maintRun.ID,
			map[string]interface{}{"finished_at": nil}, cols)
	}

	var user, memory botMemorySnap
	var dailies []botMemorySnap
	prevConsumed := s.lastNightlyConsumed(bot.ID)
	readErr := s.db.Transaction(func(tx *gorm.DB) error {
		repo := repository.NewBotMemoryRepository(tx)
		resident, err := repo.ListResidentTx(tx, bot.ID)
		if err != nil {
			return err
		}
		if f := resident[botMemoryPathUser]; f != nil {
			user = botMemorySnap{Path: f.Path, Version: f.CurrentVersion, Content: f.Content, Exists: true}
		} else {
			user = botMemorySnap{Path: botMemoryPathUser}
		}
		if f := resident[botMemoryPathMemory]; f != nil {
			memory = botMemorySnap{Path: f.Path, Version: f.CurrentVersion, Content: f.Content, Exists: true}
		} else {
			memory = botMemorySnap{Path: botMemoryPathMemory}
		}
		rows, err := repo.ListDailyFilesTx(tx, bot.ID)
		if err != nil {
			return err
		}
		for i := range rows {
			if ver, ok := prevConsumed[rows[i].Path]; ok && rows[i].CurrentVersion <= ver {
				continue
			}
			dailies = append(dailies, botMemorySnap{
				Path: rows[i].Path, Version: rows[i].CurrentVersion,
				Content: rows[i].Content, Exists: true,
			})
		}
		return nil
	})
	if readErr != nil {
		finish(model.BotMaintenanceFailed, nil, "read_failed", "读取记忆失败")
		return
	}

	inSnap := []botNightlyFileVer{
		{Path: user.Path, Version: user.Version, Exists: user.Exists},
		{Path: memory.Path, Version: memory.Version, Exists: memory.Exists},
	}
	for _, d := range dailies {
		inSnap = append(inSnap, botNightlyFileVer{Path: d.Path, Version: d.Version, Exists: true})
	}
	inRaw, _ := json.Marshal(map[string]interface{}{"files": inSnap})
	_, _ = repository.NewBotMaintenanceRepository(s.db).UpdateColumns(maintRun.ID, nil,
		map[string]interface{}{"input_snapshot_json": string(inRaw)})
	if len(dailies) == 0 {
		finish(model.BotMaintenanceSucceeded, &botNightlyResult{Note: "nothing_to_consume", Consumed: []botNightlyConsumed{}}, "", "")
		return
	}

	modelName := s.nightlyModelOf(bot)
	auxWindow := s.inputBudgetFor(bot, modelName)
	userLimit := s.cfg.Bots.Memory.UserMaxCharsV()
	memLimit := s.cfg.Bots.Memory.MemoryMaxCharsV()
	sys := "你是记忆整理器。只输出 JSON。模型不能指定路径去改 daily，也不能执行工具。"
	instr := "整理长期记忆：只合并、去重、标记已被取代的条目，不轻易删除事实；" +
		"外部未确认信息不自动提升。只允许产出 USER.md 与 MEMORY.md 候选正文。" +
		"不要改写 daily 原文。以 JSON 输出：" +
		`{"user_md":"...","memory_md":"...","sources":[{"path":"daily/YYYY-MM-DD.md","version":N}],"note":"..."}` +
		"\n正文必须在上限内（USER.md ≤ " + itoa64(userLimit) +
		" 字符，MEMORY.md ≤ " + itoa64(memLimit) +
		" 字符）；截断半句不算成功；不要发明新事实；不要输出机密。\n\n"

	var payload botNightlyPayload
	payload.UserMD = user.Content
	payload.MemoryMD = memory.Content
	remaining := dailies
	for len(remaining) > 0 {
		if ctx.Err() != nil {
			finish(model.BotMaintenanceFailed, nil, "stop_requested", "整理被中断")
			return
		}
		resident := "=== USER.md ===\n" + payload.UserMD + "\n=== MEMORY.md ===\n" + payload.MemoryMD + "\n"
		wrap := botEstimateTokens(sys) + botEstimateTokens(instr) + botEstimateTokens(resident) + 64
		outBudget := int64(4096)
		room := auxWindow - wrap - outBudget
		if room < 128 {
			outBudget = auxWindow - wrap - 128
			if outBudget < 256 {
				finish(model.BotMaintenanceFailed, nil, "over_window", "整理输入超出辅助窗口")
				return
			}
			room = auxWindow - wrap - outBudget
		}
		batch, rest := takeNightlyBatch(remaining, room)
		if len(batch) == 0 {
			finish(model.BotMaintenanceFailed, nil, "over_window", "单日笔记无法装入辅助窗口")
			return
		}
		remaining = rest
		var input strings.Builder
		input.WriteString(instr)
		input.WriteString(resident)
		batchAllowed := map[string]int64{}
		for _, d := range batch {
			input.WriteString(botNightlyFileHeader(d))
			input.WriteString(d.Content + "\n")
			batchAllowed[d.Path] = d.Version
		}
		if botEstimateTokens(sys)+botEstimateTokens(input.String())+outBudget > auxWindow {
			finish(model.BotMaintenanceFailed, nil, "over_window", "整理请求超出辅助窗口")
			return
		}
		reply, callErr := s.auxModelRequest(ctx, bot, maintRun, model.BotPurposeNightly,
			modelName, []openaiMessage{
				{Role: "system", Content: sys},
				{Role: "user", Content: input.String()},
			}, outBudget)
		if callErr != nil {
			finish(model.BotMaintenanceFailed, nil, "model_request_failed", botErrMessage(callErr))
			return
		}
		if strings.EqualFold(reply.FinishReason, "length") {
			finish(model.BotMaintenanceFailed, nil, "truncated", "整理输出被截断")
			return
		}
		rawOut := strings.TrimSpace(reply.Text)
		if rawOut == "" || rawOut == "{}" {
			finish(model.BotMaintenanceFailed, nil, "bad_output", "整理输出不完整")
			return
		}
		var next botNightlyPayload
		if json.Unmarshal([]byte(rawOut), &next) != nil {
			finish(model.BotMaintenanceFailed, nil, "bad_output", "整理输出不是合法 JSON")
			return
		}
		if strings.TrimSpace(next.UserMD) == "" && strings.TrimSpace(next.MemoryMD) == "" {
			finish(model.BotMaintenanceFailed, nil, "bad_output", "整理输出缺少正文")
			return
		}
		if len(next.Sources) == 0 {
			finish(model.BotMaintenanceFailed, nil, "bad_output", "整理输出未声明本批来源")
			return
		}
		declared := map[string]bool{}
		for _, src := range next.Sources {
			ver, ok := batchAllowed[src.Path]
			if !ok {
				finish(model.BotMaintenanceFailed, nil, "bad_source", "来源不在本批输入")
				return
			}
			if src.Version != ver {
				finish(model.BotMaintenanceFailed, nil, "bad_source", "来源版本与输入快照不一致")
				return
			}
			declared[src.Path] = true
		}
		// 逐片核验覆盖：本批输入的每个来源（含同一 daily 的尾片）都必须
		// 被本批声明处理，全部片段处理完才允许消费该版本。
		for p := range batchAllowed {
			if !declared[p] {
				finish(model.BotMaintenanceFailed, nil, "bad_source", "本批来源未全部声明处理")
				return
			}
		}
		if strings.TrimSpace(next.UserMD) != "" {
			payload.UserMD = next.UserMD
		}
		if strings.TrimSpace(next.MemoryMD) != "" {
			payload.MemoryMD = next.MemoryMD
		}
		payload.Sources = append(payload.Sources, next.Sources...)
		payload.Note = next.Note
	}

	if utf8.RuneCountInString(payload.UserMD) > int(userLimit) || utf8.RuneCountInString(payload.MemoryMD) > int(memLimit) {
		finish(model.BotMaintenanceFailed, nil, "over_limit", "整理输出超过常驻文件上限")
		return
	}
	if botLooksTruncatedMidSentence(payload.MemoryMD) || botLooksTruncatedMidSentence(payload.UserMD) {
		finish(model.BotMaintenanceFailed, nil, "truncated", "整理输出疑似截断半句")
		return
	}

	allowedDaily := map[string]int64{}
	for _, d := range dailies {
		allowedDaily[d.Path] = d.Version
	}
	consumed := make([]botNightlyConsumed, 0, len(dailies))
	seenDaily := map[string]bool{}
	for _, src := range payload.Sources {
		if err := botValidateMemoryPath(src.Path); err != nil || !strings.HasPrefix(src.Path, botMemoryDailyPref) {
			finish(model.BotMaintenanceFailed, nil, "bad_source", "来源路径不在 daily 白名单")
			return
		}
		ver, ok := allowedDaily[src.Path]
		if !ok {
			finish(model.BotMaintenanceFailed, nil, "bad_source", "来源不在本次输入")
			return
		}
		if src.Version != ver {
			finish(model.BotMaintenanceFailed, nil, "bad_source", "来源版本与输入快照不一致")
			return
		}
		if !seenDaily[src.Path] {
			seenDaily[src.Path] = true
			consumed = append(consumed, botNightlyConsumed{Path: src.Path, Version: ver})
		}
	}
	if len(consumed) == 0 {
		finish(model.BotMaintenanceFailed, nil, "bad_output", "整理输出未声明任何已处理来源")
		return
	}

	result := &botNightlyResult{Note: payload.Note, Consumed: consumed}
	if ctx.Err() != nil {
		finish(model.BotMaintenanceFailed, nil, "stop_requested", "整理被中断")
		return
	}
	commitAt := s.now()
	commitErr := s.db.Transaction(func(tx *gorm.DB) error {
		repo := repository.NewBotMemoryRepository(tx)
		if err := casMemoryFile(tx, repo, bot.ID, user); err != nil {
			return err
		}
		if err := casMemoryFile(tx, repo, bot.ID, memory); err != nil {
			return err
		}
		for _, d := range dailies {
			if err := casMemoryFile(tx, repo, bot.ID, d); err != nil {
				return err
			}
		}
		writeResident := func(path, content string, snap botMemorySnap) error {
			if strings.TrimSpace(content) == "" {
				return nil
			}
			if snap.Exists && snap.Content == content {
				return nil
			}
			op := model.BotMemoryOpCreate
			var expected *int64
			if snap.Exists {
				op = model.BotMemoryOpReplace
				v := snap.Version
				expected = &v
			}
			_, _, err := repo.WriteVersionTx(tx, bot.ID, path, op, content, false,
				expected, model.BotMemoryWriterNightly, "", maintRun.ID,
				maintRun.ID+":"+path, commitAt)
			if err != nil {
				return err
			}
			result.FilesWritten = append(result.FilesWritten, path)
			result.Consolidated = true
			return nil
		}
		if err := writeResident(botMemoryPathUser, payload.UserMD, user); err != nil {
			return err
		}
		if err := writeResident(botMemoryPathMemory, payload.MemoryMD, memory); err != nil {
			return err
		}
		raw, _ := json.Marshal(result)
		cols := repository.MarkFinishedCols(model.BotMaintenanceSucceeded, commitAt, "", "")
		cols["result_json"] = string(raw)
		_, err := repository.NewBotMaintenanceRepository(tx).UpdateColumnsTx(tx, maintRun.ID,
			map[string]interface{}{"finished_at": nil}, cols)
		return err
	})
	if commitErr != nil {
		code := "write_failed"
		if errors.Is(commitErr, repository.ErrMemoryVersionConflict) {
			code = "memory_conflict"
		}
		finish(model.BotMaintenanceFailed, result, code, botErrMessage(commitErr))
		return
	}
}

func casMemoryFile(tx *gorm.DB, repo *repository.BotMemoryRepository, botID string, snap botMemorySnap) error {
	f, err := repo.FindFileTx(tx, botID, snap.Path)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		if snap.Exists {
			return repository.ErrMemoryVersionConflict
		}
		return nil
	}
	if err != nil {
		return err
	}
	alive := f.DeletedAt == nil
	if alive != snap.Exists || (snap.Exists && f.CurrentVersion != snap.Version) {
		return repository.ErrMemoryVersionConflict
	}
	return nil
}

func (s *BotService) lastNightlyConsumed(botID string) map[string]int64 {
	out := map[string]int64{}
	rows, err := repository.NewBotMaintenanceRepository(s.db).ListSucceeded(botID, model.BotMaintenanceNightly)
	if err != nil {
		return out
	}
	for _, row := range rows {
		var res botNightlyResult
		if json.Unmarshal([]byte(row.ResultJSON), &res) != nil {
			continue
		}
		for _, c := range res.Consumed {
			if c.Version > out[c.Path] {
				out[c.Path] = c.Version
			}
		}
	}
	return out
}

// botNightlyFileHeader 是批次输入中每个 daily 的标题行。标题与结尾换行
// 同属请求体积：分批预算只累计正文会让多个短 daily 因包装开销整体超窗、
// 整轮失败而不缩小批次。
func botNightlyFileHeader(d botMemorySnap) string {
	return "=== " + d.Path + " v" + itoa64(d.Version) + " ===\n"
}

func takeNightlyBatch(remaining []botMemorySnap, roomTokens int64) (batch []botMemorySnap, rest []botMemorySnap) {
	if len(remaining) == 0 {
		return nil, nil
	}
	if roomTokens < 1 {
		return nil, remaining
	}
	var cur []botMemorySnap
	var used int64
	i := 0
	for i < len(remaining) {
		d := remaining[i]
		t := botEstimateTokens(botNightlyFileHeader(d)) + botEstimateTokens(d.Content) + 1
		if len(cur) == 0 && t > roomTokens && len(d.Content) > 0 {
			room := roomTokens - botEstimateTokens(botNightlyFileHeader(d)) - 1
			if room < 1 {
				return nil, remaining[i:]
			}
			parts := shardMemoryContent(d.Content, room)
			first := d
			first.Content = parts[0]
			if len(parts) > 1 {
				restFile := d
				restFile.Content = strings.Join(parts[1:], "")
				return []botMemorySnap{first}, append([]botMemorySnap{restFile}, remaining[i+1:]...)
			}
			return []botMemorySnap{first}, remaining[i+1:]
		}
		if used+t > roomTokens {
			break
		}
		cur = append(cur, d)
		used += t
		i++
	}
	return cur, remaining[i:]
}

func shardMemoryContent(content string, roomTokens int64) []string {
	if content == "" {
		return []string{content}
	}
	return splitUTF8ByTokenBudget(content, roomTokens)
}

func botLooksTruncatedMidSentence(s string) bool {
	s = strings.TrimSpace(s)
	return strings.HasSuffix(s, "...") || strings.HasSuffix(s, "…")
}
