import { createContext, useContext, useState, type ReactNode } from "react";
import { createPortal } from "react-dom";
import { NavLink, Outlet, Navigate, useLocation } from "react-router";
import { Header } from "@/components/layout/header";
import {
  User,
  KeyRound,
  Key,
  SlidersHorizontal,
  Sparkles,
  Copy,
} from "lucide-react";

const settingsNavItems = [
  { to: "/settings/general", label: "通用", icon: SlidersHorizontal },
  { to: "/settings/profile", label: "个人信息", icon: User },
  { to: "/settings/password", label: "修改密码", icon: KeyRound },
  { to: "/settings/ai", label: "AI 配置", icon: Sparkles },
  { to: "/settings/issue-prompts", label: "问题提示词", icon: Copy },
  { to: "/settings/api-keys", label: "API Keys", icon: Key },
];

function SettingsSidebar() {
  return (
    <aside className="hidden lg:flex lg:w-56 lg:flex-col lg:border-r lg:bg-sidebar h-[calc(100vh-3.5rem)]">
      <nav className="flex-1 space-y-1 p-3">
        {settingsNavItems.map((item) => (
          <NavLink
            key={item.to}
            to={item.to}
            end
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
    </aside>
  );
}

function SettingsMobileNav() {
  return (
    <nav className="flex lg:hidden border-b bg-sidebar p-2 overflow-x-auto">
      {settingsNavItems.map((item) => (
        <NavLink
          key={item.to}
          to={item.to}
          end
          className={({ isActive }) =>
            `flex items-center gap-2 rounded-md px-3 py-2 text-sm font-medium whitespace-nowrap transition-colors ${
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
  );
}

function SettingsBody({ children }: { children: ReactNode }) {
  return (
    <div className="flex flex-1 overflow-hidden">
      <SettingsSidebar />
      <div className="flex flex-1 flex-col overflow-hidden">
        <SettingsMobileNav />
        <div className="flex-1 overflow-y-auto p-4 md:p-6">
          <div className="max-w-2xl">{children}</div>
        </div>
      </div>
    </div>
  );
}

const SettingsActionsSlotContext = createContext<HTMLElement | null | undefined>(
  undefined,
);

export function SettingsPageShell({
  actions,
  children,
}: {
  actions?: ReactNode;
  children: ReactNode;
}) {
  const slot = useContext(SettingsActionsSlotContext);

  if (slot !== undefined) {
    return (
      <>
        {slot && actions ? createPortal(actions, slot) : null}
        {children}
      </>
    );
  }

  return (
    <>
      <Header title="设置" actions={actions} />
      <SettingsBody>{children}</SettingsBody>
    </>
  );
}

export default function SettingsLayout() {
  const location = useLocation();
  const [actionsSlot, setActionsSlot] = useState<HTMLDivElement | null>(null);

  if (location.pathname === "/settings") {
    return <Navigate to="/settings/general" replace />;
  }

  return (
    <SettingsActionsSlotContext.Provider value={actionsSlot}>
      <Header
        title="设置"
        actions={
          <div
            ref={setActionsSlot}
            className="flex items-center gap-2 shrink-0"
          />
        }
      />
      <SettingsBody>
        <Outlet />
      </SettingsBody>
    </SettingsActionsSlotContext.Provider>
  );
}
