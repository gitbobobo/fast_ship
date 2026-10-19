import { afterEach, describe, expect, it, vi } from "vitest";
import { useBotDetailPanelStore } from "./bot-detail-panel-store";

const STORAGE_KEY = "fast-ship-bot-detail-panel";

/** 让下一次 rehydrate 读到指定内容；返回写入侧 spy。 */
function stubStored(value: string | null) {
  const setItem = vi.fn();
  window.localStorage.getItem = vi.fn(() => value);
  window.localStorage.setItem = setItem;
  return setItem;
}

describe("机器人右栏展开状态", () => {
  afterEach(async () => {
    // 共享的 localStorage mock 归位，store 回到默认，避免影响其它用例。
    window.localStorage.getItem = vi.fn(() => null);
    window.localStorage.setItem = vi.fn();
    useBotDetailPanelStore.setState({ expanded: false });
  });

  it("空存储时默认收起", async () => {
    stubStored(null);
    await useBotDetailPanelStore.persist.rehydrate();
    expect(useBotDetailPanelStore.getState().expanded).toBe(false);
  });

  it("展开写回存储，重新水合后恢复展开", async () => {
    stubStored(null);
    await useBotDetailPanelStore.persist.rehydrate();

    useBotDetailPanelStore.getState().setExpanded(true);
    const setItem = vi.mocked(window.localStorage.setItem);
    const [, raw] = setItem.mock.calls.at(-1)!;
    expect(setItem.mock.calls.at(-1)![0]).toBe(STORAGE_KEY);
    expect(JSON.parse(raw)).toMatchObject({ state: { expanded: true } });

    // 模拟刷新：内存里清回默认，再从存储水合。
    stubStored(raw);
    useBotDetailPanelStore.setState({ expanded: false });
    await useBotDetailPanelStore.persist.rehydrate();
    expect(useBotDetailPanelStore.getState().expanded).toBe(true);
  });
});
