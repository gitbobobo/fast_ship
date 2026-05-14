import { useNavigate } from "react-router";
import { MobileNav } from "./sidebar";
import { UserNav } from "@/components/user-nav";
import { ThemeToggle } from "@/components/ui/theme-toggle";
import { Button } from "@/components/ui/button";
import { ChevronLeft } from "lucide-react";
import { useNavigationHistory } from "@/lib/hooks/use-navigation-history";

export function Header({
  title,
  backFallback = "/projects",
}: {
  title?: string;
  backFallback?: string;
}) {
  const navigate = useNavigate();
  const { canGoBack, canUseBrowserHistory } = useNavigationHistory();

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
