package service

import (
	"encoding/json"

	"github.com/godbobo/fast_ship/server/internal/api"
)

// 交互类条目（审批/提问/密钥）的归一化与运行时请求补齐（INT-85）。

// t3ApprovalOption 是审批条目里的一个可选项（contracts providerPolicy.ts
// ProviderApprovalOption）。
type t3ApprovalOption struct {
	Decision string  `json:"decision"`
	Label    string  `json:"label"`
	Warning  *string `json:"warning"`
}

// t3UserInputQuestion 是提问条目里的一个问题
// （contracts orchestrationV2.ts OrchestrationV2UserInputQuestion）。
type t3UserInputQuestion struct {
	ID                string              `json:"id"`
	Header            string              `json:"header"`
	Question          string              `json:"question"`
	Options           []t3UserInputOption `json:"options"`
	MultiSelect       *bool               `json:"multiSelect"`
	AllowCustomAnswer *bool               `json:"allowCustomAnswer"`
	InitialAnswer     *string             `json:"initialAnswer"`
	Required          *bool               `json:"required"`
}

type t3UserInputOption struct {
	Label       string  `json:"label"`
	Description string  `json:"description"`
	Value       *string `json:"value"`
}

// normalizeT3ApprovalOptions 审批可选项原样映射（snake_case 化）。
func normalizeT3ApprovalOptions(options []t3ApprovalOption) []api.T3ApprovalOption {
	if len(options) == 0 {
		return nil
	}
	out := make([]api.T3ApprovalOption, 0, len(options))
	for _, option := range options {
		out = append(out, api.T3ApprovalOption{
			Decision: option.Decision,
			Label:    option.Label,
			Warning:  api.NonEmptyPtr(option.Warning),
		})
	}
	return out
}

// normalizeT3UserInputQuestions 问题列表原样映射。
func normalizeT3UserInputQuestions(questions []t3UserInputQuestion) []api.T3UserInputQuestion {
	if len(questions) == 0 {
		return nil
	}
	out := make([]api.T3UserInputQuestion, 0, len(questions))
	for _, q := range questions {
		options := make([]api.T3UserInputQuestion_Options, 0, len(q.Options))
		for _, option := range q.Options {
			options = append(options, api.T3UserInputQuestion_Options{
				Label:       option.Label,
				Description: option.Description,
				Value:       api.NonEmptyPtr(option.Value),
			})
		}
		out = append(out, api.T3UserInputQuestion{
			Id:                q.ID,
			Header:            q.Header,
			Question:          q.Question,
			Options:           options,
			MultiSelect:       q.MultiSelect,
			AllowCustomAnswer: q.AllowCustomAnswer,
			InitialAnswer:     api.NonEmptyPtr(q.InitialAnswer),
			Required:          q.Required,
		})
	}
	return out
}

// normalizeT3ChatAttachments 把上游附件数组归一化成 Fast Ship 形状。
// 只带 type=image（Fast Ship 只上传/展示图片）；文件与未知类型按 T3 的
// 消费者口径跳过而非失败。
func normalizeT3ChatAttachments(raw []json.RawMessage) []T3ChatAttachment {
	out := make([]T3ChatAttachment, 0, len(raw))
	for _, item := range raw {
		var m struct {
			Type      string `json:"type"`
			ID        string `json:"id"`
			Name      string `json:"name"`
			MimeType  string `json:"mimeType"`
			SizeBytes *int   `json:"sizeBytes"`
		}
		if err := json.Unmarshal(item, &m); err != nil || m.ID == "" || m.Type != "image" {
			continue
		}
		out = append(out, T3ChatAttachment{
			Type:      "image",
			Id:        m.ID,
			Name:      m.Name,
			MimeType:  m.MimeType,
			SizeBytes: t3IntOrZero(m.SizeBytes),
		})
	}
	return out
}

// t3ResponseCapabilityOf 解运行时请求的 responseCapability 联合
// （{type:"live"|"message"|"not_resumable"}），未知形状回退空串。
func t3ResponseCapabilityOf(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var m struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(raw, &m); err != nil {
		return ""
	}
	return m.Type
}

// enrichT3RequestItems 把投影 runtimeRequests 的状态/应答通道/已定 decision
// 按 requestId 关联回审批与提问条目。找不到对应记录的条目保持缺省
// （如上游裁剪掉了 runtimeRequests）。
func enrichT3RequestItems(items []T3ThreadItem, requests []t3RuntimeRequest) {
	if len(requests) == 0 {
		return
	}
	byID := make(map[string]t3RuntimeRequest, len(requests))
	for _, request := range requests {
		byID[request.ID] = request
	}
	for i := range items {
		if items[i].RequestId == nil {
			continue
		}
		request, ok := byID[*items[i].RequestId]
		if !ok {
			continue
		}
		items[i].RequestStatus = api.NonEmpty(request.Status)
		items[i].ResponseCapability = api.NonEmpty(t3ResponseCapabilityOf(request.ResponseCapability))
		if items[i].Type == T3ThreadItemTypeApprovalRequest {
			items[i].Decision = api.NonEmptyPtr(request.Decision)
		}
		if items[i].Type == T3ThreadItemTypeUserInputRequest && len(request.Answers) > 0 {
			items[i].Answers = request.Answers
		}
	}
}
