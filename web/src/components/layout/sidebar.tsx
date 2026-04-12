import { NavLink, useNavigate } from "react-router";
import {
  Package,
  Settings,
  KeyRound,
  LogOut,
  Rocket,
  Menu,
  User,
} from "lucide-react";
import { Sheet, SheetContent, SheetTrigger } from "@/components/ui/sheet";
import { Separator } from "@/components/ui/separator";
import { Avatar, AvatarFallback } from "@/components/ui/avatar";
import { useAuthStore } from "@/lib/store/auth-store";
import { authApi } from "@/lib/api/auth";
import { useState } from "react";

const navItems = [
  { to: "/projects", label: "项目", icon: Package },
  { to: "/settings", label: "设置", icon: Settings },
  { to: "/settings/api-keys", label: "API Keys", icon: KeyRound },
];

function NavContent({ onNavigate }: { onNavigate?: () => void }) {
  const { user, logout } = useAuthStore();
  const navigate = useNavigate();

  const handleLogout = async () => {
    try {
      await authApi.logout();
    } finally {
      logout();
      navigate("/login");
    }
  };

  return (
    <div className="flex h-full flex-col">
      <div className="flex h-14 items-center gap-2 px-4">
        <Rocket className="h-5 w-5 text-primary" />
        <span className="text-lg font-semibold tracking-tight">Fast Ship</span>
      </div>
      <Separator />
      <nav className="flex-1 space-y-1 p-3">
        {navItems.map((item) => (
          <NavLink
            key={item.to}
            to={item.to}
            end={item.to === "/settings"}
            onClick={onNavigate}
            className={({ isActive }) =>
              `flex items-center gap-3 rounded-md px-3 py-2 text-sm font-medium transition-colors ${
                isActive
                  ? "bg-primary text-primary-foreground"
                  : "text-muted-foreground hover:bg-accent hover:text-accent-foreground"
              }`
            }
          >
            <item.icon className="h-4 w-4" />
            {item.label}
          </NavLink>
        ))}
      </nav>
      <Separator />
      <div className="p-2">
        <div className="flex items-center gap-2 rounded-md px-2 py-1.5">
          <Avatar className="size-6">
            <AvatarFallback className="bg-muted">
              <User className="h-3 w-3" />
            </AvatarFallback>
          </Avatar>
          <span className="flex-1 text-sm font-medium text-foreground truncate">
            {user?.username ?? ""}
          </span>
          <button
            onClick={handleLogout}
            className="flex items-center justify-center rounded-md p-1 text-muted-foreground transition-colors hover:bg-accent hover:text-accent-foreground"
            title="退出登录"
          >
            <LogOut className="h-3.5 w-3.5" />
          </button>
        </div>
      </div>
    </div>
  );
}

export function Sidebar() {
  return (
    <aside className="hidden md:flex md:w-56 md:flex-col md:border-r md:bg-sidebar">
      <NavContent />
    </aside>
  );
}

export function MobileNav() {
  const [open, setOpen] = useState(false);

  return (
    <Sheet open={open} onOpenChange={setOpen}>
      <SheetTrigger
        className="inline-flex items-center justify-center rounded-md text-sm font-medium transition-colors hover:bg-accent hover:text-accent-foreground h-9 w-9 md:hidden"
      >
        <Menu className="h-5 w-5" />
      </SheetTrigger>
      <SheetContent side="left" className="w-56 p-0">
        <NavContent onNavigate={() => setOpen(false)} />
      </SheetContent>
    </Sheet>
  );
}
