import * as CollapsiblePrimitive from "@radix-ui/react-collapsible";
import * as React from "react";
import { cn } from "@/lib/utils";

const Collapsible = CollapsiblePrimitive.Root;
const CollapsibleTrigger = React.forwardRef<React.ComponentRef<typeof CollapsiblePrimitive.Trigger>, React.ComponentPropsWithoutRef<typeof CollapsiblePrimitive.Trigger>>(
  ({ className, ...props }, ref) => <CollapsiblePrimitive.Trigger ref={ref} className={cn("inline-flex min-h-11 items-center gap-2 sm:min-h-9 rounded border border-ink/40 bg-surface px-3 text-sm font-bold hover:bg-sunken data-[state=open]:underline data-[state=open]:underline-offset-4", className)} {...props} />,
);
CollapsibleTrigger.displayName = "CollapsibleTrigger";
const CollapsibleContent = React.forwardRef<React.ComponentRef<typeof CollapsiblePrimitive.Content>, React.ComponentPropsWithoutRef<typeof CollapsiblePrimitive.Content>>(
  ({ className, ...props }, ref) => <CollapsiblePrimitive.Content ref={ref} className={cn("rounded-md border border-line bg-surface p-4", className)} {...props} />,
);
CollapsibleContent.displayName = "CollapsibleContent";

export { Collapsible, CollapsibleContent, CollapsibleTrigger };
