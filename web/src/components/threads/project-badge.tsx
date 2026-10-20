import { projectColorIndex, projectInitial } from "@/lib/threads";
import { cn } from "@/lib/utils";

/** 项目徽章的柔和底色调色板，按项目名哈希挑选。 */
const BADGE_TONES = [
  "bg-red-500/15 text-red-600 dark:text-red-400",
  "bg-orange-500/15 text-orange-600 dark:text-orange-400",
  "bg-amber-500/15 text-amber-700 dark:text-amber-400",
  "bg-emerald-500/15 text-emerald-600 dark:text-emerald-400",
  "bg-teal-500/15 text-teal-600 dark:text-teal-400",
  "bg-sky-500/15 text-sky-600 dark:text-sky-400",
  "bg-indigo-500/15 text-indigo-600 dark:text-indigo-400",
  "bg-fuchsia-500/15 text-fuchsia-600 dark:text-fuchsia-400",
];

export function ProjectBadge({
  name,
  className,
}: {
  name: string;
  className?: string;
}) {
  return (
    <span
      aria-hidden
      className={cn(
        "flex size-5 shrink-0 items-center justify-center rounded-md text-[11px] leading-none font-semibold",
        BADGE_TONES[projectColorIndex(name, BADGE_TONES.length)],
        className,
      )}
    >
      {projectInitial(name)}
    </span>
  );
}
