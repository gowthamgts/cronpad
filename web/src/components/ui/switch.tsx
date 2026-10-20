import * as SwitchPrimitive from "@radix-ui/react-switch";
import * as React from "react";
import { cn } from "@/lib/utils";

const Switch = React.forwardRef<React.ComponentRef<typeof SwitchPrimitive.Root>, React.ComponentPropsWithoutRef<typeof SwitchPrimitive.Root>>(
  ({ className, ...props }, ref) => (
    <SwitchPrimitive.Root
      ref={ref}
      className={cn(
        "peer inline-flex h-6 w-11 shrink-0 rounded cursor-pointer items-center border border-ink/60 transition-colors disabled:cursor-not-allowed disabled:opacity-60 data-[state=checked]:bg-primary data-[state=unchecked]:bg-sunken",
        className,
      )}
      {...props}
    >
      <SwitchPrimitive.Thumb className="pointer-events-none block size-4 rounded-sm border border-ink bg-ink transition-transform data-[state=checked]:translate-x-6 data-[state=unchecked]:translate-x-1" />
    </SwitchPrimitive.Root>
  ),
);
Switch.displayName = "Switch";

export { Switch };
