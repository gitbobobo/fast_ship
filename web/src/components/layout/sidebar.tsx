import { useEffect, useState, type ReactNode } from "react";
import { NavLink, useLocation } from "react-router";
import {
  LayoutDashboard,
  Package,
  Rocket,
  Tags,
  Bug,
  Kanban,
  Menu,
  ScrollText,
} from "lucide-react";
import {
  Tooltip,
  TooltipContent,
  TooltipProvider,
  TooltipTrigger,
} from "@/components/ui/tooltip";
import { Sheet, SheetContent, SheetTrigger } from "@/components/ui/sheet";
import { UserNav } from "@/components/user-nav";
import { useSidebarStore } from "@/lib/store/sidebar-store";
import { cn } from "@/lib/utils";

const navItems = [
  { to: "/dashboard", label: "仪表盘", icon: LayoutDashboard, end: true },
  { to: "/projects", label: "项目", icon: Package, end: true },
  { to: "/issues", label: "问题", icon: Bug, end: true },
  { to: "/logs", label: "日志", icon: ScrollText, end: false },
  { to: "/board", label: "看板", icon: Kanban, end: true },
  { to: "/versions", label: "版本", icon: Tags, end: true },
];

function SidebarNavItem({
  item,
  collapsed,
  onNavigate,
}: {
  item: {
    to: string;
    label: string;
    icon: React.ElementType;
    end?: boolean;
  };
  collapsed: boolean;
  onNavigate?: () => void;
}) {
  const link = (
    <NavLink
      to={item.to}
      end={item.end}
      onClick={onNavigate}
      className={({ isActive }) =>
        cn(
          "flex items-center rounded-md text-sm font-medium transition-all duration-200",
          collapsed ? "justify-center px-2 py-2" : "gap-3 px-3 py-2",
          isActive
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

function NavContent({
  collapsed,
  onNavigate,
}: {
  collapsed?: boolean;
  onNavigate?: () => void;
}) {
  return (
    <div className="flex h-full flex-col">
      <div className="flex h-14 shrink-0 items-center border-b px-4">
        <Rocket className="h-5 w-5 shrink-0 text-primary" />
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
          "flex-1 space-y-1 transition-all duration-200",
          collapsed ? "p-2" : "p-3"
        )}
      >
        {navItems.map((item) => (
          <SidebarNavItem
            key={item.to}
            item={item}
            collapsed={!!collapsed}
            onNavigate={onNavigate}
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
