import { useNavigate } from "react-router";
import { MobileNav } from "./sidebar";
import { UserNav } from "@/components/user-nav";
import { ThemeToggle } from "@/components/ui/theme-toggle";
import { Button } from "@/components/ui/button";
import {
  Tooltip,
  TooltipContent,
  TooltipProvider,
  TooltipTrigger,
} from "@/components/ui/tooltip";
import { ChevronLeft, PanelLeftClose, PanelLeftOpen } from "lucide-react";
import { useNavigationHistory } from "@/lib/hooks/use-navigation-history";
import { useSidebarStore } from "@/lib/store/sidebar-store";

export function Header({
  title,
  backFallback = "/projects",
}: {
  title?: string;
  backFallback?: string;
}) {
  const navigate = useNavigate();
  const { canGoBack, canUseBrowserHistory } = useNavigationHistory();
  const collapsed = useSidebarStore((s) => s.collapsed);
  const toggleSidebar = useSidebarStore((s) => s.toggle);

  const handleGoBack = () => {
    if (!canGoBack) return;

    if (canUseBrowserHistory) {
      navigate(-1);
      return;
    }

    navigate(backFallback);
  };

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
        onClick={handleGoBack}
        aria-label="返回"
      >
        <ChevronLeft className="h-4 w-4" />
      </Button>
      {title && (
        <h1 className="text-base font-semibold truncate flex-1">{title}</h1>
      )}
      {!title && <div className="flex-1" />}
      <div className="flex items-center gap-2">
        <ThemeToggle />
        <UserNav />
      </div>
    </header>
  );
}
