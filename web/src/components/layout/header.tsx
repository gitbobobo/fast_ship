import { MobileNav } from "./sidebar";
import { UserNav } from "@/components/user-nav";
import { ThemeToggle } from "@/components/ui/theme-toggle";
import { Separator } from "@/components/ui/separator";

export function Header({ title }: { title?: string }) {
  return (
    <header className="sticky top-0 z-30 flex h-14 items-center gap-3 border-b bg-background/80 px-4 backdrop-blur-sm md:px-6">
      <MobileNav />
      {title && (
        <h1 className="text-base font-semibold truncate flex-1">{title}</h1>
      )}
      {!title && <div className="flex-1" />}
      <div className="flex items-center gap-2">
        <ThemeToggle />
        <Separator orientation="vertical" className="h-6" />
        <UserNav />
      </div>
    </header>
  );
}
