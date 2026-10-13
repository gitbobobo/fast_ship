package service

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/godbobo/fast_ship/server/internal/pkg/errs"
	"gorm.io/gorm"
)

// T3 图片附件（INT-85）：浏览器 → Fast Ship → T3。
//
// 上传经 WS RPC attachments.createUploadUrl 取同 origin 签名 URL，再把原始
// 字节流式 POST 上去；Fast Ship 不落盘、不建附件表、不复用 Issue 附件，
// 不接受 multipart 自动落临时文件，也不做 base64。读取经 assets.createUrl
// 取签名 URL 后代理字节。PNG/JPEG/GIF/WebP，单图 10MiB
// （contracts chatAttachment.ts PROVIDER_SEND_TURN_*）。

const (
	t3MaxImageBytes = 10 << 20 // 10MiB，与上游 PROVIDER_SEND_TURN_MAX_IMAGE_BYTES 一致
)

// t3SupportedImageMimes 与上游 PROVIDER_SEND_TURN_SUPPORTED_IMAGE_MIME_TYPES 一致。
var t3SupportedImageMimes = map[string]bool{
	"image/png": true, "image/jpeg": true, "image/gif": true, "image/webp": true,
}

func t3SupportedImageMime(mime string) bool {
	return t3SupportedImageMimes[strings.ToLower(mime)]
}

// CreateAttachmentUpload 上传一张图片：createUploadUrl → POST 字节 → 返回
// 可放进消息 attachments 数组的元数据。
func (s *T3ThreadsService) CreateAttachmentUpload(ctx context.Context, userID, envID, name, mimeType string, sizeBytes int64, body io.Reader) (*T3ChatAttachment, error) {
	mimeType = strings.ToLower(strings.TrimSpace(mimeType))
	if !t3SupportedImageMime(mimeType) {
		return nil, errs.New(errs.ErrInvalidParams.Code, "仅支持 PNG/JPEG/GIF/WebP 图片")
	}
	if sizeBytes <= 0 {
		return nil, errs.New(errs.ErrInvalidParams.Code, "缺少 Content-Length 或请求体为空")
	}
	if sizeBytes > t3MaxImageBytes {
		return nil, errs.New(errs.ErrInvalidParams.Code, "图片超过 10MiB 上限")
	}
	name = strings.TrimSpace(name)
	if name == "" {
		name = "image." + strings.TrimPrefix(mimeType, "image/")
	}
	if len(name) > 255 {
		name = name[:255]
	}

	env, err := s.envRepo.FindByIDAndUserID(ctx, envID, userID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errs.ErrT3EnvironmentNotFound
		}
		return nil, errs.ErrInternal
	}
	if err := requireT3Operate(env); err != nil {
		return nil, err
	}

	ctx, cancel := s.t3WriteBudget(ctx)
	defer cancel()

	token, err := s.bearerToken(env)
	if err != nil {
		return nil, errs.New(errs.ErrT3Upstream.Code, "环境凭证不可用，请重新配对")
	}
	if status, probeErr := s.http.probeSession(ctx, env.Address, token); status != T3EnvironmentStatusOK {
		return nil, errs.New(errs.ErrT3Upstream.Code, probeErr)
	}

	session, err := dialT3RPC(ctx, wsBaseOf(env.Address), token)
	if err != nil {
		return nil, s.upstreamError(err)
	}
	defer session.Close()

	value, err := session.call(ctx, "attachments.createUploadUrl", map[string]any{
		"type":      "image",
		"name":      name,
		"mimeType":  mimeType,
		"sizeBytes": sizeBytes,
	})
	if err != nil {
		return nil, s.upstreamError(err)
	}
	var minted struct {
		AttachmentId string `json:"attachmentId"`
		RelativeUrl  string `json:"relativeUrl"`
	}
	if err := json.Unmarshal(value, &minted); err != nil || minted.AttachmentId == "" || minted.RelativeUrl == "" {
		return nil, errs.New(errs.ErrT3Upstream.Code, "上传地址解析失败")
	}

	// 同 origin 签名 POST：字节流转发，Content-Length 必须与声明一致
	// （T3 校验，不匹配返回 400）。
	status, respBody, err := s.http.postRawBytes(ctx, env.Address, minted.RelativeUrl, mimeType, sizeBytes, body)
	if err != nil {
		return nil, errs.New(errs.ErrT3Upstream.Code, "图片上传失败: "+shortenT3Error(err))
	}
	if status != http.StatusNoContent && status != http.StatusOK {
		return nil, errs.New(errs.ErrT3Upstream.Code, "图片上传被拒绝（HTTP "+strconv.Itoa(status)+"）: "+t3ErrorMessage(respBody))
	}
	return &T3ChatAttachment{
		Type:      "image",
		Id:        minted.AttachmentId,
		Name:      name,
		MimeType:  mimeType,
		SizeBytes: int(sizeBytes),
	}, nil
}

// GetAttachmentContent 代理读取附件字节：assets.createUrl（attachment 资源，
// 只需 orchestration:read）→ GET 签名 URL → 原样转发字节。
func (s *T3ThreadsService) GetAttachmentContent(ctx context.Context, userID, envID, attachmentID string) (string, []byte, error) {
	if attachmentID == "" || len(attachmentID) > 256 {
		return "", nil, errs.ErrInvalidParams
	}
	env, err := s.envRepo.FindByIDAndUserID(ctx, envID, userID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return "", nil, errs.ErrT3EnvironmentNotFound
		}
		return "", nil, errs.ErrInternal
	}

	ctx, cancel := s.t3WriteBudget(ctx)
	defer cancel()

	token, err := s.bearerToken(env)
	if err != nil {
		return "", nil, errs.New(errs.ErrT3Upstream.Code, "环境凭证不可用，请重新配对")
	}
	if status, probeErr := s.http.probeSession(ctx, env.Address, token); status != T3EnvironmentStatusOK {
		return "", nil, errs.New(errs.ErrT3Upstream.Code, probeErr)
	}

	session, err := dialT3RPC(ctx, wsBaseOf(env.Address), token)
	if err != nil {
		return "", nil, s.upstreamError(err)
	}
	defer session.Close()

	value, err := session.call(ctx, "assets.createUrl", map[string]any{
		"resource": map[string]any{"_tag": "attachment", "attachmentId": attachmentID},
	})
	if err != nil {
		if strings.Contains(err.Error(), "AssetAttachmentNotFoundError") {
			return "", nil, errs.New(errs.ErrT3ThreadNotFound.Code, "附件不存在或上传已过期")
		}
		return "", nil, s.upstreamError(err)
	}
	var asset struct {
		RelativeUrl string `json:"relativeUrl"`
	}
	if err := json.Unmarshal(value, &asset); err != nil || asset.RelativeUrl == "" {
		return "", nil, errs.New(errs.ErrT3Upstream.Code, "附件地址解析失败")
	}

	status, contentType, data, err := s.http.getRawBytes(ctx, env.Address, asset.RelativeUrl, "")
	if err != nil {
		return "", nil, errs.New(errs.ErrT3Upstream.Code, "附件读取失败: "+shortenT3Error(err))
	}
	if status == http.StatusNotFound {
		return "", nil, errs.New(errs.ErrT3ThreadNotFound.Code, "附件不存在或上传已过期")
	}
	if status != http.StatusOK {
		return "", nil, errs.New(errs.ErrT3Upstream.Code, "附件读取被拒绝（HTTP "+strconv.Itoa(status)+"）")
	}
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	return contentType, data, nil
}

// DeleteAttachment 删除尚未被消息引用的 pending 附件（attachments.delete）。
func (s *T3ThreadsService) DeleteAttachment(ctx context.Context, userID, envID, attachmentID string) error {
	if attachmentID == "" || len(attachmentID) > 256 {
		return errs.ErrInvalidParams
	}
	env, err := s.loadEnvForWrite(ctx, userID, envID)
	if err != nil {
		return err
	}

	ctx, cancel := s.t3WriteBudget(ctx)
	defer cancel()

	token, err := s.bearerToken(env)
	if err != nil {
		return errs.New(errs.ErrT3Upstream.Code, "环境凭证不可用，请重新配对")
	}
	if status, probeErr := s.http.probeSession(ctx, env.Address, token); status != T3EnvironmentStatusOK {
		return errs.New(errs.ErrT3Upstream.Code, probeErr)
	}

	session, err := dialT3RPC(ctx, wsBaseOf(env.Address), token)
	if err != nil {
		return s.upstreamError(err)
	}
	defer session.Close()

	if _, err := session.call(ctx, "attachments.delete", map[string]any{"attachmentId": attachmentID}); err != nil {
		return s.upstreamError(err)
	}
	return nil
}
