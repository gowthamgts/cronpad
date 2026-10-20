import { useFeatures } from "@/api/hooks";

/** The server's public URL for copy-paste examples; falls back to this page's origin. */
export function usePublicUrl(): string {
  const { data } = useFeatures();
  return (data?.public_url || window.location.origin).replace(/\/$/, "");
}

export function createJobCurl(base: string): string {
  return `curl -X POST ${base}/api/v1/jobs \\
  -H "Authorization: Bearer $CRONPAD_API_KEY" \\
  -H "Content-Type: application/json" \\
  -d '{
    "name": "nightly-report",
    "schedule": {"expr": "every day at 9am", "timezone": "UTC"},
    "sink": {"kind": "webhook", "config": {"url": "https://example.com/hook"}}
  }'`;
}

export function listRunsCurl(base: string, jobId?: string): string {
  const path = jobId ? `/api/v1/jobs/${jobId}/runs` : "/api/v1/runs";
  return `curl ${base}${path} -H "Authorization: Bearer $CRONPAD_API_KEY"`;
}

export function createKeyCurl(base: string): string {
  return `curl -X POST ${base}/api/v1/api-keys \\
  -H "Authorization: Bearer $CRONPAD_API_KEY" \\
  -H "Content-Type: application/json" \\
  -d '{"name": "ci", "scopes": ["jobs:write", "runs:read"]}'`;
}

export function putSecretCurl(base: string): string {
  return `curl -X PUT ${base}/api/v1/secrets/REPORTS_TOKEN \\
  -H "Authorization: Bearer $CRONPAD_API_KEY" \\
  -H "Content-Type: application/json" \\
  -d '{"value": "s3cret"}'`;
}
