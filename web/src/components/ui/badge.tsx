import { cva, type VariantProps } from "class-variance-authority";
import * as React from "react";
import { cn } from "@/lib/utils";

const badgeVariants = cva("inline-flex items-center gap-1.5 rounded-sm border border-transparent px-1.5 py-px text-xs font-bold whitespace-nowrap", {
  variants: {
    variant: {
      default: "bg-sunken text-ink",
      success: "bg-accent text-accent-ink",
      warning: "bg-sunken text-ink",
      danger: "bg-failure text-failure-ink",
      outline: "border-ink/30 bg-surface text-ink",
      redirect: "bg-redirect text-redirect-ink",
    },
  },
  defaultVariants: { variant: "default" },
});

export interface BadgeProps extends React.HTMLAttributes<HTMLSpanElement>, VariantProps<typeof badgeVariants> {}

export function Badge({ className, variant, ...props }: BadgeProps) {
  return <span className={cn(badgeVariants({ variant }), className)} {...props} />;
}
