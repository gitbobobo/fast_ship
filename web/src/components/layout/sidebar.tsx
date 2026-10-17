import { useEffect, useState, type ReactNode } from "react";
import { NavLink, useLocation } from "react-router";
import {
  LayoutDashboard,
  Package,
  Tags,
  Bug,
  Kanban,
  Menu,
  ScrollText,
  FileText,
  Images,
  MessagesSquare,
  Bot,
  Workflow,
  Bell,
  Gauge,
} from "lucide-react";
import { useUnreadNotificationCount } from "@/lib/hooks/use-notifications";
import {
  Tooltip,
  TooltipContent,
  TooltipProvider,
  TooltipTrigger,
} from "@/components/ui/tooltip";
import { Sheet, SheetContent, SheetTrigger } from "@/components/ui/sheet";
import { FastShipLogo } from "@/components/ui/fast-ship-logo";
import { UserNav } from "@/components/user-nav";
import { useSidebarStore } from "@/lib/store/sidebar-store";
import { cn } from "@/lib/utils";

const navItems = [
  { to: "/dashboard", label: "仪表盘", icon: LayoutDashboard, end: true },
  { to: "/projects", label: "项目", icon: Package, end: true },
  { to: "/issues", label: "问题", icon: Bug, end: true },
  { to: "/logs", label: "日志", icon: ScrollText, end: false },
  { to: "/documents", label: "文档", icon: FileText, end: true },
  { to: "/board", label: "看板", icon: Kanban, end: true },
  { to: "/threads", label: "线程", icon: MessagesSquare, end: false },
  { to: "/bots", label: "机器人", icon: Bot, end: false },
  { to: "/automations", label: "自动化", icon: Workflow, end: true },
  { to: "/quota", label: "套餐额度", icon: Gauge, end: true },
  { to: "/notifications", label: "通知", icon: Bell, end: true },
  { to: "/versions", label: "版本", icon: Tags, end: true },
  { to: "/screenshots", label: "截图", icon: Images, end: true },
];

function unreadLabel(label: string, unread: number) {
  if (unread <= 0) return label;
  return `${label}（${unread > 99 ? "99+" : unread} 条未读）`;
}

function SidebarNavItem({
  item,
  collapsed,
  onNavigate,
  unread = 0,
}: {
  item: {
    to: string;
    label: string;
    icon: React.ElementType;
    end?: boolean;
  };
  collapsed: boolean;
  onNavigate?: () => void;
  unread?: number;
}) {
  const name = unreadLabel(item.label, unread);
  const link = (
    <NavLink
      to={item.to}
      end={item.end}
      onClick={onNavigate}
      aria-label={name}
      className={({ isActive }) =>
        cn(
          "relative flex items-center rounded-md text-sm font-medium transition-all duration-200",
          collapsed ? "justify-center px-2 py-2" : "gap-3 px-3 py-2",
          isActive
            ? "bg-primary text-primary-foreground"
            : "text-muted-foreground hover:bg-accent hover:text-accent-foreground"
        )
      }
    >
      <span className="relative shrink-0">
        <item.icon className="h-4 w-4" />
        {unread > 0 && collapsed && (
          <span
            data-testid="sidebar-unread-dot"
            className="absolute -right-0.5 -top-0.5 h-1.5 w-1.5 rounded-full bg-destructive"
          />
        )}
      </span>
      <span
        className={cn(
          "transition-all duration-200 overflow-hidden whitespace-nowrap",
          collapsed ? "max-w-0 opacity-0" : "max-w-[120px] opacity-100"
        )}
      >
        {item.label}
      </span>
      {unread > 0 && !collapsed && (
        <span
          data-testid="sidebar-unread-badge"
          className={cn(
            "ml-auto flex h-4 min-w-4 items-center justify-center rounded-full px-1",
            "text-[10px] font-medium leading-none",
            "bg-destructive text-white",
          )}
        >
          {unread > 99 ? "99+" : unread}
        </span>
      )}
    </NavLink>
  );

  if (collapsed) {
    return (
      <Tooltip>
        <TooltipTrigger render={link} />
        <TooltipContent side="right">{name}</TooltipContent>
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

function NavContent({
  collapsed,
  onNavigate,
}: {
  collapsed?: boolean;
  onNavigate?: () => void;
}) {
  const unread = useUnreadNotificationCount().data ?? 0;

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
          "flex-1 space-y-1 overflow-y-auto transition-all duration-200",
          collapsed ? "p-2" : "p-3"
        )}
      >
        {navItems.map((item) => (
          <SidebarNavItem
            key={item.to}
            item={item}
            collapsed={!!collapsed}
            onNavigate={onNavigate}
            unread={item.to === "/notifications" ? unread : 0}
          />
        ))}
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
