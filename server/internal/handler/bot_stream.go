package handler

import (
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/godbobo/fast_ship/server/internal/middleware"
	"github.com/godbobo/fast_ship/server/internal/pkg/response"
)

// SSE 流（INT-101）：鉴权和归属检查完成后才开始写流；事件先落库再发送，
// 订阅先注册唤醒，再补读并循环读数据库，避免「补读与订阅之间」漏事件。
// 断开只结束订阅，不取消 run；慢客户端断开后按 seq 重放。

const botSSEHeartbeat = 15 * time.Second

func (h *BotHandler) StreamEvents(c *gin.Context) {
	userID := middleware.GetUserID(c)
	botID := c.Param("id")

	after := int64(0)
	if raw := c.Query("after_seq"); raw != "" {
		parsed, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || parsed < 0 {
			response.BadRequest(c, 40001, "after_seq 必须是非负整数")
			return
		}
		after = parsed
	}

	// 归属检查（ListEvents 内部同样检查；这里提前做，确保未授权不建立流）。
	if _, err := h.bots.ListEvents(userID, botID, 0, 1); err != nil {
		middleware.HandleAppError(c, err)
		return
	}

	flusher, ok := c.Writer.(http.Flusher)
	if !ok {
		c.String(http.StatusInternalServerError, "streaming unsupported")
		return
	}

	c.Writer.Header().Set("Content-Type", "text/event-stream")
	c.Writer.Header().Set("Cache-Control", "no-cache")
	c.Writer.Header().Set("X-Accel-Buffering", "no")
	c.Writer.WriteHeader(http.StatusOK)

	// 1. 先注册订阅，再补读：注册与补读之间落库的事件由订阅循环兜住。
	notify, cancel := h.bots.SubscribeEvents(botID)
	defer cancel()

	writeFrame := func(seq int64, eventType string, payload []byte) bool {
		fmt.Fprintf(c.Writer, "id: %d\nevent: %s\ndata: %s\n\n", seq, eventType, payload)
		flusher.Flush()
		return true
	}

	// 2. 补读历史事件。
	cursor := after
	for {
		events, err := h.bots.ListEvents(userID, botID, cursor, 500)
		if err != nil {
			return
		}
		if len(events) == 0 {
			break
		}
		for _, ev := range events {
			if ev.Seq <= cursor {
				continue
			}
			writeFrame(ev.Seq, ev.EventType, ev.Payload)
			cursor = ev.Seq
		}
		if len(events) < 500 {
			break
		}
	}
	flusher.Flush()

	heartbeat := time.NewTicker(botSSEHeartbeat)
	defer heartbeat.Stop()
	requestCtx := c.Request.Context()

	for {
		select {
		case <-requestCtx.Done():
			return
		case <-heartbeat.C:
			// 心跳使用 SSE 注释，不增加第六种事件。
			fmt.Fprint(c.Writer, ": ping\n\n")
			flusher.Flush()
		case latest, ok := <-notify:
			if !ok {
				return
			}
			for {
				events, err := h.bots.ListEvents(userID, botID, cursor, 500)
				if err != nil {
					return
				}
				if len(events) == 0 {
					break
				}
				for _, ev := range events {
					if ev.Seq <= cursor {
						continue
					}
					writeFrame(ev.Seq, ev.EventType, ev.Payload)
					cursor = ev.Seq
				}
				if len(events) < 500 {
					break
				}
			}
			_ = latest
		}
	}
}

// SSE 帧格式固定为 `id: <seq>` + `event: <类型>` + `data: <落库 payload>`；
// data 不再包一层 JSON 信封（handler 自己编码流）。
