import type { Schemas } from "@/api/client";
import { ScrollPre } from "@/components/ScrollPre";
import { Badge } from "@/components/ui/badge";
import { formatDuration } from "@/lib/format";

export function TestResultView({ result }: { result: Schemas["TestResult"] }) {
  const ok = result.outcome === "success";
  return (
    <div role="status" className="grid gap-2 rounded-md border p-3 text-sm">
      <div className="flex flex-wrap items-center gap-2">
        <Badge variant={ok ? "success" : "danger"}>{ok ? "Test succeeded" : `Test ${result.outcome}`}</Badge>
        {result.status_code != null ? <span className={`px-1.5 text-xs font-bold ${(result.status_code === 0 || result.status_code >= 200 && result.status_code < 300) ? "bg-accent text-accent-ink" : result.status_code >= 300 && result.status_code < 400 ? "bg-redirect text-redirect-ink" : "bg-failure text-failure-ink"}`}>{result.status_code >= 100 ? "HTTP" : "Exit"} {result.status_code}</span> : null}
        <span className="text-muted-foreground">{formatDuration(result.duration_ms)}</span>
      </div>
      {result.error ? <p className="font-medium text-destructive">{result.error}</p> : null}
      {result.request_summary ? <ScrollPre aria-label="Request summary">{result.request_summary}</ScrollPre> : null}
      {result.response_body ? <ScrollPre className="max-h-48" aria-label="Response body">{result.response_body}</ScrollPre> : null}
    </div>
  );
}
