import { ClipboardPaste, Globe, Plus, Terminal, Trash2 } from "lucide-react";
import { useId, useState } from "react";
import { isUrlOrTemplate } from "@/lib/validation";
import { CurlImportDialog } from "@/components/CurlImportDialog";
import { Field } from "@/components/Field";
import { Button } from "@/components/ui/button";
import { Input, Textarea } from "@/components/ui/input";
import Combobox from "@/components/ui/combobox";
import { Label } from "@/components/ui/label";
import { Switch } from "@/components/ui/switch";
import { TagInput } from "@/components/ui/tag-input";
import { BUILTIN_CODE_DEFAULTS, splitList, type CodeDefaults, type FormErrors, type JobFormValues } from "@/lib/jobForm";
import { EXIT_CODE_OPTIONS, RETRY_ON_OPTIONS, SUCCESS_CODE_OPTIONS } from "@/lib/codeOptions";

const TEMPLATE_VARIABLES: [name: string, type: string, about: string][] = [
  ["{{run.id}}", "string", "Run ID, e.g. run_01JB…"],
  ["{{run.scheduled_at}}", "string", "Scheduled time, RFC 3339 in UTC"],
  ["{{job.id}}", "string", "Job ID, e.g. job_01JB…"],
  ["{{job.name}}", "string", "Job name"],
  ["{{attempt}}", "integer", "Attempt number, starting at 0"],
  ["{{secret.NAME}}", "string", "Value of workspace secret NAME"],
];

interface Props {
  values: JobFormValues;
  onChange: (values: JobFormValues) => void;
  errors: FormErrors;
  /** Whether this server accepts command jobs (GET /api/v1/features). */
  commandAllowed: boolean;
  /** The server's starting codes; chips matching them carry a "default" mark. */
  defaults?: CodeDefaults;
}

export function SinkEditor({ values, onChange, errors, commandAllowed, defaults = BUILTIN_CODE_DEFAULTS }: Props) {
  const actionName = useId();
  const [urlTouched, setUrlTouched] = useState(false);
  const [curlOpen, setCurlOpen] = useState(false);
  const w = values.webhook;
  const c = values.command;
  const setWebhook = (patch: Partial<typeof w>) => onChange({ ...values, webhook: { ...w, ...patch } });
  const urlError = errors.url || (urlTouched && w.url && !isUrlOrTemplate(w.url) ? "Enter a full http:// or https:// URL" : null);
  const setCommand = (patch: Partial<typeof c>) => onChange({ ...values, command: { ...c, ...patch } });

  const noBody = w.method === "GET" || w.method === "DELETE";
  const jsonStatus = (() => {
    if (!/^[{[]/.test(w.body.trim())) return null;
    try { JSON.parse(w.body); return "Valid JSON"; }
    catch { return w.body.includes("{{") ? "JSON template — checked after variables are filled in" : "Invalid JSON — check quotes, commas and brackets"; }
  })();
  const addHeader = (name: string, value: string) => {
    if (!w.headers.some((h) => h.name.toLowerCase() === name.toLowerCase())) setWebhook({ headers: [...w.headers, { name, value }] });
  };

  return (
    <div className="grid gap-4">
      {commandAllowed ? (
        <fieldset className="grid gap-2 sm:grid-cols-2">
          <legend className="sr-only">Action type</legend>
          {([
            { value: "webhook", label: "Webhook", description: "Call an HTTP endpoint", icon: Globe },
            { value: "command", label: "Command", description: "Run on this server", icon: Terminal },
          ] as const).map(({ value, label, description, icon: Icon }) => (
            <label key={value} className="relative flex cursor-pointer items-start gap-3 rounded-md border border-line p-3 hover:border-ink/40 has-[:checked]:border-primary has-[:checked]:bg-ok-soft has-[:focus-visible]:ring-2 has-[:focus-visible]:ring-ring has-[:focus-visible]:ring-offset-2">
              <input className="sr-only" type="radio" name={actionName} aria-label={label} value={value} checked={values.sinkKind === value} onChange={() => onChange({ ...values, sinkKind: value })} />
              <Icon aria-hidden="true" className="mt-0.5 size-5 shrink-0 text-link" />
              <span><span className="block text-sm font-bold">{label}</span><span className="block text-xs text-muted-foreground">{description}</span></span>
            </label>
          ))}
        </fieldset>
      ) : null}

      {values.sinkKind === "webhook" ? (
        <div className="grid gap-4">
          <div className="flex flex-wrap items-center justify-between gap-2">
            <p className="text-xs text-muted-foreground">Already have a working request? Paste it as a curl command.</p>
            <Button variant="outline" size="sm" onClick={() => setCurlOpen(true)}><ClipboardPaste /> Import from curl</Button>
          </div>
          <CurlImportDialog
            open={curlOpen}
            onOpenChange={setCurlOpen}
            onImport={({ method, url, headers, body, followRedirects }) => { setUrlTouched(true); setWebhook({ method, url, headers, body, followRedirects }); }}
          />
          <div className="grid gap-3 sm:grid-cols-[9rem_minmax(0,1fr)]">
            <Combobox label="Method" value={w.method} options={["POST", "GET", "PUT", "PATCH", "DELETE"].map((value) => ({value}))} onChange={(method) => setWebhook({method})} />
            <Field id="job-url" label="URL" error={urlError} hint='Use an http:// or https:// URL. Secrets: {{secret.NAME}}.'>{(p) => <Input {...p} type="url" inputMode="url" placeholder="https://api.example.com/hook" value={w.url} onBlur={() => setUrlTouched(true)} onChange={(e) => setWebhook({url:e.target.value})} />}</Field>
          </div>

          <fieldset className="grid gap-2">
            <legend className="mb-1 text-sm font-medium">Headers</legend>
            <p className="text-xs text-muted-foreground">Add authentication or other request headers. Values can reference workspace secrets.</p>
            <div className="grid grid-cols-[1fr_1fr_2.25rem] gap-2 text-xs font-medium text-muted-foreground" aria-hidden="true"><span>Header name</span><span>Value</span><span /></div>
            {w.headers.length === 0 ? <p className="rounded-md border border-dashed p-3 text-xs text-muted-foreground">No custom headers. Without a Content-Type header, requests are sent with Content-Type: application/json.</p> : null}
            {w.headers.map((h, i) => (
              <div key={i} className="grid grid-cols-[1fr_1fr_auto] items-end gap-2">
                <div className="grid gap-1">
                  <Label htmlFor={`header-name-${i}`} className="text-xs font-bold">{`Header ${i + 1} name`}</Label>
                  <Input
                    aria-invalid={errors.headers ? true : undefined}
                    aria-describedby={errors.headers ? "job-headers-error" : undefined}
                    id={`header-name-${i}`}
                    placeholder="Authorization"
                    value={h.name}
                    onChange={(e) => setWebhook({ headers: w.headers.map((x, j) => (j === i ? { ...x, name: e.target.value } : x)) })}
                  />
                </div>
                <div className="grid gap-1">
                  <Label htmlFor={`header-value-${i}`} className="text-xs font-bold">{`Header ${i + 1} value`}</Label>
                  <Input
                    id={`header-value-${i}`}
                    placeholder="Bearer {{secret.TOKEN}}"
                    value={h.value}
                    onChange={(e) => setWebhook({ headers: w.headers.map((x, j) => (j === i ? { ...x, value: e.target.value } : x)) })}
                  />
                </div>
                <Button variant="ghost" size="icon" aria-label={`Remove header ${i + 1}`} onClick={() => setWebhook({ headers: w.headers.filter((_, j) => j !== i) })}>
                  <Trash2 />
                </Button>
              </div>
            ))}
            {errors.headers ? <p id="job-headers-error" className="text-xs font-medium text-destructive">✕ {errors.headers}</p> : null}
            <div className="flex flex-wrap gap-2">
              <Button id="job-headers" variant="outline" size="sm" onClick={() => setWebhook({ headers: [...w.headers, { name: "", value: "" }] })}>
                <Plus /> Add header
              </Button>
              <Button variant="outline" size="sm" disabled={w.headers.some((h) => h.name.toLowerCase() === "authorization")} onClick={() => addHeader("Authorization", "Bearer {{secret.NAME}}")}>+ Bearer token</Button>
              <Button variant="outline" size="sm" disabled={w.headers.some((h) => h.name.toLowerCase() === "content-type")} onClick={() => addHeader("Content-Type", "application/json")}>+ JSON content type</Button>
            </div>
          </fieldset>

          <Field label="Body" labelAside={!noBody && jsonStatus === "Valid JSON" ? jsonStatus : undefined} hint={noBody ? `${w.method} requests usually do not need a body. Switch method to edit; any existing body is preserved.` : "Default: standard JSON payload with run ID, job, scheduled time and attempt."}>
            {(p) => <Textarea {...p} disabled={noBody} className="font-mono" placeholder='Example: {"report": "daily", "run": "{{run.id}}"}' value={w.body} onChange={(e) => setWebhook({ body: e.target.value })} />}
          </Field>
          {!noBody && jsonStatus && jsonStatus !== "Valid JSON" ? <p role="status" className="text-xs text-muted-foreground">{jsonStatus}</p> : null}
          <details className="text-xs text-muted-foreground">
            <summary className="w-fit cursor-pointer rounded-sm font-medium focus-visible:outline focus-visible:outline-2 focus-visible:outline-ring">Available template variables</summary>
            <dl className="mt-2 grid gap-1">
              {TEMPLATE_VARIABLES.map(([name, type, about]) => (
                <div key={name} className="grid grid-cols-[minmax(0,11rem)_4.5rem_minmax(0,1fr)] gap-2 max-sm:grid-cols-[minmax(0,1fr)_4.5rem]">
                  <dt className="break-all font-mono text-foreground">{name}</dt>
                  <dd className="font-mono">{type}</dd>
                  <dd className="max-sm:col-span-2">{about}</dd>
                </div>
              ))}
            </dl>
            <p className="mt-2">Use in URLs, header values and body. Values are inserted as plain text, without JSON escaping: quote strings in a JSON body, leave {"{{attempt}}"} unquoted to send a number.</p>
          </details>

          <div className="grid gap-4 sm:grid-cols-2">
            <Field id="job-successCodes" label="Success codes" error={errors.successCodes} hint="Codes or classes like 2xx. Type to search, e.g. 301.">
              {(p) => <TagInput {...p} options={SUCCESS_CODE_OPTIONS} defaults={defaults.success_codes} value={splitList(w.successCodes)} onChange={(codes) => setWebhook({ successCodes: codes.join(", ") })} />}
            </Field>
            <Field id="job-retryOn" label="Retry on" error={errors.retryOn} hint="Network and timeout failures, or status codes and classes like 5xx.">
              {(p) => <TagInput {...p} options={RETRY_ON_OPTIONS} defaults={defaults.retry_on} value={splitList(w.retryOn)} onChange={(codes) => setWebhook({ retryOn: codes.join(", ") })} />}
            </Field>
          </div>

          <div className="flex items-center gap-2">
            <Switch id="follow-redirects" checked={w.followRedirects} onCheckedChange={(followRedirects) => setWebhook({ followRedirects })} />
            <Label htmlFor="follow-redirects">Follow redirects (up to 5)</Label>
          </div>
        </div>
      ) : (
        <div className="grid gap-4">
          <Field id="job-command" label="Command" error={errors.command} hint="Path of the executable, or a command line when “Run through a shell” is on.">
            {(p) => <Input {...p} className="font-mono" placeholder="/usr/local/bin/backup.sh" value={c.command} onChange={(e) => setCommand({ command: e.target.value })} />}
          </Field>
          <Field label="Arguments" hint="Separated by spaces or commas; quotes are literal. Templates work here and in environment values.">
            {(p) => <Input {...p} className="font-mono" value={c.args} onChange={(e) => setCommand({ args: e.target.value })} />}
          </Field>
          <fieldset className="grid gap-2">
            <legend className="mb-1 text-sm font-medium">Environment</legend>
            <p className="text-xs text-muted-foreground">Values support {"{{secret.NAME}}"} templates.</p>
            <div className="grid grid-cols-[1fr_1fr_2.25rem] gap-2 text-xs font-medium text-muted-foreground" aria-hidden="true"><span>Variable name</span><span>Value</span><span /></div>
            {c.env.length === 0 ? <p className="rounded-md border border-dashed p-3 text-xs text-muted-foreground">No extra environment variables.</p> : null}
            {c.env.map((entry, i) => (
              <div key={i} className="grid grid-cols-[1fr_1fr_auto] items-end gap-2">
                <div><Label htmlFor={`env-name-${i}`} className="text-xs font-bold">{`Variable ${i + 1} name`}</Label><Input id={`env-name-${i}`} value={entry.name} onChange={(e) => setCommand({ env: c.env.map((v, j) => j === i ? { ...v, name: e.target.value } : v) })} /></div>
                <div><Label htmlFor={`env-value-${i}`} className="text-xs font-bold">{`Variable ${i + 1} value`}</Label><Input id={`env-value-${i}`} value={entry.value} onChange={(e) => setCommand({ env: c.env.map((v, j) => j === i ? { ...v, value: e.target.value } : v) })} /></div>
                <Button variant="ghost" size="icon" aria-label={`Remove variable ${i + 1}`} onClick={() => setCommand({ env: c.env.filter((_, j) => j !== i) })}><Trash2 /></Button>
              </div>
            ))}
            <div><Button variant="outline" size="sm" onClick={() => setCommand({ env: [...c.env, { name: "", value: "" }] })}><Plus /> Add variable</Button></div>
          </fieldset>
          <Field label="Working directory" hint="Default: server working directory. Use a literal path, without templates.">
            {(p) => <Input {...p} className="font-mono" value={c.workdir} onChange={(e) => setCommand({ workdir: e.target.value })} />}
          </Field>
          <div className="grid gap-4 sm:grid-cols-2">
            <Field id="job-successExitCodes" label="Success exit codes" error={errors.successExitCodes} hint="Exit codes 0–255. Type to search.">
              {(p) => <TagInput {...p} options={EXIT_CODE_OPTIONS} defaults={defaults.success_exit_codes.map(String)} value={splitList(c.successExitCodes)} onChange={(codes) => setCommand({ successExitCodes: codes.join(", ") })} />}
            </Field>
            <Field id="job-retryExitCodes" label="Retry exit codes" error={errors.retryExitCodes} hint="Exit codes that trigger a retry, e.g. 75. None by default.">
              {(p) => <TagInput {...p} options={EXIT_CODE_OPTIONS} defaults={defaults.retry_exit_codes.map(String)} value={splitList(c.retryExitCodes)} onChange={(codes) => setCommand({ retryExitCodes: codes.join(", ") })} />}
            </Field>
          </div>
          <div className="flex items-center gap-2">
            <Switch id="run-shell" checked={c.shell} onCheckedChange={(shell) => setCommand({ shell })} />
            <Label htmlFor="run-shell">Run through a shell</Label>
          </div>
        </div>
      )}
    </div>
  );
}
