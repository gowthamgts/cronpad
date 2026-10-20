import type { ComponentProps } from "react";
import { cn } from "@/lib/utils";

/**
 * A preformatted block that scrolls. Scrollable regions must be reachable by
 * keyboard, so it takes focus and carries a label.
 */
export function ScrollPre({ className, ...props }: ComponentProps<"pre"> & { "aria-label": string }) {
  // eslint-disable-next-line jsx-a11y/no-noninteractive-tabindex -- scrollable regions must be keyboard-focusable
  return <pre tabIndex={0} className={cn("min-w-0 max-w-full overflow-auto rounded border border-line bg-sunken p-3 font-mono text-xs", className)} {...props} />;
}
