import { Avatar, AvatarFallback } from "@/components/ui/avatar";
import { cn } from "@/lib/utils";

// 机器人没有头像图：按名称做确定性取色（固定调色板按字符串 hash 取模），
// fallback 显示名称首个非空白字符，拉丁字母转大写。
const PALETTE = [
  "bg-rose-500",
  "bg-orange-500",
  "bg-amber-500",
  "bg-emerald-600",
  "bg-teal-600",
  "bg-sky-600",
  "bg-indigo-500",
  "bg-violet-500",
];

const SIZES = {
  sm: { avatar: "size-9", fallback: "text-sm" },
  lg: { avatar: "size-16", fallback: "text-2xl" },
} as const;

function colorFor(name: string) {
  let hash = 0;
  for (const ch of name) {
    hash = (hash * 31 + (ch.codePointAt(0) ?? 0)) >>> 0;
  }
  return PALETTE[hash % PALETTE.length];
}

function initial(name: string) {
  const ch = [...name.trim()][0];
  return ch ? ch.toUpperCase() : "?";
}

export function BotAvatar({
  name,
  size = "sm",
  className,
}: {
  name: string;
  size?: keyof typeof SIZES;
  className?: string;
}) {
  const s = SIZES[size];
  return (
    <Avatar size={size === "lg" ? "lg" : "default"} className={cn(s.avatar, className)}>
      <AvatarFallback className={cn("font-medium text-white", s.fallback, colorFor(name))}>
        {initial(name)}
      </AvatarFallback>
    </Avatar>
  );
}
