import { resolveSessionToken } from "./api/client";
import { useAuthStore } from "./store/auth-store";

// 机器人 SSE 订阅（INT-101）：原生 fetch + ReadableStream 手写解析，
// 不引入 eventsource 库。事件先落库再发送（服务端保证），这里负责
// 半行/CRLF/多行 data/注释心跳/跨 chunk 解码，以及断线按 event_seq 补读去重。

export interface BotStreamEvent {
  /** 服务端 event: 行；固定五类 delta/tool_call/tool_result/done/error。 */
  event: string;
  /** 多行 data 以 \n 连接后的原始文本（单行 JSON）。 */
  data: string;
  /** id: 行的 event_seq；缺失或非数字为 null，不参与游标推进。 */
  id: number | null;
}

/** data JSON 的公共外壳；payload 的具体形状按 event 类型解释（bots.ts）。 */
export interface BotStreamEventData {
  bot_id?: string;
  run_id?: string;
  message_id?: string;
  call_id?: string;
  /** delta 上 kind=message_commit 表示全文提交，payload 为完整消息。 */
  kind?: string;
  /** tool_call: proposed/awaiting_confirmation/queued/running；tool_result: receipt/decision/terminal。 */
  phase?: string;
  payload?: unknown;
}

/**
 * SSE 逐块解析器。以空行作为一次投递的边界；行终止符兼容 \n、\r、\r\n，
 * 含 \r\n 恰好跨 chunk 的情况（结尾悬空的 \r 留在缓冲里等下一块）。
 * 流结束时仍无行终止符的残余半行按规范丢弃。
 */
export function createSSEParser(onEvent: (event: BotStreamEvent) => void) {
  let buffer = "";
  let dataLines: string[] = [];
  let eventName = "message";
  let eventId: string | null = null;
  // 上一块以悬空 \r 结尾时置位：下一块若以 \n 开头，那个 \n 属于 \r\n，跳过。
  let afterCR = false;

  const dispatch = () => {
    const hasData = dataLines.length > 0;
    if (hasData || eventId !== null) {
      const id = eventId !== null && eventId !== "" ? Number(eventId) : null;
      onEvent({
        event: eventName,
        data: dataLines.join("\n"),
        id: id !== null && Number.isFinite(id) ? id : null,
      });
    }
    dataLines = [];
    eventName = "message";
    eventId = null;
  };

  const handleLine = (line: string) => {
    if (line === "") {
      dispatch();
      return;
    }
    // 冒号开头是注释（服务端心跳），直接忽略。
    if (line.startsWith(":")) return;
    const colon = line.indexOf(":");
    const field = colon === -1 ? line : line.slice(0, colon);
    let value = colon === -1 ? "" : line.slice(colon + 1);
    if (value.startsWith(" ")) value = value.slice(1);
    if (field === "data") dataLines.push(value);
    else if (field === "event") eventName = value;
    else if (field === "id") eventId = value;
    // retry 等其余字段与订阅无关，忽略。
  };

  return {
    push(chunk: string) {
      if (afterCR && chunk.startsWith("\n")) chunk = chunk.slice(1);
      afterCR = false;
      buffer += chunk;
      let start = 0;
      for (;;) {
        let end = -1;
        let nextStart = -1;
        let trailingCR = false;
        for (let i = start; i < buffer.length; i += 1) {
          const ch = buffer[i];
          if (ch === "\n") {
            end = i;
            nextStart = i + 1;
            break;
          }
          if (ch === "\r") {
            if (i + 1 < buffer.length) {
              end = i;
              nextStart = buffer[i + 1] === "\n" ? i + 2 : i + 1;
              break;
            }
            // 结尾悬空的 \r 先按裸 CR 结束本行，并记住等下一块消歧。
            trailingCR = true;
            end = i;
            nextStart = i + 1;
            break;
          }
        }
        if (end === -1) break;
        handleLine(buffer.slice(start, end));
        start = nextStart;
        if (trailingCR) {
          afterCR = true;
          break;
        }
      }
      buffer = buffer.slice(start);
    },
  };
}

export type BotStreamStatus =
  | "connecting"
  | "open"
  | "reconnecting"
  | "closed"
  | "error";

export interface BotStreamOptions {
  botId: string;
  /** 首连补读游标：只接收 seq 大于该值的事件。 */
  afterSeq: number;
  onEvent: (event: BotStreamEvent) => void;
  onStatus?: (status: BotStreamStatus, detail?: unknown) => void;
  /** 重连退避基数（指数递增，上限 15s）与连续失败上限；测试可调小。 */
  retryBaseMs?: number;
  maxRetries?: number;
}

export interface BotStreamSubscription {
  /** 幂等关闭：终止在飞请求与重连循环；页面清理与 StrictMode 双卸载安全。 */
  close(): void;
  /** 连续失败达到上限后的手动恢复：从 lastSeq 续读，不重放旧事件。 */
  resume(): void;
  /** 已确认投递的最大 event_seq，即下一次重连的补读游标。 */
  lastSeq(): number;
}

const MAX_RETRY_DELAY_MS = 15_000;

/**
 * 建立并维持一条 SSE 订阅。AbortController 只负责订阅自身的清理；
 * 断线只结束订阅连接并按 seq 补读，绝不重发消息或批准请求。
 * 401 复用全局 resolveSessionToken（含轮换等待），拿到新 token 立即重连，
 * 不计入连续失败。
 */
export function subscribeBotStream(options: BotStreamOptions): BotStreamSubscription {
  const retryBase = options.retryBaseMs ?? 2_000;
  const maxRetries = options.maxRetries ?? 5;
  const controller = new AbortController();
  let closed = false;
  let lastSeq = Math.max(0, options.afterSeq);
  let failures = 0;
  let connected = false;
  let timer: ReturnType<typeof setTimeout> | null = null;

  const streamUrl = () =>
    `/api/bots/${encodeURIComponent(options.botId)}/events?after_seq=${lastSeq}`;

  const deliver = (event: BotStreamEvent) => {
    // 重连补读可能与已投递事件重叠（服务端游标之后仍有重复段），按 seq 去重。
    if (event.id !== null) {
      if (event.id <= lastSeq) return;
      lastSeq = event.id;
    }
    options.onEvent(event);
  };

  const scheduleRetry = (cause: unknown) => {
    if (closed) return;
    failures += 1;
    if (failures > maxRetries) {
      options.onStatus?.("error", cause);
      return;
    }
    const delay = Math.min(retryBase * 2 ** (failures - 1), MAX_RETRY_DELAY_MS);
    options.onStatus?.("reconnecting", cause);
    timer = setTimeout(() => {
      timer = null;
      void connect();
    }, delay);
  };

  const discard = (response: Response) => {
    // 不消费的响应体要显式取消，避免连接悬挂。
    void response.body?.cancel().catch(() => undefined);
  };

  // 刷新成功但端点仍 401 的异常服务：连续出现时按失败退避，不做刷新紧循环。
  let unauthorizedStreak = 0;

  async function connect(): Promise<void> {
    if (closed) return;
    options.onStatus?.(connected ? "reconnecting" : "connecting");
    const usedToken = useAuthStore.getState().token;
    let response: Response;
    try {
      response = await fetch(streamUrl(), {
        headers: {
          Accept: "text/event-stream",
          ...(usedToken ? { Authorization: `Bearer ${usedToken}` } : {}),
        },
        signal: controller.signal,
      });
    } catch (err) {
      if (closed || controller.signal.aborted) return;
      scheduleRetry(err);
      return;
    }

    if (response.status === 401) {
      unauthorizedStreak += 1;
      try {
        await resolveSessionToken(usedToken ?? null);
        discard(response);
        if (closed) return;
        // 刷新成功立即重试；正常情况下凭证换新后 streak 归零。
        if (unauthorizedStreak >= 3) {
          scheduleRetry(new Error("SSE 401 persists after refresh"));
          return;
        }
        return void connect();
      } catch (err) {
        discard(response);
        scheduleRetry(err);
        return;
      }
    }
    unauthorizedStreak = 0;

    if (!response.ok || !response.body) {
      discard(response);
      scheduleRetry(new Error(`SSE HTTP ${response.status}`));
      return;
    }

    failures = 0;
    connected = true;
    options.onStatus?.("open");
    const parser = createSSEParser(deliver);
    const reader = response.body.getReader();
    const decoder = new TextDecoder();
    try {
      for (;;) {
        const { done, value } = await reader.read();
        if (done) break;
        parser.push(decoder.decode(value, { stream: true }));
      }
      // 冲刷解码器里残余的多字节字符。
      parser.push(decoder.decode());
    } catch (err) {
      if (closed || controller.signal.aborted) return;
      // 读流中断（网络抖动等），走重连补读。
      scheduleRetry(err);
      return;
    }
    if (closed || controller.signal.aborted) return;
    // 服务端正常关闭流：同样视为断线，按游标重连。
    scheduleRetry(new Error("stream ended"));
  }

  // 建立订阅：立即发起首连；断线经 scheduleRetry 指数退避补读。
  void connect();

  return {
    close() {
      if (closed) return;
      closed = true;
      if (timer !== null) {
        clearTimeout(timer);
        timer = null;
      }
      controller.abort();
      options.onStatus?.("closed");
    },
    resume() {
      if (closed) return;
      if (timer !== null) {
        clearTimeout(timer);
        timer = null;
      }
      failures = 0;
      void connect();
    },
    lastSeq() {
      return lastSeq;
    },
  };
}
