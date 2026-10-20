import { ConfirmAction } from "@/components/ConfirmAction";
import { useMutation, useQuery } from "@tanstack/react-query";
import { useNavigate, useSearch } from "@tanstack/react-router";
import { Pencil, Plus, Send, Trash2 } from "lucide-react";
import { useState, type FormEvent } from "react";
import { api, ApiError, call, type Schemas } from "@/api/client";
import { keys, useInvalidate } from "@/api/hooks";
import { rules } from "@/api/rules";
import { Field } from "@/components/Field";
import { RevealDialog } from "@/components/RevealDialog";
import { TestResultView } from "@/components/TestResultView";
import { EmptyState, ErrorState, Loading, PageHeader, Unavailable, isUnavailable } from "@/components/states";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import Combobox from "@/components/ui/combobox";
import { Label } from "@/components/ui/label";
import { Switch } from "@/components/ui/switch";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { isHttpUrl } from "@/lib/validation";
import { usePageTitle } from "@/lib/usePageTitle";

type Channel = Schemas["AlertChannel"];
type Rule = Schemas["AlertRule"];
type Event = Schemas["AlertEvent"];

export function AlertsPage() {
  usePageTitle("Alerts");
  const navigate = useNavigate();
  const tab = useSearch({ from: "/_authed/alerts" }).tab ?? "channels";
  const channels = useQuery({ queryKey: keys.alertChannels(), queryFn: () => call(api.GET("/api/v1/alert-channels")), retry: false });
  const alertRules = useQuery({ queryKey: keys.alertRules(), queryFn: () => call(api.GET("/api/v1/alert-rules")), retry: false });

  if (channels.isPending || alertRules.isPending) return <Loading label="Loading alerts" />;
  if (isUnavailable(channels.error) || isUnavailable(alertRules.error)) {
    return (
      <>
        <PageHeader title="alerts" />
        <Unavailable feature="Alerting" />
      </>
    );
  }
  if (channels.isError) return <ErrorState error={channels.error} retry={() => void channels.refetch()} />;
  if (alertRules.isError) return <ErrorState error={alertRules.error} retry={() => void alertRules.refetch()} />;

  return (
    <>
      <PageHeader title="alerts" description="Send failures, retries and SLA breaches where your team will see them." />
      <Tabs value={tab} onValueChange={(next) => void navigate({ to: "/alerts", search: next === "rules" ? { tab: "rules" } : {}, replace: true })}>
        <TabsList aria-label="Alert sections">
          <TabsTrigger value="channels">Channels</TabsTrigger>
          <TabsTrigger value="rules">Rules</TabsTrigger>
        </TabsList>
        <TabsContent value="channels">
          <Channels channels={channels.data.data} />
        </TabsContent>
        <TabsContent value="rules">
          <Rules rules={alertRules.data.data} channels={channels.data.data} />
        </TabsContent>
      </Tabs>
    </>
  );
}

/** Pretty Go durations: "15m0s" → "15m", "1h0m0s" → "1h". */
const shortDuration = (d: string) => d.replace(/(\d)m0s$/, "$1m").replace(/(\d)h0m$/, "$1h");

function Channels({ channels }: { channels: Channel[] }) {
  const invalidate = useInvalidate();
  // undefined = closed, null = new channel, a channel = editing it.
  const [editing, setEditing] = useState<Channel | null | undefined>(undefined);
  const [secret, setSecret] = useState<string | null>(null);
  const [results, setResults] = useState<Record<string, Schemas["TestResult"]>>({});
  const remove = useMutation({
    mutationFn: (id: string) => call(api.DELETE("/api/v1/alert-channels/{id}", { params: { path: { id } } })),
    onSuccess: invalidate,
  });
  const verify = useMutation({
    mutationFn: (id: string) => call(api.POST("/api/v1/alert-channels/{id}/verify", { params: { path: { id } } })),
  });
  const test = useMutation({
    mutationFn: async (id: string) => ({ id, result: await call(api.POST("/api/v1/alert-channels/{id}/test", { params: { path: { id } } })) }),
    onSuccess: ({ id, result }) => setResults((r) => ({ ...r, [id]: result })),
  });
  return (
    <div className="grid gap-3">
      {channels.length > 0 ? <div className="flex flex-wrap items-center justify-between gap-3">
        <p className="text-sm text-ink-soft">Where alerts are delivered.</p>
        <Button onClick={() => setEditing(null)}>
          <Plus /> New channel
        </Button>
      </div> : null}
      {channels.length === 0 ? <EmptyState title="No channels yet" description="Add an email address or a webhook to receive alerts." action={<Button variant="outline" onClick={() => setEditing(null)}><Plus /> New channel</Button>} /> : null}
      {channels.length > 0 ? <ul className="box divide-y divide-line">
        {channels.map((c) => (
          <li key={c.id} className="grid min-w-0 gap-3 px-4 py-2.5">
            <div className="flex flex-wrap items-center gap-2">
              <h3 className="min-w-0 truncate font-bold">{c.name}</h3>
              <Badge variant="outline">{c.kind}</Badge>
              {c.kind === "email" ? <Badge variant={c.verified ? "success" : "warning"}>{c.verified ? "verified" : "awaiting verification"}</Badge> : null}
              <div className="ml-auto flex items-center gap-2">
                {c.kind === "email" && !c.verified ? <Button variant="outline" size="sm" disabled={verify.isPending} onClick={() => verify.mutate(c.id)}>Resend verification</Button> : null}
                <Button variant="outline" size="sm" onClick={() => test.mutate(c.id)} disabled={test.isPending}>
                  <Send /> Send test
                </Button>
                <Button variant="ghost" size="sm" aria-label={`Edit channel ${c.name}`} title="Edit" onClick={() => setEditing(c)}>
                  <Pencil />
                </Button>
                <ConfirmAction label={`Delete channel ${c.name}`} description="This stops future alerts through this configuration." pending={remove.isPending} onConfirm={() => remove.mutate(c.id)}>
                  <Trash2 />
                </ConfirmAction>
              </div>
            </div>
            {results[c.id] ? <TestResultView result={results[c.id]!} /> : null}
          </li>
        ))}
      </ul> : null}
      {verify.isSuccess ? <p role="status">Verification links sent. Check each address’s inbox.</p> : null}
      {verify.isError ? <ErrorState error={verify.error} /> : null}
      {test.isError ? <ErrorState error={test.error} /> : null}
      {remove.isError ? <ErrorState error={remove.error} /> : null}
      <RevealDialog value={secret} title="Alert signing secret" description="Use this secret to verify alert webhooks." label="Alert signing secret" onClose={() => setSecret(null)} />
      {editing !== undefined ? <ChannelDialog key={editing?.id ?? "new"} channel={editing ?? undefined} onClose={() => setEditing(undefined)} onSecret={setSecret} /> : null}
    </div>
  );
}

/** Creates a channel, or edits `channel` in place (PATCH keeps a webhook's signing secret). */
function ChannelDialog({ channel, onClose, onSecret }: { channel?: Channel; onClose: () => void; onSecret: (secret: string) => void }) {
  const invalidate = useInvalidate();
  const config = (channel?.config ?? {}) as { url?: string; format?: string; to?: string[] };
  const [kind, setKind] = useState<Channel["kind"]>(channel?.kind ?? "webhook");
  const [name, setName] = useState(channel?.name ?? "");
  const [url, setUrl] = useState(config.url ?? "");
  const [format, setFormat] = useState(config.format ?? "slack");
  const [emails, setEmails] = useState((config.to ?? []).join(", "));
  const [errors, setErrors] = useState<Record<string, string>>({});
  const create = useMutation({
    mutationFn: () => {
      const body = { kind, name: name.trim(), config: kind === "email" ? { to: emails.split(/[\s,]+/).filter(Boolean) } : { url: url.trim(), format } };
      return channel
        ? call(api.PATCH("/api/v1/alert-channels/{id}", { params: { path: { id: channel.id } }, body }))
        : call(api.POST("/api/v1/alert-channels", { body }));
    },
    onSuccess: async (saved) => {
      await invalidate();
      onClose();
      if (saved.signing_secret) onSecret(saved.signing_secret);
    },
  });

  function submit(e: FormEvent) {
    e.preventDefault();
    const next: Record<string, string> = {};
    if (!name.trim()) next.name = "Name is required";
    else if (name.trim().length > rules.alertNameMax) next.name = `At most ${rules.alertNameMax} characters`;
    if (kind === "webhook" && !isHttpUrl(url)) next.url = "Enter a full http:// or https:// URL";
    if (kind === "email" && !emails.split(/[\s,]+/).filter(Boolean).every((a) => /^[^@\s]+@[^@\s]+\.[^@\s]+$/.test(a))) next.emails = "Enter one or more valid email addresses";
    if (kind === "email" && !emails.trim()) next.emails = "Enter at least one email address";
    setErrors(next);
    if (Object.keys(next).length === 0) create.mutate();
  }

  return (
    <Dialog open onOpenChange={(o) => !o && onClose()}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{channel ? `Edit ${channel.name}` : "New channel"}</DialogTitle>
          <DialogDescription>{channel ? "Changing the address or type asks email recipients to confirm again." : "Alerts are delivered here."}</DialogDescription>
        </DialogHeader>
        <form onSubmit={submit} noValidate className="grid gap-4">
          <Combobox label="Type" value={kind} onChange={(value) => setKind(value as Channel["kind"])} options={[{value:"webhook",label:"Webhook (Slack, Discord or generic)"},{value:"email",label:"Email"}]} />
          <Field label="Name" error={errors.name}>
            {(p) => <Input {...p} value={name} onChange={(e) => setName(e.target.value)} />}
          </Field>
          {kind === "webhook" ? (
            <>
              <Field label="Webhook URL" error={errors.url}>
                {(p) => <Input {...p} type="url" value={url} onChange={(e) => setUrl(e.target.value)} />}
              </Field>
              <Combobox label="Format" value={format} onChange={setFormat} options={[{value:"slack",label:"Slack"},{value:"discord",label:"Discord"},{value:"generic",label:"Generic signed JSON"}]} />
            </>
          ) : (
            <Field label="Email addresses" error={errors.emails} hint="Each address gets a confirmation link before alerts are sent.">
              {(p) => <Input {...p} value={emails} onChange={(e) => setEmails(e.target.value)} />}
            </Field>
          )}
          {create.isError ? <ErrorState error={create.error} /> : null}
          <DialogFooter>
            <Button variant="ghost" onClick={onClose}>
              Cancel
            </Button>
            <Button type="submit" disabled={create.isPending}>
              {channel ? "Save channel" : "Create channel"}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}

function Rules({ rules: items, channels }: { rules: Rule[]; channels: Channel[] }) {
  const invalidate = useInvalidate();
  // undefined = closed, null = new rule, a rule = editing it.
  const [editing, setEditing] = useState<Rule | null | undefined>(undefined);
  const remove = useMutation({
    mutationFn: (id: string) => call(api.DELETE("/api/v1/alert-rules/{id}", { params: { path: { id } } })),
    onSuccess: invalidate,
  });
  const toggle = useMutation({
    mutationFn: ({ id, enabled }: { id: string; enabled: boolean }) => call(api.PATCH("/api/v1/alert-rules/{id}", { params: { path: { id } }, body: { enabled } })),
    onSuccess: invalidate,
  });
  const selectorText = (r: Rule) => (r.job_selector.all ? "All jobs" : r.job_selector.tags?.length ? `Tagged ${r.job_selector.tags.join(", ")}` : `${r.job_selector.job_ids?.length ?? 0} selected jobs`);
  return (
    <div className="grid gap-3">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <p className="text-sm text-ink-soft">{channels.length === 0 ? "Create a channel first." : "Which events reach which channels."}</p>
        <Button onClick={() => setEditing(null)} disabled={channels.length === 0}>
          <Plus /> New rule
        </Button>
      </div>
      {items.length === 0 ? <EmptyState title="No rules yet" description="A rule decides which events reach which channels." /> : null}
      {items.length > 0 ? <ul className="box divide-y divide-line">
        {items.map((r) => (
          <li key={r.id} className="grid min-w-0 gap-1.5 px-4 py-2.5">
            <div className="flex flex-wrap items-center gap-x-3 gap-y-1">
              <h3 className="min-w-0 truncate font-bold">{r.name}</h3>
              <span className="text-xs text-ink-soft">{selectorText(r)} · cooldown {shortDuration(r.cooldown)}</span>
              <div className="ml-auto flex items-center gap-3">
                <div className="flex items-center gap-2">
                  <span className="text-xs text-ink-soft" aria-hidden="true">{r.enabled ? "on" : "off"}</span>
                  <Switch checked={r.enabled} aria-label={`Rule ${r.name} enabled`} onCheckedChange={(enabled) => toggle.mutate({ id: r.id, enabled })} />
                </div>
                <Button variant="ghost" size="sm" aria-label={`Edit rule ${r.name}`} title="Edit" onClick={() => setEditing(r)}>
                  <Pencil />
                </Button>
                <ConfirmAction label={`Delete rule ${r.name}`} description="This stops future alerts through this configuration." pending={remove.isPending} onConfirm={() => remove.mutate(r.id)}>
                  <Trash2 />
                </ConfirmAction>
              </div>
            </div>
            <p className="flex flex-wrap gap-1">
              {r.events.map((e) => (
                <Badge key={e} variant="outline">{e}</Badge>
              ))}
            </p>
          </li>
        ))}
      </ul> : null}
      {remove.isError ? <ErrorState error={remove.error} /> : null}
      {editing !== undefined ? <RuleDialog key={editing?.id ?? "new"} rule={editing ?? undefined} onClose={() => setEditing(undefined)} channels={channels} /> : null}
    </div>
  );
}

/** Creates a rule, or edits `rule` in place. */
function RuleDialog({ rule, onClose, channels }: { rule?: Rule; onClose: () => void; channels: Channel[] }) {
  const invalidate = useInvalidate();
  const sel = rule?.job_selector;
  const [name, setName] = useState(rule?.name ?? "");
  const [selector, setSelector] = useState<"all" | "tags" | "job_ids">(sel?.tags?.length ? "tags" : sel?.job_ids?.length ? "job_ids" : "all");
  const [selectorValue, setSelectorValue] = useState((sel?.tags?.length ? sel.tags : sel?.job_ids ?? []).join(", "));
  const [events, setEvents] = useState<Event[]>(rule?.events ?? ["run.failed"]);
  const [channelIds, setChannelIds] = useState<string[]>(rule?.channel_ids ?? []);
  const [minConsecutive, setMinConsecutive] = useState(rule?.conditions?.min_consecutive ? String(rule.conditions.min_consecutive) : "");
  const [minAttempt, setMinAttempt] = useState(rule?.conditions?.min_attempt ? String(rule.conditions.min_attempt) : "");
  const [cooldown, setCooldown] = useState(rule ? shortDuration(rule.cooldown) : "15m");
  const [enabled, setEnabled] = useState(rule?.enabled ?? true);
  const [errors, setErrors] = useState<Record<string, string>>({});
  const list = (v: string) => v.split(/[\s,]+/).filter(Boolean);
  const create = useMutation({
    mutationFn: () => {
      const body = {
        name: name.trim(),
        job_selector: selector === "all" ? { all: true } : selector === "tags" ? { tags: list(selectorValue) } : { job_ids: list(selectorValue) },
        events,
        conditions: { ...(minConsecutive ? { min_consecutive: Number(minConsecutive) } : {}), ...(minAttempt ? { min_attempt: Number(minAttempt) } : {}) },
        channel_ids: channelIds,
        cooldown: cooldown.trim(),
        enabled,
      };
      return rule ? call(api.PATCH("/api/v1/alert-rules/{id}", { params: { path: { id: rule.id } }, body })) : call(api.POST("/api/v1/alert-rules", { body }));
    },
    onSuccess: async () => {
      await invalidate();
      onClose();
    },
  });
  const toggle = <T,>(list: T[], item: T) => (list.includes(item) ? list.filter((x) => x !== item) : [...list, item]);

  function submit(e: FormEvent) {
    e.preventDefault();
    const next: Record<string, string> = {};
    if (!name.trim()) next.name = "Name is required";
    if (selector !== "all" && list(selectorValue).length === 0) next.selector = selector === "tags" ? "Enter at least one tag" : "Enter at least one job ID";
    if (events.length === 0) next.events = "Pick at least one event";
    if (channelIds.length === 0) next.channels = "Pick at least one channel";
    if (minConsecutive && (!/^\d+$/.test(minConsecutive) || Number(minConsecutive) < 1)) next.minConsecutive = "Enter a whole number, at least 1";
    if (minAttempt && (!/^\d+$/.test(minAttempt) || Number(minAttempt) < 1)) next.minAttempt = "Enter a whole number, at least 1";
    if (!/^(\d+(\.\d+)?(ms|s|m|h))+$/.test(cooldown.trim())) next.cooldown = "Use a duration like 15m";
    setErrors(next);
    if (Object.keys(next).length === 0) create.mutate();
  }

  return (
    <Dialog open onOpenChange={(o) => !o && onClose()}>
      <DialogContent className="max-h-[90vh] overflow-y-auto">
        <DialogHeader>
          <DialogTitle>{rule ? `Edit ${rule.name}` : "New rule"}</DialogTitle>
          <DialogDescription>Pick the jobs, the events and where to send them.</DialogDescription>
        </DialogHeader>
        <form onSubmit={submit} noValidate className="grid gap-4">
          <Field label="Name" error={errors.name}>
            {(p) => <Input {...p} value={name} onChange={(e) => setName(e.target.value)} />}
          </Field>
          <div className="grid gap-4 sm:grid-cols-2">
            <Combobox label="Jobs" value={selector} onChange={(value) => setSelector(value as typeof selector)} options={[{value:"all",label:"All jobs"},{value:"tags",label:"Jobs with tags"},{value:"job_ids",label:"Specific jobs"}]} />
            {selector !== "all" ? (
              <Field label={selector === "tags" ? "Tags" : "Job IDs"} error={errors.selector} hint="Separate with commas.">
                {(p) => <Input {...p} value={selectorValue} onChange={(e) => setSelectorValue(e.target.value)} />}
              </Field>
            ) : null}
          </div>
          <fieldset className="grid gap-2">
            <legend className="mb-1 text-sm font-medium">Events</legend>
            <div className="grid gap-1 sm:grid-cols-2">
              {rules.alertEvents.map((ev) => (
                <label key={ev} className="flex items-center gap-2 text-sm">
                  <input type="checkbox" className="size-4 accent-primary" checked={events.includes(ev)} onChange={() => setEvents((cur) => toggle(cur, ev))} />
                  {ev}
                </label>
              ))}
            </div>
            {errors.events ? <p className="text-xs font-medium text-destructive">{errors.events}</p> : null}
          </fieldset>
          <fieldset className="grid gap-2">
            <legend className="mb-1 text-sm font-medium">Send to</legend>
            {channels.map((c) => (
              <label key={c.id} className="flex items-center gap-2 text-sm">
                <input type="checkbox" className="size-4 accent-primary" checked={channelIds.includes(c.id)} onChange={() => setChannelIds((cur) => toggle(cur, c.id))} />
                {c.name} <span className="text-muted-foreground">({c.kind})</span>
              </label>
            ))}
            {errors.channels ? <p className="text-xs font-medium text-destructive">{errors.channels}</p> : null}
          </fieldset>
          <div className="grid gap-4 sm:grid-cols-2">
            <Field label="Only after N failures in a row" error={errors.minConsecutive} hint="Optional">
              {(p) => <Input {...p} inputMode="numeric" value={minConsecutive} onChange={(e) => setMinConsecutive(e.target.value)} />}
            </Field>
            <Field label="Only from attempt" error={errors.minAttempt} hint="For retry alerts; optional">
              {(p) => <Input {...p} inputMode="numeric" value={minAttempt} onChange={(e) => setMinAttempt(e.target.value)} />}
            </Field>
            <Field label="Cooldown" error={errors.cooldown}>
              {(p) => <Input {...p} value={cooldown} onChange={(e) => setCooldown(e.target.value)} />}
            </Field>
          </div>
          <div className="flex items-center gap-2">
            <Switch id="rule-enabled" checked={enabled} onCheckedChange={setEnabled} />
            <Label htmlFor="rule-enabled">Enabled</Label>
          </div>
          {create.isError ? <ErrorState error={create.error instanceof ApiError ? create.error : new Error(rule ? "Could not save the rule" : "Could not create the rule")} /> : null}
          <DialogFooter>
            <Button variant="ghost" onClick={onClose}>
              Cancel
            </Button>
            <Button type="submit" disabled={create.isPending}>
              {rule ? "Save rule" : "Create rule"}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}
