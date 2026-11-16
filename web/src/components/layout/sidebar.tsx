import { useEffect, useState, type ReactNode } from "react";
import { Link, NavLink, useLocation, useNavigate } from "react-router";
import {
  LayoutDashboard,
  Package,
  Plus,
  Tags,
  Bug,
  Kanban,
  Menu,
  ScrollText,
  FileText,
  Images,
  MessagesSquare,
  Bot,
  GitPullRequest,
} from "lucide-react";
import {
  Tooltip,
  TooltipContent,
  TooltipProvider,
  TooltipTrigger,
} from "@/components/ui/tooltip";
import { Sheet, SheetContent, SheetTrigger } from "@/components/ui/sheet";
import { FastShipLogo } from "@/components/ui/fast-ship-logo";
import { ProjectSwitcher } from "@/components/projects/project-switcher";
import { UserNav } from "@/components/user-nav";
import { useCurrentProjectId } from "@/lib/current-project";
import { useProjects } from "@/lib/hooks/use-projects";
import { projectSwitchPath } from "@/lib/project-nav";
import { useLastBotId } from "@/lib/store/last-bot-store";
import { useProjectPreferenceStore } from "@/lib/store/project-preference-store";
import { useSidebarStore } from "@/lib/store/sidebar-store";
import { cn } from "@/lib/utils";

interface NavItem {
  to: string;
  label: string;
  icon: React.ElementType;
  end?: boolean;
  // 跳转目标被改写成具体页面时（如 /bots/<lastBotId>），NavLink 的
  // to 前缀匹配只认那一页，分区高亮会丢；用它按分区前缀判定激活态。
  activePrefix?: string;
}

// 主入口与智能体组是静态的；「项目内」组的链接按当前项目 ID 动态拼出，
// 当前项目由 useCurrentProjectId 裁决（路径 > 最近选择 > 兜底回落）。
const mainGroupItems: NavItem[] = [
  { to: "/dashboard", label: "仪表盘", icon: LayoutDashboard, end: true },
  { to: "/projects", label: "项目", icon: Package, end: true },
];

// 项目内六个分区：链接目标 = /projects/<当前项目>/<section>，路径直接命中
// 新路由，不再经过旧路径重定向。日志有 runId 子页，不做 end 精确匹配。
const projectSectionItems: {
  section: string;
  label: string;
  icon: NavItem["icon"];
  end?: boolean;
}[] = [
  { section: "issues", label: "问题", icon: Bug, end: true },
  { section: "board", label: "看板", icon: Kanban, end: true },
  { section: "versions", label: "版本", icon: Tags, end: true },
  { section: "documents", label: "文档", icon: FileText, end: true },
  { section: "screenshots", label: "截图", icon: Images, end: true },
  { section: "logs", label: "日志", icon: ScrollText, end: false },
];

const agentGroupItems: NavItem[] = [
  { to: "/threads", label: "线程", icon: MessagesSquare, end: false },
  { to: "/bots", label: "机器人", icon: Bot, end: false },
  { to: "/reviews", label: "审查", icon: GitPullRequest, end: false },
];

function SidebarNavItem({
  item,
  collapsed,
  onNavigate,
}: {
  item: NavItem;
  collapsed: boolean;
  onNavigate?: () => void;
}) {
  const { pathname } = useLocation();
  const link = (
    <NavLink
      to={item.to}
      end={item.end}
      onClick={onNavigate}
      aria-label={item.label}
      className={({ isActive }) =>
        cn(
          "relative flex items-center rounded-md text-sm font-medium transition-all duration-200",
          collapsed ? "justify-center px-2 py-2" : "gap-3 px-3 py-2",
          (
            item.activePrefix
              ? pathname.startsWith(item.activePrefix)
              : isActive
          )
            ? "bg-primary text-primary-foreground"
            : "text-muted-foreground hover:bg-accent hover:text-accent-foreground"
        )
      }
    >
      <item.icon className="h-4 w-4 shrink-0" />
      <span
        className={cn(
          "transition-all duration-200 overflow-hidden whitespace-nowrap",
          collapsed ? "max-w-0 opacity-0" : "max-w-[120px] opacity-100"
        )}
      >
        {item.label}
      </span>
    </NavLink>
  );

  if (collapsed) {
    return (
      <Tooltip>
        <TooltipTrigger render={link} />
        <TooltipContent side="right">{item.label}</TooltipContent>
      </Tooltip>
    );
  }

  return link;
}

function SidebarControl({
  collapsed,
  label,
  children,
}: {
  collapsed: boolean;
  label: string;
  children: ReactNode;
}) {
  if (!collapsed) {
    return <>{children}</>;
  }

  return (
    <Tooltip>
      <TooltipTrigger
        render={<div className="flex justify-center">{children}</div>}
      />
      <TooltipContent side="right">{label}</TooltipContent>
    </Tooltip>
  );
}

function SidebarBottom({ collapsed }: { collapsed: boolean }) {
  const menuSide = collapsed ? "right" : "top";

  return (
    <div
      data-testid="sidebar-bottom"
      className="border-t transition-all duration-200"
    >
      <SidebarControl collapsed={collapsed} label="用户菜单">
        <UserNav menuSide={menuSide} collapsed={collapsed} />
      </SidebarControl>
    </div>
  );
}

// 组标题：展开态显示文字，收起态退化为分隔线（与旧版侧边栏行为一致）。
function NavGroupHeading({
  label,
  collapsed,
}: {
  label: string;
  collapsed: boolean;
}) {
  if (collapsed) {
    return (
      <div data-testid="sidebar-group-separator" className="my-2 border-t" />
    );
  }
  return (
    <div className="px-3 py-1 text-xs text-muted-foreground">{label}</div>
  );
}

// 无项目时的创建入口：跳 /projects 页使用那里的创建对话框，不在侧边栏复制弹层。
function CreateProjectItem({
  collapsed,
  onNavigate,
}: {
  collapsed: boolean;
  onNavigate?: () => void;
}) {
  const link = (
    <Link
      to="/projects"
      onClick={onNavigate}
      aria-label="创建项目"
      className={cn(
        "flex items-center rounded-md text-sm font-medium text-muted-foreground transition-colors hover:bg-accent hover:text-accent-foreground",
        collapsed ? "justify-center px-2 py-2" : "gap-3 px-3 py-2",
      )}
    >
      <Plus className="h-4 w-4 shrink-0" />
      {!collapsed && (
        <span className="overflow-hidden whitespace-nowrap">创建项目</span>
      )}
    </Link>
  );

  if (collapsed) {
    return (
      <Tooltip>
        <TooltipTrigger render={link} />
        <TooltipContent side="right">创建项目</TooltipContent>
      </Tooltip>
    );
  }
  return link;
}

function NavContent({
  collapsed,
  onNavigate,
}: {
  collapsed?: boolean;
  onNavigate?: () => void;
}) {
  // 「机器人」菜单项直跳上次打开的聊天（INT-157）；没打开过就进列表。
  const lastBotId = useLastBotId();
  const navigate = useNavigate();
  const { pathname } = useLocation();
  const setLastSelectedProjectId = useProjectPreferenceStore(
    (state) => state.setLastSelectedProjectId,
  );

  // 当前项目与项目列表：切换器和「项目内」组都依赖它们。
  const projectsQuery = useProjects();
  const projects = projectsQuery.data?.items ?? [];
  const currentProjectId = useCurrentProjectId(projects);
  const currentProject = projects.find((p) => p.id === currentProjectId);
  // 列表就绪且为空：没有可切换的项目，切换器位置换成「创建项目」入口，
  // 「项目内」组一并隐藏；加载中或查询失败（无数据）不误判为无项目。
  const projectListEmpty = projectsQuery.isSuccess && projects.length === 0;
  const showProjectGroup = !projectListEmpty && !!currentProjectId;

  // 切换规则（INT-237）：项目级页面 → projectSwitchPath 延续同名 section
  // （列表页丢页内 query，详情/new/edit/runId 回对应列表页）；全局页无法
  // 延续项目上下文，只记住偏好，侧边栏链接随之更新。
  const handleSwitchProject = (nextId: string) => {
    const to = projectSwitchPath(pathname, nextId);
    if (to) navigate(to);
    else setLastSelectedProjectId(nextId);
  };

  const renderNavItem = (item: NavItem) => (
    <SidebarNavItem
      key={item.to}
      item={{
        ...item,
        to: item.to === "/bots" && lastBotId ? `/bots/${lastBotId}` : item.to,
        activePrefix: item.to === "/bots" ? "/bots" : undefined,
      }}
      collapsed={!!collapsed}
      onNavigate={onNavigate}
    />
  );

  return (
    <div className="flex h-full flex-col">
      <div className="flex h-14 shrink-0 items-center border-b px-4">
        <FastShipLogo className="h-5 w-5 shrink-0" />
        <span
          className={cn(
            "text-lg font-semibold tracking-tight transition-all duration-200 overflow-hidden whitespace-nowrap",
            collapsed
              ? "ml-0 max-w-0 opacity-0"
              : "ml-2 max-w-[120px] opacity-100"
          )}
        >
          Fast Ship
        </span>
      </div>
      <nav
        className={cn(
          "flex-1 overflow-y-auto transition-all duration-200",
          collapsed ? "p-2" : "p-3"
        )}
      >
        <div className="space-y-1">
          {mainGroupItems.map((item) => renderNavItem(item))}
        </div>

        {/* 项目上下文区块：分隔线 + 切换器（或创建入口）+ 「项目内」组 */}
        {(projectListEmpty || currentProjectId) && (
          <div className="mt-4 border-t pt-2">
            {projectListEmpty ? (
              <CreateProjectItem
                collapsed={!!collapsed}
                onNavigate={onNavigate}
              />
            ) : (
              currentProjectId && (
                <SidebarControl
                  collapsed={!!collapsed}
                  label={currentProject?.name ?? "切换项目"}
                >
                  <div className={!collapsed ? "pb-1" : undefined}>
                    <ProjectSwitcher
                      projects={projects}
                      value={currentProjectId}
                      onValueChange={handleSwitchProject}
                      variant={collapsed ? "sidebar-collapsed" : "sidebar"}
                    />
                  </div>
                </SidebarControl>
              )
            )}
            {showProjectGroup && (
              <div className="space-y-1">
                <NavGroupHeading label="项目内" collapsed={!!collapsed} />
                {projectSectionItems.map(({ section, label, icon, end }) => (
                  <SidebarNavItem
                    key={section}
                    item={{
                      to: `/projects/${currentProjectId}/${section}`,
                      label,
                      icon,
                      end,
                    }}
                    collapsed={!!collapsed}
                    onNavigate={onNavigate}
                  />
                ))}
              </div>
            )}
          </div>
        )}

        <div className="mt-4 space-y-1">
          <NavGroupHeading label="智能体" collapsed={!!collapsed} />
          {agentGroupItems.map((item) => renderNavItem(item))}
        </div>
      </nav>
      <SidebarBottom collapsed={!!collapsed} />
    </div>
  );
}

export function Sidebar() {
  const collapsed = useSidebarStore((s) => s.collapsed);

  return (
    <TooltipProvider delay={0}>
      <aside
        className={cn(
          "hidden md:flex md:flex-col md:border-r md:bg-sidebar transition-[width] duration-200",
          collapsed ? "md:w-14" : "md:w-56"
        )}
      >
        <NavContent collapsed={collapsed} />
      </aside>
    </TooltipProvider>
  );
}

export function MobileNav() {
  const [open, setOpen] = useState(false);
  const location = useLocation();

  useEffect(() => {
    const id = setTimeout(() => setOpen(false), 0);
    return () => clearTimeout(id);
  }, [location.pathname]);

  return (
    <Sheet open={open} onOpenChange={setOpen}>
      <SheetTrigger className="inline-flex items-center justify-center rounded-md text-sm font-medium transition-colors hover:bg-accent hover:text-accent-foreground h-9 w-9 md:hidden">
        <Menu className="h-5 w-5" />
      </SheetTrigger>
      <SheetContent side="left" className="w-56 p-0">
        <NavContent onNavigate={() => setOpen(false)} />
      </SheetContent>
    </Sheet>
  );
}
