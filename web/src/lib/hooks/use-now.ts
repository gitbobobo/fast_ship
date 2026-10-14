import { useEffect, useState } from "react";

/** 当前时间戳，enabled 时每 intervalMs 刷新一次，用于运行时长这类相对时间。 */
export function useNow(intervalMs: number, enabled = true) {
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    if (!enabled) return;
    setNow(Date.now());
    const id = window.setInterval(() => setNow(Date.now()), intervalMs);
    return () => window.clearInterval(id);
  }, [intervalMs, enabled]);
  return now;
}
