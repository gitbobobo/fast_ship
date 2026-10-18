import { create } from "zustand";
import { persist } from "zustand/middleware";

/** 收藏键：instanceId 里可能含 `_`/`-`，用 `::` 分隔避免歧义。 */
export function modelFavoriteKey(instanceId: string, slug: string) {
  return `${instanceId}::${slug}`;
}

interface ModelFavoritesState {
  /** 已收藏模型的 key 列表（modelFavoriteKey 格式）。 */
  favorites: string[];
  toggleFavorite: (key: string) => void;
}

export const useModelFavoritesStore = create<ModelFavoritesState>()(
  persist(
    (set) => ({
      favorites: [],
      toggleFavorite: (key) =>
        set((state) => ({
          favorites: state.favorites.includes(key)
            ? state.favorites.filter((item) => item !== key)
            : [...state.favorites, key],
        })),
    }),
    {
      name: "fast-ship-model-favorites",
    },
  ),
);
