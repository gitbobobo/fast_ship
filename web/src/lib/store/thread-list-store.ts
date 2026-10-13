import { create } from "zustand";
import {
  DEFAULT_THREAD_FILTERS,
  THREAD_PAGE_SIZE,
  type ThreadFilters,
} from "@/lib/threads";

interface ThreadListState {
  filters: ThreadFilters;
  limit: number;
  setFilters: (patch: Partial<ThreadFilters>) => void;
  resetFilters: () => void;
  loadMore: () => void;
}

/**
 * 线程网格的筛选与已加载条数。放在内存中，从详情返回时网格与滚动位置一并恢复；
 * 刷新页面后回到默认视图。
 */
export const useThreadListStore = create<ThreadListState>()((set) => ({
  filters: DEFAULT_THREAD_FILTERS,
  limit: THREAD_PAGE_SIZE,
  setFilters: (patch) =>
    set((state) => ({
      filters: { ...state.filters, ...patch },
      limit: THREAD_PAGE_SIZE,
    })),
  resetFilters: () =>
    set({ filters: DEFAULT_THREAD_FILTERS, limit: THREAD_PAGE_SIZE }),
  loadMore: () =>
    set((state) => ({ limit: state.limit + THREAD_PAGE_SIZE })),
}));
