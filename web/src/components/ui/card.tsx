import * as React from "react";
import { cn } from "@/lib/utils";

const Card = React.forwardRef<HTMLDivElement, React.HTMLAttributes<HTMLDivElement>>(({ className, ...props }, ref) => (
  <div ref={ref} className={cn("box min-w-0 text-ink", className)} {...props} />
));
Card.displayName = "Card";

const CardHeader = ({ className, ...props }: React.HTMLAttributes<HTMLDivElement>) => (
  <div className={cn("flex flex-col gap-1 p-4 pb-3", className)} {...props} />
);
const CardTitle = ({ className, tab = false, ...props }: React.HTMLAttributes<HTMLHeadingElement> & { tab?: boolean }) => (
  // eslint-disable-next-line jsx-a11y/heading-has-content -- content is supplied by callers
  <h2 className={cn(tab ? "box-title" : "text-base font-bold lowercase leading-tight tracking-tight", className)} {...props} />
);
const CardDescription = ({ className, ...props }: React.HTMLAttributes<HTMLParagraphElement>) => (
  <p className={cn("text-sm leading-relaxed text-muted-foreground", className)} {...props} />
);
const CardContent = ({ className, ...props }: React.HTMLAttributes<HTMLDivElement>) => <div className={cn("min-w-0 p-4 pt-0", className)} {...props} />;

export { Card, CardContent, CardDescription, CardHeader, CardTitle };
