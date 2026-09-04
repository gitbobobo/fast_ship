import type { ReactNode } from "react";
import { useLocation, useNavigate } from "react-router";
import { MobileNav } from "./sidebar";
import { Button } from "@/components/ui/button";
import {
  Tooltip,
  TooltipContent,
  TooltipProvider,
  TooltipTrigger,
} from "@/components/ui/tooltip";
import { ChevronLeft, PanelLeftClose, PanelLeftOpen } from "lucide-react";
import { useSidebarStore } from "@/lib/store/sidebar-store";

function hasPreviousRouterEntry(locationKey: string): boolean {
  if (typeof locationKey !== "string") {
    return false;
  }
  const idx = window.history.state?.idx;
  return typeof idx === "number" && idx > 0;
}

export function Header({
  title,
  actions,
}: {
  title?: string;
  actions?: ReactNode;
}) {
  const navigate = useNavigate();
  const location = useLocation();
  const canGoBack = hasPreviousRouterEntry(location.key);
  const collapsed = useSidebarStore((s) => s.collapsed);
  const toggleSidebar = useSidebarStore((s) => s.toggle);

  return (
    <header className="sticky top-0 z-30 flex h-14 shrink-0 items-center gap-3 border-b bg-background/80 px-4 backdrop-blur-sm md:px-6">
      <MobileNav />
      <TooltipProvider delay={0}>
        <Tooltip>
          <TooltipTrigger
            render={
              <Button
                variant="ghost"
                size="icon"
                className="hidden md:inline-flex h-8 w-8"
                onClick={toggleSidebar}
                aria-label={collapsed ? "展开侧边栏" : "折叠侧边栏"}
              >
                {collapsed ? (
                  <PanelLeftOpen className="h-4 w-4" />
                ) : (
                  <PanelLeftClose className="h-4 w-4" />
                )}
              </Button>
            }
          />
          <TooltipContent>{collapsed ? "展开侧边栏" : "折叠侧边栏"}</TooltipContent>
        </Tooltip>
      </TooltipProvider>
      <Button
        variant="ghost"
        size="icon"
        className="h-8 w-8"
        disabled={!canGoBack}
        onClick={() => navigate(-1)}
        aria-label="返回"
      >
        <ChevronLeft className="h-4 w-4" />
      </Button>
      {title && (
        <h1 className="text-base font-semibold truncate flex-1 min-w-0">{title}</h1>
      )}
      {!title && <div className="flex-1 min-w-0" />}
      {actions}
    </header>
  );
}
