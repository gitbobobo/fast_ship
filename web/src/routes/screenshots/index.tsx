import { useDeferredValue, useEffect, useMemo, useRef, useState } from "react";
import { useSearchParams } from "react-router";
import { Images, Search, Upload } from "lucide-react";
import { toast } from "sonner";
import { Header } from "@/components/layout/header";
import { HeaderActions } from "@/components/layout/header-actions";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Skeleton } from "@/components/ui/skeleton";
import { ProjectSwitcher } from "@/components/projects/project-switcher";
import { UploadScreenshotsDialog } from "@/components/screenshots/upload-screenshots-dialog";
import { ScreenshotLightbox } from "@/components/screenshots/screenshot-lightbox";
import { DimensionMultiSelect } from "@/components/screenshots/dimension-multi-select";
import { LaunchAnnotationThreadDialog } from "@/components/screenshots/launch-annotation-thread-dialog";
import {
  ScreenshotCanvas,
  type CanvasLocateRequest,
} from "@/components/screenshots/canvas/screenshot-canvas";
import { copyWithToast } from "@/lib/copy";
import { buildAnnotationPrompt } from "@/lib/annotation-prompt";
import {
  canonicalizeScreenshotLocale,
  decodeDimensionFilter,
  deriveDimensionOptions,
  encodeDimensionFilter,
  filterAnnotationsByDimensions,
  filterScreensByDimensions,
  filterScreensByQuery,
  SCREENSHOT_NONE,
} from "@/lib/screenshots";
import { useProjects } from "@/lib/hooks/use-projects";
import { useScreenshotScreens } from "@/lib/hooks/use-screenshots";
import { useScreenshotAnnotations } from "@/lib/hooks/use-screenshot-annotations";
import { getActiveProjectId } from "@/routes/board/lib/utils";
import { useProjectPreferenceStore } from "@/lib/store/project-preference-store";

export default function ScreenshotsPage() {
  const { data: projectsData, isLoading: projectsLoading } = useProjects();
  const projects = useMemo(
    () => projectsData?.items ?? [],
    [projectsData?.items],
  );
  const [searchParams, setSearchParams] = useSearchParams();
  const urlProjectId = searchParams.get("project");
  const urlAnnotationId = searchParams.get("annotation");
  const { lastSelectedProjectId, setLastSelectedProjectId } =
    useProjectPreferenceStore();
  const [selectedProjectId, setSelectedProjectId] = useState<string>(
    () => urlProjectId ?? lastSelectedProjectId ?? "",
  );
  const activeProjectId = useMemo(
    () => getActiveProjectId(projects, selectedProjectId, urlProjectId),
    [projects, selectedProjectId, urlProjectId],
  );

  // 当 activeProjectId 回退到第一个项目时（如存储的项目被删除），同步 state 和 store
  useEffect(() => {
    if (!projectsLoading && activeProjectId !== selectedProjectId) {
      setSelectedProjectId(activeProjectId);
      setLastSelectedProjectId(activeProjectId || null);
    }
  }, [
    projectsLoading,
    activeProjectId,
    selectedProjectId,
    setLastSelectedProjectId,
  ]);

  // 筛选条 ↔ URL（逗号分隔）；旧链接里的 ?view= 直接忽略。
  // status 不允许空集：URL 无参数、空值或解析后为空都回落默认「只看未解决」，
  // 与刷新行为一致，也杜绝「open/resolved 全不选 = 隐藏全部标注」的歧义。
  const filters = useMemo(() => {
    const rawStatus = searchParams.get("status");
    const status = decodeDimensionFilter(rawStatus).filter(
      (v) => v === "open" || v === "resolved",
    );
    return {
      // platform 库存是服务端小写化后的值，URL 共享/手输的大写在本地
      // 匹配前折叠，避免画布显示空而批量/提示词打到实际命中的范围
      platform: decodeDimensionFilter(searchParams.get("platform")).map((v) =>
        v.trim().toLowerCase(),
      ),
      // 与服务端 normalizeScreenshotLocale 同一大小写口径，画布/面板/批量
      // payload 都读这份 filters，避免 URL `en-us` 展示空而批量打到 en-US
      locale: decodeDimensionFilter(searchParams.get("locale")).map(
        canonicalizeScreenshotLocale,
      ),
      theme: decodeDimensionFilter(searchParams.get("theme")),
      status: status.length > 0 ? status : ["open"],
    };
  }, [searchParams]);
  const statusScope = useMemo(
    () => ({
      open: filters.status.includes("open"),
      resolved: filters.status.includes("resolved"),
    }),
    [filters.status],
  );

  const patchFilters = (patch: Partial<typeof filters>) => {
    setSearchParams(
      (prev) => {
        const params = new URLSearchParams(prev);
        for (const [name, value] of Object.entries(patch)) {
          const encoded = encodeDimensionFilter(value ?? []);
          if (encoded) {
            params.set(name, encoded);
          } else {
            params.delete(name);
          }
        }
        return params;
      },
      { replace: true },
    );
  };

  const { data: screensData, isLoading: screensLoading } =
    useScreenshotScreens(activeProjectId);
  const allScreens = useMemo(() => screensData?.items ?? [], [screensData]);

  const {
    data: annotationsData,
    isSuccess: annotationsReady,
    isFetching: annotationsFetching,
  } = useScreenshotAnnotations(activeProjectId);
  const allAnnotations = useMemo(
    () => annotationsData ?? [],
    [annotationsData],
  );

  const [uploadOpen, setUploadOpen] = useState(false);
  const [launchOpen, setLaunchOpen] = useState(false);
  const [openScreenId, setOpenScreenId] = useState<string | null>(null);
  const [locateRequest, setLocateRequest] =
    useState<CanvasLocateRequest | null>(null);
  const handledUrlAnnotationRef = useRef<string | null>(null);
  // 截图搜索只收窄画布卡片；标注计数与提示词维持维度口径（列表接口查询
  // 表达不了搜索词），搜索隐藏截图上的标注由画布按 screen 归属自然排除
  const [search, setSearch] = useState("");
  const deferredSearch = useDeferredValue(search);

  // 维度筛选后的截图集合：标注计数、面板与批量动作的公共范围；搜索词只
  // 收窄画布卡片（列表接口表达不了搜索词），不收窄标注口径
  const dimensionScreens = useMemo(
    () => filterScreensByDimensions(allScreens, filters),
    [allScreens, filters],
  );
  const scopeScreenIds = useMemo(
    () => new Set(dimensionScreens.map((s) => s.id)),
    [dimensionScreens],
  );
  const visibleScreens = useMemo(
    () => filterScreensByQuery(dimensionScreens, deferredSearch),
    [dimensionScreens, deferredSearch],
  );
  const screenById = useMemo(
    () => new Map(allScreens.map((s) => [s.id, s])),
    [allScreens],
  );
  const visibleAnnotations = useMemo(
    () => filterAnnotationsByDimensions(allAnnotations, screenById, filters),
    [allAnnotations, screenById, filters],
  );
  const scopeOpenCount = useMemo(
    () => visibleAnnotations.filter((a) => a.status === "open").length,
    [visibleAnnotations],
  );
  const scopeResolvedCount = useMemo(
    () => visibleAnnotations.filter((a) => a.status === "resolved").length,
    [visibleAnnotations],
  );
  // 启动/复制的口径与提示词目标一致：按状态范围收窄。resolved-only 下范围
  // 内没有 open，入口置灰，提示词不会指向显示范围之外的标注。
  const launchOpenCount = useMemo(
    () => (statusScope.open ? scopeOpenCount : 0),
    [scopeOpenCount, statusScope],
  );

  // 筛选选项由全量数据推导（含计数与「未标注」桶）
  const platformOptions = useMemo(
    () => deriveDimensionOptions(allScreens, "platform"),
    [allScreens],
  );
  const localeOptions = useMemo(
    () => deriveDimensionOptions(allScreens, "locale"),
    [allScreens],
  );
  const themeOptions = useMemo(
    () => deriveDimensionOptions(allScreens, "theme"),
    [allScreens],
  );
  const statusOptions = useMemo(
    () => [
      { value: "open", count: scopeOpenCount },
      { value: "resolved", count: scopeResolvedCount },
    ],
    [scopeOpenCount, scopeResolvedCount],
  );

  // 项目切换时关闭预览并重置深链；换项目后摘掉 annotation 参数，
  // 避免指向旧项目的标注。首个空值（项目列表未加载）到首个项目不算切换
  const prevProjectIdRef = useRef(activeProjectId);
  useEffect(() => {
    const prev = prevProjectIdRef.current;
    prevProjectIdRef.current = activeProjectId;
    if (!prev || prev === activeProjectId) return;
    setOpenScreenId(null);
    handledUrlAnnotationRef.current = null;
    if (urlAnnotationId) {
      setSearchParams(
        (prevParams) => {
          const params = new URLSearchParams(prevParams);
          params.delete("annotation");
          return params;
        },
        { replace: true },
      );
    }
  }, [activeProjectId, urlAnnotationId, setSearchParams]);

  // ?annotation=<id>：数据就绪后一次性放宽筛选（把目标截图过滤掉的维度 +
  // 目标标注的状态），再交给画布执行定位。所有放宽合并进同一次 URL 写入：
  // 逐项多次 setSearchParams 会各自基于同一份已捕获参数计算、互相覆盖；
  // 状态也要放宽，否则默认 status=open 会把 Issue 卡片链过来的已解决
  // 标注挡在定位之外。参数处理后保留在 URL 上，画布等真正可定位才消费。
  useEffect(() => {
    if (!urlAnnotationId) return;
    if (handledUrlAnnotationRef.current === urlAnnotationId) return;
    // isSuccess 可能命中的是过期缓存；等当次拉取落地后再判定存在性，
    // 避免旧缓存把有效的定位参数消耗掉
    if (!annotationsReady || annotationsFetching || screensLoading) return;
    handledUrlAnnotationRef.current = urlAnnotationId;
    const target = allAnnotations.find((a) => a.id === urlAnnotationId);
    if (!target) {
      toast.error("标注不存在或已被删除");
      return;
    }
    const patch: Partial<typeof filters> = {};
    const screen = screenById.get(target.screen_id);
    if (screen) {
      // 放宽把目标截图过滤掉的维度（未标注桶用 __none__ 命中）
      for (const dimension of ["platform", "locale", "theme"] as const) {
        const key = screen[dimension] ?? SCREENSHOT_NONE;
        if (filters[dimension].length > 0 && !filters[dimension].includes(key)) {
          patch[dimension] = [];
        }
      }
    }
    if (!filters.status.includes(target.status)) {
      patch.status = [...filters.status, target.status];
    }
    if (Object.keys(patch).length > 0) {
      patchFilters(patch);
    }
    setLocateRequest({ annotationId: target.id, nonce: Date.now() });
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [
    urlAnnotationId,
    annotationsReady,
    annotationsFetching,
    screensLoading,
    allAnnotations,
  ]);

  // 处理标注意见：固定模板 + 当前筛选范围计数；复制不确认，启动走弹框二次确认
  const promptText = useMemo(
    () =>
      buildAnnotationPrompt({
        projectId: activeProjectId,
        filters: {
          platform: filters.platform,
          locale: filters.locale,
          theme: filters.theme,
        },
        openCount: launchOpenCount,
        resolvedCount: scopeResolvedCount,
      }),
    [activeProjectId, filters, launchOpenCount, scopeResolvedCount],
  );

  return (
    <>
      <Header
        title="截图"
        actions={
          activeProjectId ? (
            <HeaderActions
              primary={
                <Button size="sm" onClick={() => setUploadOpen(true)}>
                  <Upload className="mr-1.5 h-3.5 w-3.5" />
                  上传截图
                </Button>
              }
            />
          ) : undefined
        }
      />
      {/* 画布直达：工具行只放项目选择与筛选条，画布贴边占满剩余空间 */}
      <div className="flex h-[calc(100dvh-3.5rem)] flex-col">
        <div className="flex shrink-0 flex-wrap items-center gap-2 border-b px-3 py-2">
          {projectsLoading ? (
            <Skeleton className="h-8 w-64 rounded-md" />
          ) : projects.length === 0 ? (
            <p className="text-sm text-muted-foreground">暂无项目</p>
          ) : (
            <ProjectSwitcher
              value={activeProjectId}
              onValueChange={(nextValue) => {
                setSelectedProjectId(nextValue);
                setLastSelectedProjectId(nextValue || null);
              }}
              projects={projects}
              placeholder="请选择项目"
              className="w-64"
            />
          )}

          {allScreens.length > 0 && (
            <div className="flex min-w-0 flex-wrap items-center gap-1.5">
              <DimensionMultiSelect
                label="平台"
                options={platformOptions}
                value={filters.platform}
                onChange={(next) => patchFilters({ platform: next })}
              />
              <DimensionMultiSelect
                label="语言"
                options={localeOptions}
                value={filters.locale}
                onChange={(next) => patchFilters({ locale: next })}
              />
              <DimensionMultiSelect
                label="亮度"
                options={themeOptions}
                value={filters.theme}
                onChange={(next) => patchFilters({ theme: next })}
              />
              <DimensionMultiSelect
                label="状态"
                options={statusOptions}
                value={filters.status}
                onChange={(next) => patchFilters({ status: next })}
              />
            </div>
          )}

          {allScreens.length > 0 && (
            <div className="relative w-full min-w-0 sm:w-64">
              <Search className="pointer-events-none absolute top-1/2 left-2.5 h-4 w-4 -translate-y-1/2 text-muted-foreground" />
              <Input
                value={search}
                onChange={(e) => setSearch(e.target.value)}
                className="pl-8"
                placeholder="搜索名称或界面标识"
                data-testid="screenshot-search"
              />
            </div>
          )}
        </div>

        <div className="min-h-0 flex-1">
          {projectsLoading || screensLoading ? (
            <div className="flex h-full items-center justify-center">
              <Skeleton className="h-full w-full rounded-none" />
            </div>
          ) : projects.length === 0 ? (
            <div className="flex h-full flex-col items-center justify-center gap-2 text-muted-foreground">
              <Images className="h-10 w-10 text-muted-foreground/50" />
              <p className="text-sm">暂无项目</p>
            </div>
          ) : allScreens.length === 0 ? (
            // 空状态 = 画布中央上传按钮
            <div className="flex h-full flex-col items-center justify-center gap-3 text-muted-foreground">
              <Images className="h-10 w-10 text-muted-foreground/50" />
              <p className="text-sm">该项目还没有截图</p>
              <Button size="sm" onClick={() => setUploadOpen(true)}>
                <Upload className="mr-1.5 h-3.5 w-3.5" />
                上传截图
              </Button>
            </div>
          ) : visibleScreens.length === 0 ? (
            <div className="flex h-full flex-col items-center justify-center gap-2 text-muted-foreground">
              <Images className="h-10 w-10 text-muted-foreground/50" />
              <p className="text-sm">
                {deferredSearch.trim()
                  ? "没有匹配的截图，调整搜索关键词后再试"
                  : "当前筛选下没有截图，调整筛选条后再试"}
              </p>
            </div>
          ) : (
            <ScreenshotCanvas
              projectId={activeProjectId}
              screens={visibleScreens}
              scopeScreenIds={scopeScreenIds}
              annotations={visibleAnnotations}
              annotationsReady={annotationsReady}
              statusScope={statusScope}
              onStatusScopeChange={(scope) =>
                patchFilters({
                  status: [
                    ...(scope.open ? (["open"] as const) : []),
                    ...(scope.resolved ? (["resolved"] as const) : []),
                  ],
                })
              }
              scopeFilter={{
                platform: filters.platform,
                locale: filters.locale,
                theme: filters.theme,
              }}
              openCount={launchOpenCount}
              resetKey={`${activeProjectId}|${searchParams.toString()}`}
              controlsEnabled={openScreenId === null && !uploadOpen && !launchOpen}
              locateRequest={locateRequest}
              onOpenPreview={setOpenScreenId}
              onLaunchThread={() => setLaunchOpen(true)}
              onCopyPrompt={() => void copyWithToast(promptText, "已复制提示词")}
            />
          )}
        </div>
      </div>

      <UploadScreenshotsDialog
        open={uploadOpen}
        onOpenChange={setUploadOpen}
        projectId={activeProjectId}
      />

      {activeProjectId && (
        <LaunchAnnotationThreadDialog
          open={launchOpen}
          onOpenChange={setLaunchOpen}
          projectId={activeProjectId}
          promptText={promptText}
          openCount={launchOpenCount}
        />
      )}

      <ScreenshotLightbox
        open={openScreenId !== null}
        onOpenChange={(open) => {
          if (!open) setOpenScreenId(null);
        }}
        projectId={activeProjectId}
        screens={visibleScreens}
        screenId={openScreenId}
        onNavigate={setOpenScreenId}
      />
    </>
  );
}
