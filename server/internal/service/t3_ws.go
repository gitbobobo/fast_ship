package service

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"golang.org/x/net/websocket"
)

// T3 环境 WebSocket RPC 客户端。
//
// 线路协议（2026-10 核对自 T3 源码与 effect@4.0.2 rpc/RpcSerialization.ts）：
//   - 连接：GET {ws-base}/ws?orchestrationProtocol=2，Authorization: Bearer 直连
//     （apps/server/src/ws.ts：协议不匹配返回 426；认证先 wsTicket 后 Bearer，
//     EnvironmentAuth.authenticateWebSocketUpgrade）
//   - 每个文本帧一个 JSON 对象（RpcSerialization.json）
//   - 请求帧：{"_tag":"Request","id":<id>,"tag":"<方法>","payload":{...},"headers":[]}
//   - 流式块帧：{"_tag":"Chunk","requestId":<id>,"values":[...]}，
//     客户端需回 {"_tag":"Ack","requestId":<id>} 才会收到下一块
//   - 终止帧：{"_tag":"Exit","requestId":<id>,"exit":{"_tag":"Success","value":...}}
//     或 {"exit":{"_tag":"Failure","cause":[{"_tag":"Fail","error":{...}}| ...]}}
//   - 中断：{"_tag":"Interrupt","requestId":<id>,"interruptors":[]}

const (
	t3WSReadTimeout  = 15 * time.Second // 单帧等待上限
	t3WSWriteTimeout = 10 * time.Second // 单帧写出上限
	t3WSMaxFrameSize = 64 << 20         // 单帧上限，与 HTTP 响应体上限一致
)

// t3RPCSession 是一条已认证的 /ws 连接，按顺序串行发请求。
type t3RPCSession struct {
	conn   *websocket.Conn
	nextID int
}

// closeOnCancel 让 ctx 取消（含 deadline 到点）时立即关闭连接，打断阻塞中的
// 读写 I/O——x/net/websocket 的 Receive/Send 不感知 ctx，关连接是唯一的解阻塞
// 手段，否则 runT3Bounded 的预算到点后 goroutine 仍要等到帧级超时。会话本就
// 随调用方的 defer Close 一次性使用，额外的关闭没有副作用。返回 stop 函数，
// 调用结束时执行以注销回调。
func (s *t3RPCSession) closeOnCancel(ctx context.Context) (stop func() bool) {
	return context.AfterFunc(ctx, func() { _ = s.conn.Close() })
}

// dialT3RPC 建立 WS RPC 会话。TCP 拨号与 WS 握手全程由 ctx 控制——调用方
// 必须传入带 deadline 的 ctx（单环境预算），否则挂起的服务端会无限占用连接。
// 升级被拒（凭证失效/协议不符）与网络不可达都返回错误，由调用方
// 结合 HTTP 会话探测结果区分（x/net/websocket 的 DialError 不暴露状态码）。
func dialT3RPC(ctx context.Context, wsBaseURL, bearerToken string) (*t3RPCSession, error) {
	wsURL := strings.TrimSuffix(wsBaseURL, "/") + "/ws?orchestrationProtocol=" + t3OrchestrationProtocolV
	config, err := websocket.NewConfig(wsURL, "http://localhost")
	if err != nil {
		return nil, fmt.Errorf("构造 WS 连接失败: %w", err)
	}
	config.Header = http.Header{
		"Authorization": []string{"Bearer " + bearerToken},
	}

	conn, err := config.DialContext(ctx)
	if err != nil {
		return nil, fmt.Errorf("WS 连接失败: %w", err)
	}
	conn.MaxPayloadBytes = t3WSMaxFrameSize
	return &t3RPCSession{conn: conn, nextID: 1}, nil
}

func (s *t3RPCSession) Close() {
	_ = s.conn.Close()
}

// rpcRequestFrame 是客户端请求帧。
type rpcRequestFrame struct {
	Tag     string `json:"_tag"`
	ID      int    `json:"id"`
	Method  string `json:"tag"`
	Payload any    `json:"payload"`
	Headers []any  `json:"headers"`
}

// call 发送一次性请求，等待 Exit 并返回 success value 的原始 JSON。
// ctx 取消时连接被立即关闭（见 closeOnCancel），阻塞中的读写随之中断；
// 取消后的调用在写出前即失败，不再发出任何 RPC 帧。
func (s *t3RPCSession) call(ctx context.Context, method string, payload any) (json.RawMessage, error) {
	id := s.nextID
	s.nextID++
	defer s.closeOnCancel(ctx)()
	if err := s.sendRequest(ctx, id, method, payload); err != nil {
		return nil, err
	}
	for {
		frame, err := s.readFrame(ctx)
		if err != nil {
			return nil, err
		}
		switch {
		case frame.isExitFor(id):
			return frame.exitValue()
		case frame.isChunkFor(id):
			// 一次性请求不会发 Chunk；礼貌性 Ack 防止服务端 latch 卡住。
			if err := s.sendAck(ctx, frame.RequestID); err != nil {
				return nil, fmt.Errorf("发送 Ack 失败: %w", err)
			}
		}
	}
}

// callFirstChunkValue 订阅一个流式方法，把第一个 Chunk 的每个 value 交给 handle，
// handle 返回 stop 后发送 Interrupt 并结束。handle 遇到的第一个错误优先返回；
// Ack/Interrupt 等 I/O 失败只在没有业务错误可报时向上传播——连接层失败不掩盖
// handle 给出的真实原因。
func (s *t3RPCSession) callFirstChunkValue(ctx context.Context, method string, payload any, handle func(value json.RawMessage) (stop bool, err error)) error {
	id := s.nextID
	s.nextID++
	defer s.closeOnCancel(ctx)()
	if err := s.sendRequest(ctx, id, method, payload); err != nil {
		return err
	}
	var handleErr error
	for {
		frame, err := s.readFrame(ctx)
		if err != nil {
			if handleErr != nil {
				return handleErr
			}
			return err
		}
		if frame.isExitFor(id) {
			if _, err := frame.exitValue(); err != nil && handleErr == nil {
				return err
			}
			return handleErr
		}
		if !frame.isChunkFor(id) {
			continue
		}
		stop := false
		for _, value := range frame.Values {
			if s, err := handle(value); err != nil {
				if handleErr == nil {
					handleErr = err
				}
				stop = true
				break
			} else if s {
				stop = true
				break
			}
		}
		if err := s.sendAck(ctx, id); err != nil && handleErr == nil {
			handleErr = err
		}
		if stop {
			if err := s.sendInterrupt(ctx, id); err != nil && handleErr == nil {
				handleErr = err
			}
			return handleErr
		}
	}
}

func (s *t3RPCSession) sendRequest(ctx context.Context, id int, method string, payload any) error {
	frame := rpcRequestFrame{Tag: "Request", ID: id, Method: method, Payload: payload, Headers: []any{}}
	data, err := json.Marshal(frame)
	if err != nil {
		return fmt.Errorf("编码 RPC 请求失败: %w", err)
	}
	if err := s.writeFrame(ctx, data); err != nil {
		return fmt.Errorf("发送 RPC 请求失败: %w", err)
	}
	return nil
}

func (s *t3RPCSession) sendAck(ctx context.Context, requestID any) error {
	return s.sendRaw(ctx, map[string]any{"_tag": "Ack", "requestId": requestID})
}

func (s *t3RPCSession) sendInterrupt(ctx context.Context, requestID any) error {
	return s.sendRaw(ctx, map[string]any{"_tag": "Interrupt", "requestId": requestID, "interruptors": []any{}})
}

func (s *t3RPCSession) sendRaw(ctx context.Context, msg any) error {
	data, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	return s.writeFrame(ctx, data)
}

// writeFrame 带独立写预算写出文本帧：deadline 取单帧写超时与 ctx 预算的较小者，
// 写前查取消——ctx 已作废时不发出任何字节。阻塞中的写由 closeOnCancel 注册的
// 连接关闭打断。
func (s *t3RPCSession) writeFrame(ctx context.Context, data []byte) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("RPC 连接已取消: %w", err)
	}
	deadline := time.Now().Add(t3WSWriteTimeout)
	if ctxDeadline, ok := ctx.Deadline(); ok && ctxDeadline.Before(deadline) {
		deadline = ctxDeadline
	}
	s.conn.SetWriteDeadline(deadline)
	return websocket.Message.Send(s.conn, data)
}

// rpcFrame 是服务端帧的宽松解码：只固定 _tag/requestId，其余按需二次解析。
type rpcFrame struct {
	Tag       string            `json:"_tag"`
	RequestID any               `json:"requestId"`
	ID        any               `json:"id"`
	Values    []json.RawMessage `json:"values"`
	Exit      *rpcExit          `json:"exit"`
}

type rpcExit struct {
	Tag   string            `json:"_tag"`
	Value json.RawMessage   `json:"value"`
	Cause []rpcFailureCause `json:"cause"`
}

type rpcFailureCause struct {
	Tag    string          `json:"_tag"`
	Error  json.RawMessage `json:"error"`
	Defect json.RawMessage `json:"defect"`
}

// readFrame 读下一帧：deadline 取单帧读超时与 ctx 预算的较小者；ctx 取消时
// closeOnCancel 注册的连接关闭会立即打断阻塞中的 Receive（不必等到读超时）。
func (s *t3RPCSession) readFrame(ctx context.Context) (*rpcFrame, error) {
	deadline := time.Now().Add(t3WSReadTimeout)
	if ctxDeadline, ok := ctx.Deadline(); ok && ctxDeadline.Before(deadline) {
		deadline = ctxDeadline
	}
	s.conn.SetReadDeadline(deadline)

	// x/net 的 Message 编解码只认 []byte/string，不能用 json.RawMessage 直接收。
	var data []byte
	if err := websocket.Message.Receive(s.conn, &data); err != nil {
		return nil, fmt.Errorf("读取 RPC 帧失败: %w", err)
	}
	var frame rpcFrame
	if err := json.Unmarshal(data, &frame); err != nil {
		return nil, fmt.Errorf("RPC 帧解析失败: %w", err)
	}
	return &frame, nil
}

func (f *rpcFrame) isExitFor(id int) bool {
	return f.Tag == "Exit" && f.requestIDMatches(id)
}

func (f *rpcFrame) isChunkFor(id int) bool {
	return f.Tag == "Chunk" && f.requestIDMatches(id)
}

func (f *rpcFrame) requestIDMatches(id int) bool {
	switch v := f.RequestID.(type) {
	case float64:
		return int(v) == id
	case string:
		return v == fmt.Sprintf("%d", id)
	default:
		return false
	}
}

// exitValue 把 Exit 帧转成 success value 或错误。
// Fail 错误对象带 Effect TaggedError 的 _tag；授权类错误映射为凭证失效，
// 其余取 message 字段。
func (f *rpcFrame) exitValue() (json.RawMessage, error) {
	if f.Exit == nil {
		return nil, fmt.Errorf("RPC Exit 帧缺少 exit 字段")
	}
	switch f.Exit.Tag {
	case "Success":
		return f.Exit.Value, nil
	case "Failure":
		return nil, t3RPCCauseError(f.Exit.Cause)
	default:
		return nil, fmt.Errorf("RPC Exit 未知类型 %q", f.Exit.Tag)
	}
}

func t3RPCCauseError(cause []rpcFailureCause) error {
	if len(cause) == 0 {
		return fmt.Errorf("RPC 调用失败（无错误详情）")
	}
	first := cause[0]
	switch first.Tag {
	case "Fail":
		var tagged struct {
			Tag     string `json:"_tag"`
			Message string `json:"message"`
		}
		if err := json.Unmarshal(first.Error, &tagged); err == nil {
			// AuthorizationError=RPC 层授权拒绝（scope/权限不足），
			// AuthInvalidError=凭证无效/过期；两者都按凭证失效处理。
			if tagged.Tag == "EnvironmentAuthorizationError" || tagged.Tag == "EnvironmentAuthInvalidError" {
				if tagged.Message != "" {
					return fmt.Errorf("%w: %s", errT3CredentialRejected, tagged.Message)
				}
				return errT3CredentialRejected
			}
			if tagged.Message != "" {
				return fmt.Errorf("%s: %s", tagged.Tag, tagged.Message)
			}
			return fmt.Errorf("RPC 调用失败: %s", tagged.Tag)
		}
		return fmt.Errorf("RPC 调用失败: %s", string(first.Error))
	case "Die":
		return fmt.Errorf("RPC 处理器异常: %s", t3ErrorMessage(first.Defect))
	case "Interrupt":
		return fmt.Errorf("RPC 调用被中断")
	default:
		return fmt.Errorf("RPC 调用失败（未知错误类型 %q）", first.Tag)
	}
}
