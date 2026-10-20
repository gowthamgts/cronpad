import * as TabsPrimitive from "@radix-ui/react-tabs";
import * as React from "react";
import { cn } from "@/lib/utils";

const Tabs = TabsPrimitive.Root;

const TabsList = React.forwardRef<React.ComponentRef<typeof TabsPrimitive.List>, React.ComponentPropsWithoutRef<typeof TabsPrimitive.List>>(
  ({ className, ...props }, ref) => (
    <TabsPrimitive.List ref={ref} className={cn("flex max-w-full flex-wrap items-center gap-1 border-b border-line text-muted-foreground", className)} {...props} />
  ),
);
TabsList.displayName = "TabsList";

const TabsTrigger = React.forwardRef<React.ComponentRef<typeof TabsPrimitive.Trigger>, React.ComponentPropsWithoutRef<typeof TabsPrimitive.Trigger>>(
  ({ className, ...props }, ref) => (
    <TabsPrimitive.Trigger
      ref={ref}
      className={cn(
        "inline-flex items-center justify-center -mb-px whitespace-nowrap min-h-11 border-b-2 border-transparent px-3 text-sm font-bold lowercase text-ink-soft transition-colors hover:text-ink disabled:opacity-60 sm:min-h-9 data-[state=active]:border-ink data-[state=active]:text-ink",
        className,
      )}
      {...props}
    />
  ),
);
TabsTrigger.displayName = "TabsTrigger";

const TabsContent = React.forwardRef<React.ComponentRef<typeof TabsPrimitive.Content>, React.ComponentPropsWithoutRef<typeof TabsPrimitive.Content>>(
  ({ className, ...props }, ref) => <TabsPrimitive.Content ref={ref} className={cn("pt-4 focus-visible:ring-offset-4", className)} {...props} />,
);
TabsContent.displayName = "TabsContent";

export { Tabs, TabsContent, TabsList, TabsTrigger };
