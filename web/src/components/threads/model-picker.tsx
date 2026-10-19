import {
  useEffect,
  useLayoutEffect,
  useMemo,
  useRef,
  useState,
  type ReactNode,
} from "react";
import { Check, ChevronDown, Search, Star } from "lucide-react";
import {
  Popover,
  PopoverContent,
  PopoverTrigger,
} from "@/components/ui/popover";
import {
  modelFavoriteKey,
  useModelFavoritesStore,
} from "@/lib/store/model-favorites-store";
import type {
  ModelChoice,
  ModelPick,
  ProviderChoice,
  ResolvedModel,
} from "@/lib/threads-compose";
import { cn } from "@/lib/utils";
import { ProviderIcon } from "./provider-icon";
import { CHIP } from "./thread-chips";

// 哨兵值不能是合法的 provider 实例 ID（实例 ID 首字符必须是字母），
// 否则 instanceId 叫 favorites 的 provider 会和收藏 rail 撞 key。
const FAVORITES_RAIL = "__favorites__";

/** 面板默认 rail：有可用收藏时收藏 rail，否则当前 provider，再到第一个 provider。 */
function pickDefaultRail(
  providers: ProviderChoice[],
  activeInstanceId: string | undefined,
  favorites: string[],
): string {
  const favoriteSet = new Set(favorites);
  const hasAvailableFavorite = providers.some((provider) =>
    provider.models.some((model) =>
      favoriteSet.has(modelFavoriteKey(provider.instanceId, model.slug)),
    ),
  );
  if (hasAvailableFavorite) return FAVORITES_RAIL;
  // activeInstanceId 可能来自已失效的持久化选择（provider 已不存在），
  // 不校验会直接落在空 rail 上。
  const active = providers.find(
    (provider) => provider.instanceId === activeInstanceId,
  );
  return active?.instanceId ?? providers[0]?.instanceId ?? FAVORITES_RAIL;
}

interface FlatModel {
  provider: ProviderChoice;
  model: ModelChoice;
}

/** 导航条目：固定项（如「项目默认」）与模型行都参与 ↑/↓/Enter。 */
type PickerEntry =
  | { kind: "leading" }
  | { kind: "model"; item: FlatModel };

function modelPickOf(item: FlatModel): ModelPick {
  return {
    instanceId: item.provider.instanceId,
    model: item.model.slug,
    efforts: {},
  };
}

/**
 * 行内副标题：provider 名，带上游子 provider 时补 ` · <subProvider>`
 * （如 `Pi · commandcode`），对齐 T3 Code。搜索命中同一字符串。
 *
 * 子 provider 名本身读作「provider 名 + 限定词」时（OpenCode / OpenCode Zen）
 * 只显示子 provider——拼起来会变成「OpenCode · OpenCode Zen」。判据与 T3 的
 * `providerIconUtils.getProviderRowLabel` 一致。
 */
function modelSubtitle(provider: ProviderChoice, model: ModelChoice) {
  const sub = model.subProvider?.trim();
  if (!sub) return provider.name;
  const name = provider.name.trim();
  if (name && new RegExp(`^${escapeRegExp(name)}\\s+\\S`, "i").test(sub)) {
    return sub;
  }
  return `${provider.name} · ${sub}`;
}

function escapeRegExp(value: string) {
  return value.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
}

export interface ModelPickerProps {
  providers: ProviderChoice[];
  resolved: ResolvedModel;
  onPick: (pick: ModelPick) => void;
  disabled?: boolean;
  /** 触发器 aria-label，默认 "模型"。 */
  ariaLabel?: string;
  /** 覆盖触发器显示文本。 */
  triggerLabel?: ReactNode;
  /**
   * 面板顶部固定项（自动化的「项目默认」用）：不受搜索/rail 过滤影响，
   * 点击调用 onSelect 并关闭。selected 为 true 时触发器默认显示其 label
   * （仍可被 triggerLabel 覆盖）。
   */
  leadingOption?: { label: string; selected: boolean; onSelect: () => void };
  className?: string;
}

/** 单选模型选择器：provider rail + 搜索 + 模型列表，点行即选并关闭。 */
export function ModelPicker({
  providers,
  resolved,
  onPick,
  disabled,
  ariaLabel,
  triggerLabel,
  leadingOption,
  className,
}: ModelPickerProps) {
  const [open, setOpen] = useState(false);
  const label =
    triggerLabel ??
    (leadingOption?.selected ? leadingOption.label : undefined) ??
    resolved.model?.name ??
    "默认模型";
  return (
    <Popover open={open} onOpenChange={setOpen}>
      <PopoverTrigger
        className={cn(CHIP, className)}
        aria-label={ariaLabel ?? "模型"}
        disabled={disabled}
      >
        <ProviderIcon providerInstanceId={resolved.provider?.instanceId ?? ""} />
        <span className="truncate">{label}</span>
        <ChevronDown className="h-3 w-3 shrink-0 text-muted-foreground" />
      </PopoverTrigger>
      <PopoverContent className="w-auto overflow-hidden p-0">
        <ModelPickerPanel
          providers={providers}
          activeInstanceId={resolved.provider?.instanceId}
          isSelected={(item) =>
            // 「项目默认」选中时不再把回落模型也标成选中（否则两条
            // aria-selected=true，看起来像第二个显式选择）。
            !leadingOption?.selected &&
            resolved.provider?.instanceId === item.provider.instanceId &&
            resolved.model?.slug === item.model.slug
          }
          onSelect={(item) => {
            onPick(modelPickOf(item));
            setOpen(false);
          }}
          leadingOption={leadingOption}
          onRequestClose={() => setOpen(false)}
        />
      </PopoverContent>
    </Popover>
  );
}

export interface ModelMultiPickerProps {
  providers: ProviderChoice[];
  selected: ModelPick[];
  onToggle: (pick: ModelPick) => void;
  disabled?: boolean;
  ariaLabel?: string;
  className?: string;
}

/** 多选模型选择器：点行切换选中、面板不关闭，不响应数字键直选。 */
export function ModelMultiPicker({
  providers,
  selected,
  onToggle,
  disabled,
  ariaLabel,
  className,
}: ModelMultiPickerProps) {
  const [open, setOpen] = useState(false);
  const selectedKeys = useMemo(
    () =>
      new Set(
        selected.map((pick) => modelFavoriteKey(pick.instanceId, pick.model)),
      ),
    [selected],
  );
  const names = selected.map(
    (pick) =>
      providers
        .find((p) => p.instanceId === pick.instanceId)
        ?.models.find((m) => m.slug === pick.model)?.name ?? pick.model,
  );
  const label =
    names.length === 0
      ? "选择模型"
      : names.slice(0, 2).join("、") +
        (names.length > 2 ? ` +${names.length - 2}` : "");
  return (
    <Popover open={open} onOpenChange={setOpen}>
      <PopoverTrigger
        className={cn(CHIP, className)}
        aria-label={ariaLabel ?? "模型"}
        disabled={disabled}
      >
        <ProviderIcon providerInstanceId={selected[0]?.instanceId ?? ""} />
        <span className="truncate">{label}</span>
        <ChevronDown className="h-3 w-3 shrink-0 text-muted-foreground" />
      </PopoverTrigger>
      <PopoverContent className="w-auto overflow-hidden p-0">
        <ModelPickerPanel
          providers={providers}
          activeInstanceId={selected[0]?.instanceId}
          isSelected={(item) =>
            selectedKeys.has(
              modelFavoriteKey(item.provider.instanceId, item.model.slug),
            )
          }
          onSelect={(item) => onToggle(modelPickOf(item))}
          multiple
          onRequestClose={() => setOpen(false)}
        />
      </PopoverContent>
    </Popover>
  );
}

interface ModelPickerPanelProps {
  providers: ProviderChoice[];
  /** rail 默认选中的 provider（通常是当前 resolved 的实例）。 */
  activeInstanceId?: string;
  isSelected: (item: FlatModel) => boolean;
  onSelect: (item: FlatModel) => void;
  multiple?: boolean;
  leadingOption?: { label: string; selected: boolean; onSelect: () => void };
  onRequestClose: () => void;
}

/**
 * 弹出面板：左 rail（收藏 + 各 provider）+ 搜索框 + 模型列表。
 * 键盘：↑/↓ 移动高亮（环绕）、Enter 选中高亮行、Esc 关闭；
 * 单选另支持 ⌘N/Ctrl+N 直选第 N 条可见行。
 */
function ModelPickerPanel({
  providers,
  activeInstanceId,
  isSelected,
  onSelect,
  multiple = false,
  leadingOption,
  onRequestClose,
}: ModelPickerPanelProps) {
  const favorites = useModelFavoritesStore((s) => s.favorites);
  const toggleFavorite = useModelFavoritesStore((s) => s.toggleFavorite);
  const favoritesSet = useMemo(() => new Set(favorites), [favorites]);
  // rail：挂载时（即打开时）算一次默认值；用户手动点过后不再自动改。
  // providers 为空时打开（自动化弹框还在加载 composer-options）会在挂载时
  // 占位到收藏 rail，等 providers 到达后重算一次，不会停在空列表上。
  const [rail, setRail] = useState(() =>
    pickDefaultRail(providers, activeInstanceId, favorites),
  );
  const userPickedRail = useRef(false);
  const providersPending = useRef(providers.length === 0);
  useEffect(() => {
    if (userPickedRail.current || providers.length === 0) return;
    if (providersPending.current) {
      providersPending.current = false;
      setRail(pickDefaultRail(providers, activeInstanceId, favorites));
      return;
    }
    // providers 一直在：只校正已失效的 rail（不在列表里的 provider）。
    setRail((current) =>
      current === FAVORITES_RAIL ||
      providers.some((provider) => provider.instanceId === current)
        ? current
        : pickDefaultRail(providers, activeInstanceId, favorites),
    );
  }, [providers, activeInstanceId, favorites]);
  const pickRail = (key: string) => {
    userPickedRail.current = true;
    setRail(key);
  };
  const [query, setQuery] = useState("");
  const [highlight, setHighlight] = useState(0);
  const searchRef = useRef<HTMLInputElement>(null);
  const listRef = useRef<HTMLDivElement>(null);

  const flat = useMemo<FlatModel[]>(
    () =>
      providers.flatMap((provider) =>
        provider.models.map((model) => ({ provider, model })),
      ),
    [providers],
  );
  const searching = query.trim().length > 0;
  const visible = useMemo<FlatModel[]>(() => {
    if (searching) {
      const q = query.trim().toLowerCase();
      return flat.filter(
        ({ provider, model }) =>
          model.name.toLowerCase().includes(q) ||
          modelSubtitle(provider, model).toLowerCase().includes(q),
      );
    }
    if (rail === FAVORITES_RAIL) {
      return flat.filter(({ provider, model }) =>
        favoritesSet.has(modelFavoriteKey(provider.instanceId, model.slug)),
      );
    }
    return flat.filter((item) => item.provider.instanceId === rail);
  }, [flat, searching, query, rail, favoritesSet]);
  // 导航条目 = 固定项（如有）+ 可见模型行；⌘N 只映射模型行，固定项不占快捷键。
  const entries = useMemo<PickerEntry[]>(() => {
    const models = visible.map((item): PickerEntry => ({ kind: "model", item }));
    return leadingOption ? [{ kind: "leading" }, ...models] : models;
  }, [leadingOption, visible]);
  const focused = Math.min(highlight, Math.max(entries.length - 1, 0));

  const focusSearch = () => searchRef.current?.focus({ preventScroll: true });
  // 打开后焦点落在搜索框；rAF/timeout 兜底等 Popover 自身的焦点转移结束。
  useLayoutEffect(() => {
    focusSearch();
    const frame = window.requestAnimationFrame(focusSearch);
    const timer = window.setTimeout(focusSearch, 0);
    return () => {
      window.cancelAnimationFrame(frame);
      window.clearTimeout(timer);
    };
  }, []);

  // 列表内容变化（换 rail、改搜索、增删收藏）后高亮回到第一行。
  useLayoutEffect(() => {
    setHighlight(0);
  }, [rail, query, favoritesSet]);

  // 高亮行滚进可视区。
  useLayoutEffect(() => {
    listRef.current
      ?.querySelector('[data-highlighted="true"]')
      ?.scrollIntoView?.({ block: "nearest" });
  }, [focused]);

  // 键盘监听挂 window（捕获阶段）：覆盖搜索框/rail/列表任一焦点位置；
  // Esc 直接 stopPropagation，避免连带关闭外层 Dialog。
  useEffect(() => {
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.defaultPrevented || event.repeat) return;
      // IME 组合中（中日韩输入未确认）的 Enter/方向键不当作选择操作。
      if (event.isComposing || event.keyCode === 229) return;
      const onButton =
        event.target instanceof HTMLElement &&
        event.target.closest("button") !== null;
      if (event.metaKey || event.ctrlKey || event.altKey) return;
      if (event.key === "ArrowDown" || event.key === "ArrowUp") {
        if (entries.length === 0) return;
        event.preventDefault();
        const delta = event.key === "ArrowDown" ? 1 : -1;
        setHighlight(
          (h) =>
            (Math.min(h, entries.length - 1) + delta + entries.length) %
            entries.length,
        );
      } else if (event.key === "Enter") {
        // rail/收藏按钮的 Enter 交给按钮自身（触发其点击）。
        if (onButton) return;
        const entry = entries[focused];
        if (entry?.kind === "leading") {
          event.preventDefault();
          leadingOption?.onSelect();
          onRequestClose();
        } else if (entry) {
          event.preventDefault();
          onSelect(entry.item);
        }
      } else if (event.key === "Escape") {
        event.preventDefault();
        event.stopPropagation();
        onRequestClose();
      }
    };
    window.addEventListener("keydown", onKeyDown, true);
    return () => window.removeEventListener("keydown", onKeyDown, true);
  }, [entries, focused, onSelect, leadingOption, onRequestClose]);

  const railButton = (key: string, label: string, icon: ReactNode) => (
    <button
      key={key}
      type="button"
      aria-label={label}
      aria-pressed={rail === key}
      onClick={() => {
        pickRail(key);
        focusSearch();
      }}
      className="relative isolate flex aspect-square w-full cursor-pointer items-center justify-center rounded-md transition-colors hover:bg-foreground/10 focus-visible:bg-foreground/10 focus-visible:outline-none"
    >
      {icon}
      {rail === key && (
        <span
          aria-hidden
          className="pointer-events-none absolute -right-1 top-1/2 h-5 w-0.5 -translate-y-1/2 rounded-l-full bg-primary"
        />
      )}
    </button>
  );

  const renderRow = (item: FlatModel, index: number) => {
    const key = modelFavoriteKey(item.provider.instanceId, item.model.slug);
    const favorite = favoritesSet.has(key);
    const selected = isSelected(item);
    return (
      <div
        key={key}
        role="option"
        aria-selected={selected}
        data-highlighted={index === focused || undefined}
        className="flex cursor-pointer items-center gap-2 rounded-md px-2 py-1.5 select-none data-[highlighted]:bg-accent"
        onMouseDown={(event) => event.preventDefault()}
        onMouseEnter={() => setHighlight(index)}
        onClick={() => onSelect(item)}
      >
        <div className="min-w-0 flex-1 text-left">
          <div className="truncate text-xs font-medium leading-snug">
            {item.model.name}
          </div>
          <div className="mt-0.5 flex items-center gap-1.5">
            <ProviderIcon
              providerInstanceId={item.provider.instanceId}
              className="size-3"
            />
            <span className="truncate text-xs leading-snug text-muted-foreground">
              {modelSubtitle(item.provider, item.model)}
            </span>
          </div>
        </div>
        <div className="flex shrink-0 items-center gap-1.5">
          {selected && <Check className="size-3.5" aria-hidden />}
          <button
            type="button"
            aria-label={
              favorite
                ? `取消收藏 ${item.model.name}`
                : `收藏 ${item.model.name}`
            }
            className="-mr-1 shrink-0 cursor-pointer rounded p-0.5 text-muted-foreground transition-colors hover:text-foreground"
            onClick={(event) => {
              event.stopPropagation();
              toggleFavorite(key);
            }}
          >
            <Star
              aria-hidden
              className={cn(
                "size-3.5",
                favorite && "fill-current text-amber-500",
              )}
            />
          </button>
        </div>
      </div>
    );
  };

  return (
    <div
      className="flex h-[min(22rem,var(--available-height))] w-80 flex-row overflow-hidden"
      data-slot="model-picker-panel"
    >
      {/* 左 rail：收藏 + 每个 provider 一项，选中项右侧有高亮条。 */}
      <div
        className="flex w-9 shrink-0 flex-col gap-2.5 overflow-y-auto overscroll-contain border-r border-border/70 bg-muted/30 p-1"
        aria-label="Providers"
      >
        {railButton(
          FAVORITES_RAIL,
          "收藏",
          <Star className="size-4 shrink-0 fill-current" aria-hidden />,
        )}
        <div className="border-b border-border/70" aria-hidden />
        {providers.map((provider) =>
          railButton(
            provider.instanceId,
            provider.name,
            <ProviderIcon
              providerInstanceId={provider.instanceId}
              className="size-5"
            />,
          ),
        )}
      </div>

      {/* 搜索 + 列表 */}
      <div className="flex min-h-0 min-w-0 flex-1 flex-col">
        <div className="flex items-center gap-2 border-b border-border/70 px-2.5">
          <Search className="size-3.5 shrink-0 text-muted-foreground" aria-hidden />
          <input
            ref={searchRef}
            type="text"
            aria-label="搜索模型"
            placeholder="搜索模型…"
            value={query}
            onChange={(event) => setQuery(event.target.value)}
            className="h-8 w-full bg-transparent text-xs outline-none placeholder:text-muted-foreground"
          />
        </div>
        <div
          ref={listRef}
          role="listbox"
          aria-label="模型列表"
          aria-multiselectable={multiple || undefined}
          className="min-h-0 flex-1 overflow-y-auto overscroll-contain p-1"
        >
          {entries.map((entry, index) =>
            entry.kind === "leading" ? (
              <div
                key="leading"
                role="option"
                aria-selected={leadingOption?.selected ?? false}
                data-highlighted={index === focused || undefined}
                className="flex cursor-pointer items-center gap-2 rounded-md px-2 py-1.5 select-none data-[highlighted]:bg-accent"
                onMouseDown={(event) => event.preventDefault()}
                onMouseEnter={() => setHighlight(index)}
                onClick={() => {
                  leadingOption?.onSelect();
                  onRequestClose();
                }}
              >
                <div className="min-w-0 flex-1 text-left">
                  <div className="truncate text-xs font-medium leading-snug">
                    {leadingOption?.label}
                  </div>
                </div>
                {leadingOption?.selected && (
                  <Check className="size-3.5 shrink-0" aria-hidden />
                )}
              </div>
            ) : (
              renderRow(entry.item, index)
            ),
          )}
          {visible.length === 0 && (
            <p className="px-2 py-6 text-center text-xs text-muted-foreground">
              没有匹配的模型
            </p>
          )}
        </div>
      </div>
    </div>
  );
}
