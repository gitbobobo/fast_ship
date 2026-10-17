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

const FAVORITES_RAIL = "favorites";
/** 数字键直选只覆盖前 9 条可见行。 */
const JUMP_LIMIT = 9;

interface FlatModel {
  provider: ProviderChoice;
  model: ModelChoice;
}

/** macOS 用 ⌘、其他平台用 Ctrl；happy-dom 的 navigator.platform/userAgent 也可用。 */
function isMacPlatform() {
  return /mac|iphone|ipad|ipod/i.test(
    `${navigator.platform} ${navigator.userAgent}`,
  );
}

function modelPickOf(item: FlatModel): ModelPick {
  return {
    instanceId: item.provider.instanceId,
    model: item.model.slug,
    efforts: {},
  };
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
  const [rail, setRail] = useState(() => {
    // 有可用收藏时默认落在收藏 rail（对齐 T3），否则落在当前 provider。
    const favoriteSet = new Set(favorites);
    const hasAvailableFavorite = providers.some((provider) =>
      provider.models.some((model) =>
        favoriteSet.has(modelFavoriteKey(provider.instanceId, model.slug)),
      ),
    );
    return hasAvailableFavorite
      ? FAVORITES_RAIL
      : (activeInstanceId ?? providers[0]?.instanceId ?? FAVORITES_RAIL);
  });
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
          provider.name.toLowerCase().includes(q),
      );
    }
    if (rail === FAVORITES_RAIL) {
      return flat.filter(({ provider, model }) =>
        favoritesSet.has(modelFavoriteKey(provider.instanceId, model.slug)),
      );
    }
    return flat.filter((item) => item.provider.instanceId === rail);
  }, [flat, searching, query, rail, favoritesSet]);
  const focused = Math.min(highlight, Math.max(visible.length - 1, 0));

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
      if ((event.metaKey || event.ctrlKey) && !event.altKey && !event.shiftKey) {
        if (!multiple && /^[1-9]$/.test(event.key)) {
          const item = visible[Number(event.key) - 1];
          if (item) {
            event.preventDefault();
            event.stopPropagation();
            onSelect(item);
          }
        }
        return;
      }
      if (event.metaKey || event.ctrlKey || event.altKey) return;
      if (event.key === "ArrowDown" || event.key === "ArrowUp") {
        if (visible.length === 0) return;
        event.preventDefault();
        const delta = event.key === "ArrowDown" ? 1 : -1;
        setHighlight(
          (h) =>
            (Math.min(h, visible.length - 1) + delta + visible.length) %
            visible.length,
        );
      } else if (event.key === "Enter") {
        // rail/收藏按钮的 Enter 交给按钮自身（触发其点击）。
        if (onButton) return;
        const item = visible[focused];
        if (item) {
          event.preventDefault();
          onSelect(item);
        }
      } else if (event.key === "Escape") {
        event.preventDefault();
        event.stopPropagation();
        onRequestClose();
      }
    };
    window.addEventListener("keydown", onKeyDown, true);
    return () => window.removeEventListener("keydown", onKeyDown, true);
  }, [visible, focused, multiple, onSelect, onRequestClose]);

  const railButton = (key: string, label: string, icon: ReactNode) => (
    <button
      key={key}
      type="button"
      aria-label={label}
      aria-pressed={rail === key}
      onClick={() => {
        setRail(key);
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
    const jumpLabel =
      !multiple && index < JUMP_LIMIT
        ? isMacPlatform()
          ? `⌘${index + 1}`
          : `Ctrl+${index + 1}`
        : null;
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
              {item.provider.name}
            </span>
          </div>
        </div>
        <div className="flex shrink-0 items-center gap-1.5">
          {selected && <Check className="size-3.5" aria-hidden />}
          {jumpLabel && (
            <kbd className="rounded border border-border bg-muted px-1 py-0.5 font-mono text-[10px] leading-none text-muted-foreground">
              {jumpLabel}
            </kbd>
          )}
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
    <div className="flex max-h-80 w-80 flex-row overflow-hidden" data-slot="model-picker-panel">
      {/* 左 rail：收藏 + 每个 provider 一项，选中项右侧有高亮条。 */}
      <div
        className="flex w-9 shrink-0 flex-col gap-1 overflow-y-auto overscroll-contain border-r border-border/70 bg-muted/30 p-1"
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
      <div className="flex min-w-0 flex-1 flex-col">
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
          className="max-h-72 overflow-y-auto overscroll-contain p-1"
        >
          {leadingOption && (
            <div
              role="option"
              aria-selected={leadingOption.selected}
              className="flex cursor-pointer items-center gap-2 rounded-md px-2 py-1.5 select-none hover:bg-accent"
              onMouseDown={(event) => event.preventDefault()}
              onClick={() => {
                leadingOption.onSelect();
                onRequestClose();
              }}
            >
              <div className="min-w-0 flex-1 text-left">
                <div className="truncate text-xs font-medium leading-snug">
                  {leadingOption.label}
                </div>
              </div>
              {leadingOption.selected && (
                <Check className="size-3.5 shrink-0" aria-hidden />
              )}
            </div>
          )}
          {visible.map(renderRow)}
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
