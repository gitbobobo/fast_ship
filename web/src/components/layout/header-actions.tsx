import type { ReactNode } from "react";
import { Ellipsis } from "lucide-react";
import { Button } from "@/components/ui/button";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";

export function HeaderActions({
  primary,
  secondary = [],
}: {
  primary?: ReactNode;
  secondary?: ReactNode[];
}) {
  return (
    <div className="flex items-center gap-2 shrink-0">
      {primary}
      {secondary.length > 0 && (
        <>
          <div className="hidden md:flex items-center gap-2">{secondary}</div>
          <DropdownMenu>
            <DropdownMenuTrigger
              render={
                <Button
                  variant="outline"
                  size="icon-sm"
                  className="md:hidden"
                  aria-label="更多操作"
                />
              }
            >
              <Ellipsis className="h-4 w-4" />
            </DropdownMenuTrigger>
            <DropdownMenuContent align="end" className="w-48 p-1.5">
              <div className="flex flex-col gap-1.5 [&_button]:w-full [&_button]:justify-start [&_a]:w-full [&_a]:justify-start">
                {secondary.map((item, index) => (
                  <div key={index}>{item}</div>
                ))}
              </div>
            </DropdownMenuContent>
          </DropdownMenu>
        </>
      )}
    </div>
  );
}
