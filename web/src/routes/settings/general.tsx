import { Monitor, Sun, Moon } from "lucide-react";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { SettingsPageShell } from "@/routes/settings/layout";
import { useThemeStore, type Theme } from "@/lib/store/theme-store";

const themes: { value: Theme; label: string; icon: typeof Sun }[] = [
  { value: "light", label: "浅色", icon: Sun },
  { value: "dark", label: "深色", icon: Moon },
  { value: "system", label: "跟随系统", icon: Monitor },
];

export default function GeneralPage() {
  const { theme, setTheme } = useThemeStore();

  const currentTheme = themes.find((t) => t.value === theme);
  const Icon = currentTheme?.icon ?? Monitor;

  return (
    <SettingsPageShell>
      <div className="space-y-6">
        <div>
          <h2 className="text-lg font-medium">通用</h2>
          <p className="text-sm text-muted-foreground">管理应用的基本设置</p>
        </div>

        <div className="flex items-center justify-between rounded-lg border p-4">
          <div className="flex items-center gap-3">
            <Icon className="h-5 w-5 text-muted-foreground" />
            <div>
              <p className="font-medium">主题</p>
              <p className="text-sm text-muted-foreground">
                选择浅色、深色或跟随系统主题
              </p>
            </div>
          </div>
          <Select value={theme} onValueChange={(value) => setTheme(value as Theme)}>
            <SelectTrigger className="w-32">
              <SelectValue placeholder="选择主题">
                {currentTheme?.label}
              </SelectValue>
            </SelectTrigger>
            <SelectContent>
              {themes.map((t) => (
                <SelectItem key={t.value} value={t.value}>
                  <div className="flex items-center gap-2">
                    <t.icon className="h-4 w-4" />
                    {t.label}
                  </div>
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </div>
      </div>
    </SettingsPageShell>
  );
}
