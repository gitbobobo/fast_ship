import { useDeferredValue, useEffect, useMemo, useRef, useState } from "react";
import { useSearchParams } from "react-router";
import { toast } from "sonner";
import { Images, LayoutGrid, List, Search, Upload } from "lucide-react";
import { Header } from "@/components/layout/header";
import { HeaderActions } from "@/components/layout/header-actions";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { Input } from "@/components/ui/input";
import { Tabs, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { Card, CardContent } from "@/components/ui/card";
import { Skeleton } from "@/components/ui/skeleton";
import { ProjectSwitcher } from "@/components/projects/project-switcher";
import { UploadScreenshotsDialog } from "@/components/screenshots/upload-screenshots-dialog";
import { ScreenshotLightbox } from "@/components/screenshots/screenshot-lightbox";
import {
  ScreenshotCanvas,
  type CanvasLocateRequest,
} from "@/components/screenshots/canvas/screenshot-canvas";
import { screenshotApi } from "@/lib/api/screenshots";
import { usePersistedScroll } from "@/lib/hooks/use-persisted-scroll";
import { useProjects } from "@/lib/hooks/use-projects";
import { useScreenshotScreens } from "@/lib/hooks/use-screenshots";
import { useScreenshotAnnotations } from "@/lib/hooks/use-screenshot-annotations";
import {
  SCREENSHOT_TAB_ALL,
  SCREENSHOT_TAB_UNGROUPED,
  decodeGroupTab,
  deriveGroupTabs,
  filterScreensByQuery,
  filterScreensByTab,
  hasUngroupedScreens,
  screenDisplayName,
} from "@/lib/screenshots";
import { getActiveProjectId } from "@/routes/board/lib/utils";
import { useProjectPreferenceStore } from "@/lib/store/project-preference-store";
import { formatRelativeTime } from "@/lib/utils/format";

type ScreenshotsView = "list" | "canvas";

export default function ScreenshotsPage() {
  const { data: projectsData, isLoading: projectsLoading } = useProjects();
  const projects = useMemo(
    () => projectsData?.items ?? [],
    [projectsData?.items],
  );
  const [searchParams, setSearchParams] = useSearchParams();
  const urlProjectId = searchParams.get("project");
  const view: ScreenshotsView =
    searchParams.get("view") === "canvas" ? "canvas" : "list";
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

  const { data: screensData, isLoading: screensLoading } =
    useScreenshotScreens(activeProjectId);
  const items = useMemo(() => screensData?.items ?? [], [screensData]);

  // 列表在内容区内部滚动，位置按项目持久化；画布视图下容器被夹到 0，
  // ready 关闭既挡住把 0 写回存储，也让切回列表时重新恢复位置
  const contentScrollRef = usePersistedScroll<HTMLDivElement>(
    `screenshots:${activeProjectId}`,
    {
      ready: view === "list" && Boolean(activeProjectId) && !screensLoading,
    },
  );

  const [tab, setTab] = useState<string>(SCREENSHOT_TAB_ALL);
  const [search, setSearch] = useState("");
  const deferredSearch = useDeferredValue(search);
  const [uploadOpen, setUploadOpen] = useState(false);
  const [openScreenId, setOpenScreenId] = useState<string | null>(null);
  // 从画布定位到旧版本标注时，预览弹窗要直接选中该版本并高亮该标注
  const [previewVersionId, setPreviewVersionId] = useState<string | null>(null);
  const [previewAnnotationId, setPreviewAnnotationId] = useState<
    string | null
  >(null);
  const [locateRequest, setLocateRequest] =
    useState<CanvasLocateRequest | null>(null);
  const handledUrlAnnotationRef = useRef<string | null>(null);

  const {
    data: annotationsData,
    isSuccess: annotationsReady,
    isFetching: annotationsFetching,
  } = useScreenshotAnnotations(activeProjectId, view === "canvas");
  const annotations = useMemo(() => annotationsData ?? [], [annotationsData]);

  const setView = (next: ScreenshotsView) => {
    setSearchParams(
      (prev) => {
        const params = new URLSearchParams(prev);
        if (next === "canvas") {
          params.set("view", "canvas");
        } else {
          params.delete("view");
          params.delete("annotation");
        }
        return params;
      },
      { replace: true },
    );
  };

  const openPreview = (
    screenId: string,
    versionId?: string,
    annotationId?: string,
  ) => {
    setOpenScreenId(screenId);
    setPreviewVersionId(versionId ?? null);
    setPreviewAnnotationId(annotationId ?? null);
  };
  const closePreview = () => {
    setOpenScreenId(null);
    setPreviewVersionId(null);
    setPreviewAnnotationId(null);
  };

  const groupTabs = useMemo(() => deriveGroupTabs(items), [items]);
  const ungrouped = useMemo(() => hasUngroupedScreens(items), [items]);

  // 选中分组被改空/删掉时回退到「全部」
  const activeTab =
    tab === SCREENSHOT_TAB_ALL ||
    (tab === SCREENSHOT_TAB_UNGROUPED && ungrouped) ||
    groupTabs.includes(tab)
      ? tab
      : SCREENSHOT_TAB_ALL;

  const tabScreens = useMemo(
    () => filterScreensByTab(items, activeTab),
    [items, activeTab],
  );
  const visibleScreens = useMemo(
    () => filterScreensByQuery(tabScreens, deferredSearch),
    [tabScreens, deferredSearch],
  );

  // 项目切换时回到「全部」并关闭预览；深链 annotation 只对当时的项目
  // 生效——换项目后摘掉参数并重置已处理标记，避免指向旧项目的标注。
  // 首个空值（项目列表未加载）到首个项目不算切换
  const prevProjectIdRef = useRef(activeProjectId);
  useEffect(() => {
    const prev = prevProjectIdRef.current;
    prevProjectIdRef.current = activeProjectId;
    if (!prev || prev === activeProjectId) return;
    setTab(SCREENSHOT_TAB_ALL);
    setSearch("");
    setOpenScreenId(null);
    setPreviewVersionId(null);
    setPreviewAnnotationId(null);
    handledUrlAnnotationRef.current = null;
    if (urlAnnotationId) {
      setSearchParams(
        (prev) => {
          const params = new URLSearchParams(prev);
          params.delete("annotation");
          return params;
        },
        { replace: true },
      );
    }
  }, [activeProjectId, urlAnnotationId, setSearchParams]);

  // ?view=canvas&annotation=<id>：数据就绪后把分组 tab 与搜索放宽到能看到
  // 该标注所在界面，再交给画布执行定位；参数处理后保留在 URL 上
  useEffect(() => {
    if (view !== "canvas" || !urlAnnotationId) return;
    if (handledUrlAnnotationRef.current === urlAnnotationId) return;
    // isSuccess 可能命中的是过期缓存；等当次拉取落地后再判定存在性，
    // 避免旧缓存把有效的定位参数消耗掉
    if (!annotationsReady || annotationsFetching || screensLoading) return;
    handledUrlAnnotationRef.current = urlAnnotationId;
    const target = annotations.find((a) => a.id === urlAnnotationId);
    if (!target) {
      toast.error("标注不存在或已被删除");
      return;
    }
    if (!visibleScreens.some((s) => s.id === target.screen_id)) {
      setTab(SCREENSHOT_TAB_ALL);
      setSearch("");
    }
    setLocateRequest({ annotationId: target.id, nonce: Date.now() });
  }, [
    view,
    urlAnnotationId,
    annotationsReady,
    annotationsFetching,
    screensLoading,
    annotations,
    visibleScreens,
  ]);

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
      {/* 两个视图共用固定外壳：工具行不动，列表在内容区内部滚动 */}
      <div className="flex h-[calc(100dvh-3.5rem)] flex-col">
        <div className="flex shrink-0 flex-wrap items-center gap-2 border-b px-3 py-2">
          <div className="flex flex-wrap items-center gap-2">
            {projectsLoading ? (
              <Skeleton className="h-10 w-64 rounded-md" />
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
            <div className="flex items-center gap-0.5 rounded-lg bg-muted p-0.5">
              <Button
                size="sm"
                variant={view === "list" ? "secondary" : "ghost"}
                aria-pressed={view === "list"}
                onClick={() => setView("list")}
              >
                <List className="mr-1 h-3.5 w-3.5" />
                列表
              </Button>
              <Button
                size="sm"
                variant={view === "canvas" ? "secondary" : "ghost"}
                aria-pressed={view === "canvas"}
                onClick={() => setView("canvas")}
              >
                <LayoutGrid className="mr-1 h-3.5 w-3.5" />
                画布
              </Button>
            </div>
          </div>

          {items.length > 0 && (
            <>
              <Tabs
                value={activeTab}
                onValueChange={(value) => setTab(value)}
                className="min-w-0 flex-1"
              >
                <TabsList variant="line" className="overflow-x-auto">
                  <TabsTrigger value={SCREENSHOT_TAB_ALL}>全部</TabsTrigger>
                  {groupTabs.map((tabValue) => (
                    <TabsTrigger key={tabValue} value={tabValue}>
                      {decodeGroupTab(tabValue)}
                    </TabsTrigger>
                  ))}
                  {ungrouped && (
                    <TabsTrigger value={SCREENSHOT_TAB_UNGROUPED}>
                      未分组
                    </TabsTrigger>
                  )}
                </TabsList>
              </Tabs>
              <div className="relative min-w-0 sm:w-64">
                <Search className="pointer-events-none absolute top-1/2 left-2.5 h-4 w-4 -translate-y-1/2 text-muted-foreground" />
                <Input
                  value={search}
                  onChange={(e) => setSearch(e.target.value)}
                  className="pl-8"
                  placeholder="搜索名称或界面标识"
                />
              </div>
            </>
          )}
        </div>

        <div
          ref={contentScrollRef}
          data-testid="screenshots-scroll"
          className="min-h-0 flex-1 overflow-y-auto"
        >
          {projectsLoading || screensLoading ? (
            <div className="grid gap-4 p-4 sm:grid-cols-2 md:p-6 lg:grid-cols-3 xl:grid-cols-4">
              {Array.from({ length: 4 }).map((_, i) => (
                <Skeleton key={i} className="h-48 rounded-lg" />
              ))}
            </div>
          ) : projects.length === 0 ? (
            <div className="p-4 md:p-6">
              <Card>
                <CardContent className="flex flex-col items-center py-10">
                  <Images className="mb-3 h-10 w-10 text-muted-foreground/50" />
                  <p className="text-sm text-muted-foreground">暂无项目</p>
                </CardContent>
              </Card>
            </div>
          ) : items.length === 0 ? (
            <div className="p-4 md:p-6">
              <Card>
                <CardContent className="flex flex-col items-center py-10">
                  <Images className="mb-3 h-10 w-10 text-muted-foreground/50" />
                  <p className="text-sm text-muted-foreground">
                    该项目暂无截图，点击右上角「上传截图」开始
                  </p>
                </CardContent>
              </Card>
            </div>
          ) : visibleScreens.length === 0 ? (
            <div className="p-4 md:p-6">
              <Card>
                <CardContent className="flex flex-col items-center py-10">
                  <Images className="mb-3 h-10 w-10 text-muted-foreground/50" />
                  <p className="text-sm text-muted-foreground">
                    {deferredSearch.trim()
                      ? "没有匹配的截图，调整搜索关键词后再试"
                      : "该分组暂无截图"}
                  </p>
                </CardContent>
              </Card>
            </div>
          ) : view === "canvas" ? (
            <ScreenshotCanvas
              projectId={activeProjectId}
              screens={visibleScreens}
              annotations={annotations}
              annotationsReady={annotationsReady}
              resetKey={`${activeProjectId}|${activeTab}`}
              controlsEnabled={openScreenId === null && !uploadOpen}
              locateRequest={locateRequest}
              onOpenPreview={openPreview}
            />
          ) : (
            <div className="grid gap-4 p-4 sm:grid-cols-2 md:p-6 lg:grid-cols-3 xl:grid-cols-4">
              {visibleScreens.map((screen) => (
                <Card
                  key={screen.id}
                  className="cursor-pointer transition-colors hover:border-primary/50"
                  onClick={() => openPreview(screen.id)}
                  data-testid={`screenshot-card-${screen.id}`}
                >
                  <div className="flex aspect-[4/3] items-center justify-center overflow-hidden bg-muted/30">
                    {screen.latest_version ? (
                      <img
                        src={screenshotApi.contentUrl(screen.latest_version)}
                        alt={screenDisplayName(screen)}
                        className="h-full w-full object-cover"
                        loading="lazy"
                      />
                    ) : (
                      <Images className="h-8 w-8 text-muted-foreground/40" />
                    )}
                  </div>
                  <CardContent className="flex items-center justify-between gap-2 py-3">
                    <div className="min-w-0">
                      <p className="truncate text-sm font-medium">
                        {screenDisplayName(screen)}
                      </p>
                      <p className="text-xs text-muted-foreground">
                        {formatRelativeTime(screen.last_uploaded_at)}
                      </p>
                    </div>
                    <Badge variant="secondary" className="shrink-0">
                      {screen.version_count} 个版本
                    </Badge>
                  </CardContent>
                </Card>
              ))}
            </div>
          )}
        </div>
      </div>

      <UploadScreenshotsDialog
        open={uploadOpen}
        onOpenChange={setUploadOpen}
        projectId={activeProjectId}
      />

      <ScreenshotLightbox
        open={openScreenId !== null}
        onOpenChange={(open) => {
          if (!open) closePreview();
        }}
        projectId={activeProjectId}
        screens={visibleScreens}
        screenId={openScreenId}
        onNavigate={(screenId) => openPreview(screenId)}
        initialVersionId={previewVersionId}
        focusAnnotationId={previewAnnotationId}
      />
    </>
  );
}
