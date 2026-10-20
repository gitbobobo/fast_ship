import type {
  AIProvider,
  CatalogModel,
  CatalogProvider,
} from "@/lib/api/ai-providers";
import type { ModelChoice, ProviderChoice } from "@/lib/threads-compose";

/**
 * 已配置的 AI 提供商 → ModelPicker 的 ProviderChoice：
 * instanceId = ai_providers.id（选中回调即 providerId），rail 按提供商分组，
 * 图标用 models.dev 目录 id 解析；模型清单来自内置目录。
 * 目录里没有模型清单的提供商（custom 或目录缺失）不出现在 picker 里，
 * 由调用方回落到手填模型名。
 */
export function botProviderChoices(
  providers: AIProvider[],
  catalogById: Map<string, CatalogProvider>,
): ProviderChoice[] {
  return providers.flatMap((provider) => {
    const catalog = catalogById.get(provider.catalog_id);
    if (!catalog || catalog.models.length === 0) return [];
    return [
      {
        instanceId: provider.id,
        name: provider.name,
        iconId: provider.catalog_id,
        models: catalog.models.map(toModelChoice),
      },
    ];
  });
}

/** 提供商是否能在 picker 里出模型清单；不能的走手填模型名。 */
export function hasCatalogModels(
  provider: AIProvider,
  catalogById: Map<string, CatalogProvider>,
): boolean {
  return (catalogById.get(provider.catalog_id)?.models.length ?? 0) > 0;
}

function toModelChoice(model: CatalogModel): ModelChoice {
  return {
    slug: model.id,
    name: model.name || model.id,
    isDefault: false,
    descriptors: [],
  };
}
