import * as LabelPrimitive from "@radix-ui/react-label";
import * as React from "react";
import { cn } from "@/lib/utils";

const Label = React.forwardRef<React.ComponentRef<typeof LabelPrimitive.Root>, React.ComponentPropsWithoutRef<typeof LabelPrimitive.Root>>(
  ({ className, ...props }, ref) => <LabelPrimitive.Root ref={ref} className={cn("text-xs font-bold leading-none", className)} {...props} />,
);
Label.displayName = "Label";

export { Label };
