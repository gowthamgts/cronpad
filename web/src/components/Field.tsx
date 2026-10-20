import { useId, type ReactNode } from "react";
import { Label } from "@/components/ui/label";
import { cn } from "@/lib/utils";

interface FieldProps {
  label: string;
  id?: string;
  hint?: ReactNode;
  error?: string | null;
  labelAside?: ReactNode;
  className?: string;
  children: (props: { id: string; "aria-describedby"?: string; "aria-invalid"?: boolean }) => ReactNode;
}

/** A labelled control. Errors replace helper text so feedback stays concise. */
export function Field({ label, id: suppliedId, hint, error, labelAside, className, children }: FieldProps) {
  const generated = useId();
  const id = suppliedId ?? generated;
  const messageId = `${id}-message`;
  return (
    <div className={cn("grid content-start gap-1.5", className)}>
      <div className="flex min-h-5 items-center justify-between gap-2">
        <Label className="text-xs font-bold" htmlFor={id}>{label}</Label>
        {labelAside ? <span className="text-xs tabular-nums text-muted-foreground">{labelAside}</span> : null}
      </div>
      {children({ id, "aria-describedby": error || hint ? messageId : undefined, "aria-invalid": error ? true : undefined })}
      {error ? (
        <p id={messageId} className="flex items-start gap-1.5 text-xs font-medium text-destructive">
          <span aria-hidden="true">✕</span>{error}
        </p>
      ) : hint ? <p id={messageId} className="text-xs text-muted-foreground">{hint}</p> : null}
    </div>
  );
}
