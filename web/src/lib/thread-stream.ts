import { resolveSessionToken } from "./api/client";
import { createSSEParser, type BotStreamEvent } from "./bot-stream";
import { useAuthStore } from "./store/auth-store";

// T3 线程壳 SSE 订阅（INT-132）：服务端把线程变化推给浏览器，取代前端轮询。
// 与 bot-stream 的差异：事件没有 seq 游标——服务端会合并事件，重连后首帧
// 必是 full 全局刷新，客户端不补读、不去重，只负责把数据帧交给调用方。

/** event: shell 帧的 data JSON；字段缺省语义由调用方（applyThreadStreamEvent）解释。 */
export interface ThreadStreamEvent {
  environment_id?: string;
  thread_ids?: string[];
  full?: boolean;
  state?: string;
}

export type ThreadStreamStatus =
  | "connecting"
  | "open"
  | "reconnecting"
  | "closed"
  | "error";

export interface ThreadStreamOptions {
  onEvent: (event: ThreadStreamEvent) => void;
  onStatus?: (status: ThreadStreamStatus, detail?: unknown) => void;
  /** 重连退避基数（指数递增）与上限；测试可调小。 */
  retryBaseMs?: number;
  retryMaxMs?: number;
}

export interface ThreadStreamSubscription {
  /** 幂等关闭：终止在飞请求与重连循环；页面清理与 StrictMode 双卸载安全。 */
  close(): void;
}

const STREAM_URL = "/api/threads/stream";

/**
 * 建立并维持一条线程壳订阅。AbortController 只负责订阅自身的清理。
 * 后台数据流不设重试上限：断线按 retryBase*2^(n-1) 退避（封顶 retryMax）无限重连；
 * 401 复用全局 resolveSessionToken（含轮换等待），拿到新 token 立即重连，
 * 只有连续 3 次 401 才计入失败退避。
 */
export function subscribeThreadStream(
  options: ThreadStreamOptions,
): ThreadStreamSubscription {
  const retryBase = options.retryBaseMs ?? 2_000;
  const retryMax = options.retryMaxMs ?? 30_000;
  const controller = new AbortController();
  let closed = false;
  let failures = 0;
  let connected = false;
  let timer: ReturnType<typeof setTimeout> | null = null;
  // 刷新成功但端点仍 401 的异常服务：连续出现时按失败退避，不做刷新紧循环。
  let unauthorizedStreak = 0;

  const deliver = (raw: BotStreamEvent) => {
    // 契约里只有 shell 帧携带数据；注释心跳已被解析器吞掉，其余事件名忽略。
    if (raw.event !== "shell") return;
    try {
      options.onEvent(JSON.parse(raw.data) as ThreadStreamEvent);
    } catch {
      // 畸形帧丢弃，不中断连接。
    }
  };

  const scheduleRetry = (cause: unknown) => {
    if (closed) return;
    failures += 1;
    const delay = Math.min(retryBase * 2 ** (failures - 1), retryMax);
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

  async function connect(): Promise<void> {
    if (closed) return;
    options.onStatus?.(connected ? "reconnecting" : "connecting");
    const usedToken = useAuthStore.getState().token;
    let response: Response;
    try {
      response = await fetch(STREAM_URL, {
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
        // 刷新成功立即重试；连续 401 说明刷新救不回来，转入正常退避。
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
    // 每条连接一份解析器：上一段流残留的半行缓冲不得带进新连接。
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
      scheduleRetry(err);
      return;
    }
    if (closed || controller.signal.aborted) return;
    // 服务端正常关闭流：同样视为断线重连，首帧 full 保证数据重新对齐。
    scheduleRetry(new Error("stream ended"));
  }

  // 建立订阅：立即发起首连；断线经 scheduleRetry 指数退避。
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
  };
}
