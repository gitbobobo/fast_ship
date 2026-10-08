import { useDeferredValue, useEffect, useMemo, useState } from "react";
import { useSearchParams } from "react-router";
import { Images, Search, Upload } from "lucide-react";
import { Header } from "@/components/layout/header";
import { HeaderActions } from "@/components/layout/header-actions";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { Input } from "@/components/ui/input";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { Tabs, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { Card, CardContent } from "@/components/ui/card";
import { Skeleton } from "@/components/ui/skeleton";
import { UploadScreenshotsDialog } from "@/components/screenshots/upload-screenshots-dialog";
import { ScreenshotLightbox } from "@/components/screenshots/screenshot-lightbox";
import { screenshotApi } from "@/lib/api/screenshots";
import { useProjects } from "@/lib/hooks/use-projects";
import { useScreenshotScreens } from "@/lib/hooks/use-screenshots";
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

export default function ScreenshotsPage() {
  const { data: projectsData, isLoading: projectsLoading } = useProjects();
  const projects = useMemo(
    () => projectsData?.items ?? [],
    [projectsData?.items],
  );
  const [searchParams] = useSearchParams();
  const urlProjectId = searchParams.get("project");
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

  const [tab, setTab] = useState<string>(SCREENSHOT_TAB_ALL);
  const [search, setSearch] = useState("");
  const deferredSearch = useDeferredValue(search);
  const [uploadOpen, setUploadOpen] = useState(false);
  const [openScreenId, setOpenScreenId] = useState<string | null>(null);

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

  // 项目切换时回到「全部」并关闭预览
  useEffect(() => {
    setTab(SCREENSHOT_TAB_ALL);
    setSearch("");
    setOpenScreenId(null);
  }, [activeProjectId]);

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
      <div className="p-4 md:p-6 space-y-6">
        <div>
          <div className="mb-4 flex flex-wrap items-center justify-between gap-3">
            {projectsLoading ? (
              <Skeleton className="h-10 w-64 rounded-md" />
            ) : projects.length === 0 ? (
              <p className="text-sm text-muted-foreground">暂无项目</p>
            ) : (
              <Select
                value={activeProjectId}
                onValueChange={(value) => {
                  const nextValue = value ?? "";
                  setSelectedProjectId(nextValue);
                  setLastSelectedProjectId(nextValue || null);
                }}
              >
                <SelectTrigger className="w-64">
                  <SelectValue placeholder="请选择项目">
                    {projects.find((p) => p.id === activeProjectId)?.name}
                  </SelectValue>
                </SelectTrigger>
                <SelectContent>
                  {projects.map((project) => (
                    <SelectItem key={project.id} value={project.id}>
                      {project.name}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            )}

            {items.length > 0 && (
              <div className="relative w-full sm:w-64">
                <Search className="pointer-events-none absolute top-1/2 left-2.5 h-4 w-4 -translate-y-1/2 text-muted-foreground" />
                <Input
                  value={search}
                  onChange={(e) => setSearch(e.target.value)}
                  className="pl-8"
                  placeholder="搜索名称或界面标识"
                />
              </div>
            )}
          </div>

          {items.length > 0 && (
            <Tabs
              value={activeTab}
              onValueChange={(value) => setTab(value)}
              className="mb-4"
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
          )}

          {projectsLoading || screensLoading ? (
            <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-3 xl:grid-cols-4">
              {Array.from({ length: 4 }).map((_, i) => (
                <Skeleton key={i} className="h-48 rounded-lg" />
              ))}
            </div>
          ) : projects.length === 0 ? (
            <Card>
              <CardContent className="flex flex-col items-center py-10">
                <Images className="mb-3 h-10 w-10 text-muted-foreground/50" />
                <p className="text-sm text-muted-foreground">暂无项目</p>
              </CardContent>
            </Card>
          ) : items.length === 0 ? (
            <Card>
              <CardContent className="flex flex-col items-center py-10">
                <Images className="mb-3 h-10 w-10 text-muted-foreground/50" />
                <p className="text-sm text-muted-foreground">
                  该项目暂无截图，点击右上角「上传截图」开始
                </p>
              </CardContent>
            </Card>
          ) : visibleScreens.length === 0 ? (
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
          ) : (
            <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-3 xl:grid-cols-4">
              {visibleScreens.map((screen) => (
                <Card
                  key={screen.id}
                  className="cursor-pointer transition-colors hover:border-primary/50"
                  onClick={() => setOpenScreenId(screen.id)}
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
