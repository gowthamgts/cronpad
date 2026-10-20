import { AlertTriangle, CheckCircle2, CircleDashed, Clock, Loader2, PauseCircle, SkipForward, XCircle } from "lucide-react";
import type { ReactNode } from "react";
import { Badge } from "@/components/ui/badge";

type Variant = "default" | "success" | "warning" | "danger" | "outline" | "redirect";

const STATUS: Record<string, { variant: Variant; icon: ReactNode; label?: string }> = {
  succeeded: { variant: "success", icon: <CheckCircle2 aria-hidden="true" className="size-3" /> },
  failed: { variant: "danger", icon: <XCircle aria-hidden="true" className="size-3" /> },
  running: { variant: "warning", icon: <Loader2 aria-hidden="true" className="size-3 animate-spin" /> },
  deferred: { variant: "warning", icon: <Clock aria-hidden="true" className="size-3" />, label: "retrying" },
  retrying: { variant: "default", icon: <Clock aria-hidden="true" className="size-3" /> },
  redirect: { variant: "redirect", icon: <SkipForward aria-hidden="true" className="size-3" /> },
  skipped: { variant: "default", icon: <SkipForward aria-hidden="true" className="size-3" /> },
  cancelled: { variant: "default", icon: <XCircle aria-hidden="true" className="size-3" /> },
  active: { variant: "success", icon: <CheckCircle2 aria-hidden="true" className="size-3" /> },
  paused: { variant: "default", icon: <PauseCircle aria-hidden="true" className="size-3" /> },
  disabled: { variant: "danger", icon: <AlertTriangle aria-hidden="true" className="size-3" /> },
  completed: { variant: "default", icon: <CheckCircle2 aria-hidden="true" className="size-3" /> },
};

/** Status shown as icon + text so it never relies on color alone. */
export function StatusPill({ status }: { status: string | null | undefined }) {
  if (!status) {
    return (
      <Badge variant="outline">
        <CircleDashed aria-hidden="true" className="size-3" />
        never run
      </Badge>
    );
  }
  const s = STATUS[status] ?? { variant: "default" as Variant, icon: null };
  return (
    <Badge variant={s.variant}>
      {s.icon}
      {s.label ?? status}
    </Badge>
  );
}
