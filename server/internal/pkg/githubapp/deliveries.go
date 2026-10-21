package githubapp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/godbobo/fast_ship/server/internal/githubapi"
)

// App 维度的 webhook 投递记录访问面（对账用）：/app/hook/* 端点只认 App JWT，
// 不按 installation 区分，因此不进 githubapi.GitHubClient 接口（安装令牌 seam），
// 单独定义 HookDeliveries。调用方（审查调度对账）用窄接口 + 类型断言取用。

// HookDelivery GitHub App 投递记录的一行（GET /app/hook/deliveries）。原始投递
// 与每次重投各占一行：同一 guid 追加记录，redelivery 标记是否重投行。
type HookDelivery struct {
	ID          int64
	GUID        string
	Event       string
	Action      string
	StatusCode  int
	Redelivery  bool
	DeliveredAt time.Time
}

// Delivered status_code 为 2xx 视为送达。
func (d HookDelivery) Delivered() bool {
	return d.StatusCode >= 200 && d.StatusCode < 300
}

// HookDeliveries App 维度投递记录的访问面：/app/hook/* 只认 App JWT，不按
// installation 区分。*Client 实现；Fake 也实现（debug 自测与测试）。
type HookDeliveries interface {
	// ListHookDeliveries 拉 since 之后的投递记录（含失败的）。
	ListHookDeliveries(ctx context.Context, since time.Time) ([]HookDelivery, error)
	// RedeliverHookDelivery 让 GitHub 重新投递一次（请求与原始投递相同，
	// 因此 X-GitHub-Delivery 也相同，现有幂等登记能保证不重复处理）。
	RedeliverHookDelivery(ctx context.Context, deliveryID int64) error
}

// hookDeliveryMaxPages 列表翻页上限：per_page=100 一页已远超 24h 回看窗口的
// 投递量，10 页是防失控的兜底，不是正常路径。
const hookDeliveryMaxPages = 10

// ErrDeliveriesTruncated 投递记录超过翻页上限：账本不完整，对账本轮放弃
// （部分账本会把重投次数算少；1000 条/24h 也说明投递量异常，值得人工看一眼）。
var ErrDeliveriesTruncated = errors.New("githubapp: 投递记录超过翻页上限，账本不完整")

var _ HookDeliveries = (*Client)(nil)

// hookDeliveryRaw /app/hook/deliveries 响应行的形状（GitHub 官方字段名）。
type hookDeliveryRaw struct {
	ID          int64     `json:"id"`
	GUID        string    `json:"guid"`
	Event       string    `json:"event"`
	Action      string    `json:"action"`
	StatusCode  int       `json:"status_code"`
	Redelivery  bool      `json:"redelivery"`
	DeliveredAt time.Time `json:"delivered_at"`
}

// ListHookDeliveries 拉 since 之后的投递记录（含失败的）。GitHub 按
// delivered_at 倒序返回，翻页走响应头 Link 的 rel="next" 绝对 URL；某页行
// 全部早于 since、没有 next 或达到页数上限即停。
func (c *Client) ListHookDeliveries(ctx context.Context, since time.Time) ([]HookDelivery, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	jwtTok, err := c.store.creds.appJWT(time.Now())
	if err != nil {
		return nil, err
	}
	var out []HookDelivery
	url := c.store.baseURL + "/app/hook/deliveries?per_page=100"
	for page := 0; page < hookDeliveryMaxPages; page++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Accept", "application/vnd.github+json")
		req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
		req.Header.Set("Authorization", "Bearer "+jwtTok)
		resp, err := c.store.client.Do(req)
		if err != nil {
			return nil, err
		}
		raw, readErr := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
		resp.Body.Close()
		if readErr != nil {
			return nil, readErr
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return nil, &githubapi.StatusError{StatusCode: resp.StatusCode, Method: http.MethodGet, URL: "/app/hook/deliveries", Body: string(raw)}
		}
		var rows []hookDeliveryRaw
		if err := json.Unmarshal(raw, &rows); err != nil {
			return nil, fmt.Errorf("githubapp: 解析投递列表响应失败: %w", err)
		}
		older := true
		for _, r := range rows {
			if !r.DeliveredAt.Before(since) {
				out = append(out, HookDelivery{
					ID: r.ID, GUID: r.GUID, Event: r.Event, Action: r.Action,
					StatusCode: r.StatusCode, Redelivery: r.Redelivery, DeliveredAt: r.DeliveredAt,
				})
				older = false
			}
		}
		next := nextLinkFromHeader(resp.Header.Get("Link"))
		if older || next == "" {
			return out, nil
		}
		// 跟随 next 前校验同源：Link 取自响应头，跟随前不能把显式设置的 App
		// JWT 带到别的地址；不同源当作没有下一页，正常返回已收集的行。
		if !sameOrigin(next, c.store.baseURL) {
			return out, nil
		}
		url = next
	}
	// 循环自然耗尽 = 最后一页仍有窗口内的行且带 next：账本被页数上限截断，
	// 按错误返回（对账靠完整账本推已重投次数，缺页会把次数算少）。
	return out, ErrDeliveriesTruncated
}

// RedeliverHookDelivery POST /app/hook/deliveries/{id}/attempts 请求重投；
// 期望 202 Accepted。非 2xx 转 *githubapi.StatusError。
func (c *Client) RedeliverHookDelivery(ctx context.Context, deliveryID int64) error {
	if ctx == nil {
		ctx = context.Background()
	}
	jwtTok, err := c.store.creds.appJWT(time.Now())
	if err != nil {
		return err
	}
	path := fmt.Sprintf("/app/hook/deliveries/%d/attempts", deliveryID)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.store.baseURL+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("Authorization", "Bearer "+jwtTok)
	resp, err := c.store.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return &githubapi.StatusError{StatusCode: resp.StatusCode, Method: http.MethodPost, URL: path, Body: string(raw)}
	}
	return nil
}

// nextLinkFromHeader 解析 Link 响应头里 rel="next" 的目标 URL（GitHub 分页给
// 的是绝对地址，直接作为下一请求 URL）；无下一页返回空串。
func nextLinkFromHeader(header string) string {
	for _, part := range strings.Split(header, ",") {
		segments := strings.Split(part, ";")
		if len(segments) < 2 {
			continue
		}
		target := strings.TrimSpace(segments[0])
		target = strings.TrimSuffix(strings.TrimPrefix(target, "<"), ">")
		for _, param := range segments[1:] {
			kv := strings.SplitN(strings.TrimSpace(param), "=", 2)
			if len(kv) == 2 && kv[0] == "rel" && strings.Trim(kv[1], `"`) == "next" {
				return target
			}
		}
	}
	return ""
}

// sameOrigin 判断 next 的 scheme 与 host 是否与 baseURL 一致：不一致（含解析
// 失败）返回 false，调用方按没有下一页处理。
func sameOrigin(next, baseURL string) bool {
	u, err := url.Parse(next)
	if err != nil {
		return false
	}
	b, err := url.Parse(baseURL)
	if err != nil {
		return false
	}
	return u.Scheme == b.Scheme && u.Host == b.Host
}
