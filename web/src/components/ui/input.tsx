import * as React from "react";
import { cn } from "@/lib/utils";

const Input = React.forwardRef<HTMLInputElement, React.InputHTMLAttributes<HTMLInputElement>>(({ className, type, ...props }, ref) => (
  <input
    ref={ref}
    type={type}
    className={cn(
      "flex min-h-11 sm:min-h-9 w-full rounded border border-ink/40 bg-paper text-ink hover:border-ink/70 focus-visible:border-focus focus-visible:ring-1 focus-visible:ring-focus focus-visible:ring-offset-0 px-3 py-2 sm:py-1 font-mono text-base sm:text-sm placeholder:text-ink-soft disabled:cursor-not-allowed disabled:opacity-60 aria-[invalid=true]:border-danger aria-[invalid=true]:bg-bad-soft",
      className,
    )}
    {...props}
  />
));
Input.displayName = "Input";

const Textarea = React.forwardRef<HTMLTextAreaElement, React.TextareaHTMLAttributes<HTMLTextAreaElement>>(({ className, ...props }, ref) => (
  <textarea
    ref={ref}
    className={cn(
      "flex min-h-20 w-full rounded border border-ink/40 bg-paper text-ink hover:border-ink/70 focus-visible:border-focus focus-visible:ring-1 focus-visible:ring-focus focus-visible:ring-offset-0 px-3 py-2 font-mono text-base sm:text-sm placeholder:text-ink-soft disabled:cursor-not-allowed disabled:opacity-60 aria-[invalid=true]:border-danger aria-[invalid=true]:bg-bad-soft",
      className,
    )}
    {...props}
  />
));
Textarea.displayName = "Textarea";

export { Input, Textarea };
