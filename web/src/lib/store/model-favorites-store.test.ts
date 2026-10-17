import { beforeEach, describe, expect, it } from "vitest";
import {
  modelFavoriteKey,
  useModelFavoritesStore,
} from "./model-favorites-store";

describe("model-favorites-store", () => {
  beforeEach(() => {
    useModelFavoritesStore.setState({ favorites: [] });
  });

  it("modelFavoriteKey 用 :: 连接实例与模型", () => {
    expect(modelFavoriteKey("acpRegistry_devin", "gpt-5")).toBe(
      "acpRegistry_devin::gpt-5",
    );
  });

  it("toggleFavorite 有则删、无则加", () => {
    const store = useModelFavoritesStore.getState();
    store.toggleFavorite("a::b");
    expect(useModelFavoritesStore.getState().favorites).toEqual(["a::b"]);
    store.toggleFavorite("a::c");
    expect(useModelFavoritesStore.getState().favorites).toEqual([
      "a::b",
      "a::c",
    ]);
    useModelFavoritesStore.getState().toggleFavorite("a::b");
    expect(useModelFavoritesStore.getState().favorites).toEqual(["a::c"]);
  });
});
