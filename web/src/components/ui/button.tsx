import { Slot } from "@radix-ui/react-slot";
import { cva, type VariantProps } from "class-variance-authority";
import * as React from "react";
import { cn } from "@/lib/utils";

const buttonVariants = cva(
  "inline-flex items-center justify-center gap-2 whitespace-nowrap rounded text-sm font-bold disabled:pointer-events-none disabled:opacity-60 [&_svg]:size-4 [&_svg]:shrink-0",
  {
    variants: {
      variant: {
        default: "btn btn-primary",
        destructive: "btn bg-failure text-failure-ink",
        outline: "btn btn-secondary",
        secondary: "btn btn-secondary",
        ghost: "hover:bg-sunken text-ink",
        link: "link",
      },
      size: { default: "min-h-10 px-3.5 py-1 sm:min-h-8 sm:px-3 sm:py-0.5", sm: "!min-h-9 px-3 py-1 sm:!min-h-7 sm:px-2.5 !shadow-none hover:!translate-x-0 hover:!translate-y-0 active:!translate-x-0 active:!translate-y-0", lg: "min-h-11 px-5 sm:min-h-10", icon: "size-11 p-0 sm:size-8" },
    },
    defaultVariants: { variant: "default", size: "default" },
  },
);

export interface ButtonProps extends React.ButtonHTMLAttributes<HTMLButtonElement>, VariantProps<typeof buttonVariants> {
  asChild?: boolean;
}

const Button = React.forwardRef<HTMLButtonElement, ButtonProps>(({ className, variant, size, asChild = false, type, ...props }, ref) => {
  const Comp = asChild ? Slot : "button";
  return <Comp ref={ref} className={cn(buttonVariants({ variant, size }), className)} {...(asChild ? {} : { type: type ?? "button" })} {...props} />;
});
Button.displayName = "Button";

export { Button, buttonVariants };
