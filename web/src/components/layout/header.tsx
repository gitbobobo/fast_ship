import { MobileNav } from "./sidebar";

export function Header({ title }: { title?: string }) {
  return (
    <header className="sticky top-0 z-30 flex h-14 items-center gap-3 border-b bg-background/80 px-4 backdrop-blur-sm md:px-6">
      <MobileNav />
      {title && (
        <h1 className="text-base font-semibold truncate">{title}</h1>
      )}
    </header>
  );
}
