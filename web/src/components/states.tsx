import { AlertCircle, Loader2, PlugZap, Workflow } from "lucide-react";
import type { ReactNode } from "react";
import { Button } from "@/components/ui/button";
import { ApiError } from "@/api/client";
import { CopyBlock } from "./CopyBlock";

export function Loading({ label = "Loading" }: { label?: string }) {
  return (
    <div role="status" aria-busy="true" className="flex items-center gap-2 py-8 text-ink-soft">
      <Loader2 aria-hidden="true" className="size-4 animate-spin" />
      {label}…
    </div>
  );
}

export function ErrorState({ error, retry }: { error: unknown; retry?: () => void }) {
  const message = error instanceof Error ? error.message : "Something went wrong";
  return (
    <div role="alert" className="flex items-start gap-3 rounded-md border border-failure/50 bg-bad-soft p-3">
      <AlertCircle aria-hidden="true" className="mt-0.5 size-4 shrink-0 text-destructive" />
      <div className="grid gap-2">
        <p className="font-medium text-destructive">{message}</p>
        {retry ? (
          <div>
            <Button variant="outline" size="sm" onClick={retry}>
              Try again
            </Button>
          </div>
        ) : null}
      </div>
    </div>
  );
}

/** Shown when the server answers 501: the feature is not available on this server. */
export function isUnavailable(error: unknown): boolean {
  return error instanceof ApiError && error.status === 501;
}

export function Unavailable({ feature }: { feature: string }) {
  return (
    <div className="flex flex-col items-center gap-3 box border-dashed p-8 text-center">
      <PlugZap aria-hidden="true" className="size-8 text-ink-soft" />
      <h2 className="text-base font-bold">{feature} isn’t available on this server</h2>
      <p className="max-w-md text-sm text-ink-soft">
        This server doesn’t offer {feature.toLowerCase()}. Everything else keeps working; ask the operator of this server if you need it.
      </p>
    </div>
  );
}

export function EmptyState({ title, description, curl, label, action }: { title: string; description: string; curl?: string; label?: string; action?: ReactNode }) {
  return (
    <div className="empty-state grid min-w-0 gap-4 box border-dashed p-5">
      <div className="flex min-w-0 items-start gap-3">
        <span className="inline-flex size-9 shrink-0 items-center justify-center rounded border border-line bg-sunken text-ink"><Workflow aria-hidden="true" className="size-4" /></span>
        <div className="grid min-w-0 gap-1">
          <h2 className="text-base font-bold lowercase leading-tight tracking-tight">{title}</h2>
          <p className="max-w-xl text-sm leading-relaxed text-ink-soft">{description}</p>
        </div>
      </div>
      {action ? <div>{action}</div> : null}
      {curl ? (
        <div className="grid min-w-0 gap-2">
          <p className="text-xs font-medium text-ink-soft">Or use the API:</p>
          <CopyBlock text={curl} label={label ?? "curl command"} />
        </div>
      ) : null}
    </div>
  );
}

export function PageHeader({ title, description, actions }: { title: string; description?: ReactNode; actions?: ReactNode }) {
  return (
    <div className="mb-5 flex min-w-0 flex-col items-start justify-between gap-3 sm:flex-row sm:items-end">
      <div className="grid w-full min-w-0 flex-1 grid-cols-1 gap-1 sm:w-auto">
        <h1 className="break-words text-base font-bold tracking-tight before:text-link before:content-['//_']">{title}</h1>
        {description ? <div className="max-w-2xl text-sm leading-relaxed text-ink-soft">{description}</div> : null}
      </div>
      {actions ? <div className="flex flex-wrap items-center gap-2">{actions}</div> : null}
    </div>
  );
}

export interface Stat {
  label: string;
  value: ReactNode;
  hint?: ReactNode;
  tone?: "danger" | "warning";
  /** Extra content under the value, such as a meter. */
  extra?: ReactNode;
}

/**
 * A row of headline numbers in one box, split by hairlines instead of one card
 * per number. `columns` sets the desktop column count (mobile is two).
 */
export function StatStrip({ items, label, columns = 4 }: { items: Stat[]; label: string; columns?: 4 | 5 }) {
  return (
    <dl aria-label={label} className={`box grid grid-cols-2 gap-px overflow-hidden bg-line [&>*:last-child:nth-child(odd)]:col-span-2 ${columns === 5 ? "lg:grid-cols-5" : "lg:grid-cols-4"} lg:[&>*:last-child:nth-child(odd)]:col-span-1`}>
      {items.map((item) => (
        <div key={item.label} className={`grid min-w-0 content-start gap-0.5 bg-surface px-4 py-3 ${item.tone === "warning" ? "shadow-[inset_3px_0_0_hsl(var(--warning))]" : item.tone === "danger" ? "shadow-[inset_3px_0_0_hsl(var(--danger))]" : ""}`}>
          <dt className="text-xs text-ink-soft">{item.label}</dt>
          <dd className={`text-2xl font-bold leading-tight tabular-nums ${item.tone === "warning" ? "text-warning" : item.tone === "danger" ? "text-danger" : ""}`}>{item.value}</dd>
          {item.hint ? <dd className="text-xs text-ink-soft">{item.hint}</dd> : null}
          {item.extra ? <dd className="pt-1">{item.extra}</dd> : null}
        </div>
      ))}
    </dl>
  );
}
