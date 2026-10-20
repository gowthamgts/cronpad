import { ConfirmAction } from "@/components/ConfirmAction";
import { WorkspaceUsage } from "@/components/UsageMeter";
import { useMutation, useQuery } from "@tanstack/react-query";
import { useNavigate, useSearch } from "@tanstack/react-router";
import { KeyRound, Plus, Trash2 } from "lucide-react";
import { useState, type FormEvent } from "react";
import { api, ApiError, call } from "@/api/client";
import { keys, useInvalidate, useMe } from "@/api/hooks";
import { rules } from "@/api/rules";
import { Field } from "@/components/Field";
import { RevealDialog } from "@/components/RevealDialog";
import { createKeyCurl, putSecretCurl, usePublicUrl } from "@/components/curl";
import { EmptyState, ErrorState, Loading, PageHeader } from "@/components/states";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { formatDateTime, relativeTime } from "@/lib/format";
import { usePageTitle } from "@/lib/usePageTitle";
import { validateKeyName, validateSecretName, validateSecretValue, validateWorkspace } from "@/lib/validation";

export function SettingsPage() {
  usePageTitle("Settings");
  const navigate = useNavigate();
  const tab = useSearch({ from: "/_authed/settings" }).tab ?? "keys";
  return (
    <>
      <PageHeader title="settings" description="Manage access, secrets and your workspace." />
      <Tabs value={tab} onValueChange={(next) => void navigate({ to: "/settings", search: next === "keys" ? {} : { tab: next as "secrets" | "workspace" }, replace: true })}>
        <TabsList aria-label="Settings sections">
          <TabsTrigger value="keys">API keys</TabsTrigger>
          <TabsTrigger value="secrets">Secrets</TabsTrigger>
          <TabsTrigger value="workspace">Workspace</TabsTrigger>
        </TabsList>
        <TabsContent value="keys">
          <ApiKeys />
        </TabsContent>
        <TabsContent value="secrets">
          <Secrets />
        </TabsContent>
        <TabsContent value="workspace">
          <WorkspaceUsage />
          <WorkspaceSettings />
        </TabsContent>
      </Tabs>
    </>
  );
}

function ApiKeys() {
  const base = usePublicUrl();
  const invalidate = useInvalidate();
  const list = useQuery({ queryKey: keys.apiKeys(), queryFn: () => call(api.GET("/api/v1/api-keys", { params: { query: { limit: rules.pageLimitMax } } })) });
  const [open, setOpen] = useState(false);
  const [created, setCreated] = useState<string | null>(null);
  const [revoking, setRevoking] = useState<{ id: string; name: string } | null>(null);
  const revoke = useMutation({
    mutationFn: (id: string) => call(api.DELETE("/api/v1/api-keys/{id}", { params: { path: { id } } })),
    onSuccess: async () => {
      setRevoking(null);
      await invalidate();
    },
  });
  const rows = list.data?.data.filter((k) => !k.revoked_at) ?? [];
  return (
    <Card>
      <CardHeader className="flex-row flex-wrap items-center justify-between gap-3">
        <div className="grid gap-1">
          <CardTitle className="sr-only">API keys</CardTitle>
          <CardDescription>Keys belong to this workspace and authenticate the API and CLI.</CardDescription>
        </div>
        <Button onClick={() => setOpen(true)}>
          <Plus /> New key
        </Button>
      </CardHeader>
      <CardContent>
        {list.isPending ? <Loading /> : null}
        {list.isError ? <ErrorState error={list.error} retry={() => void list.refetch()} /> : null}
        {list.isSuccess && rows.length === 0 ? (
          <EmptyState title="No API keys" description="Create a key to call the API from scripts and CI." curl={createKeyCurl(base)} label="create key command" />
        ) : null}
        {rows.length > 0 ? (
          <Table className="min-w-[36rem]">
            <TableHeader>
              <TableRow>
                <TableHead>Name</TableHead>
                <TableHead>Key</TableHead>
                <TableHead>Scopes</TableHead>
                <TableHead>Last used</TableHead>
                <TableHead>
                  <span className="sr-only">Actions</span>
                </TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {rows.map((k) => (
                <TableRow key={k.id}>
                  <TableCell className="font-medium">{k.name}</TableCell>
                  <TableCell className="font-mono text-xs">{k.prefix}…</TableCell>
                  <TableCell>
                    <span className="flex flex-wrap gap-1">
                      {k.scopes.map((s) => (
                        <Badge key={s} variant="outline">{s}</Badge>
                      ))}
                    </span>
                  </TableCell>
                  <TableCell>{k.last_used_at ? <time dateTime={k.last_used_at} title={formatDateTime(k.last_used_at)}>{relativeTime(k.last_used_at)}</time> : "never"}</TableCell>
                  <TableCell className="text-right">
                    <Button variant="ghost" size="sm" onClick={() => setRevoking({ id: k.id, name: k.name })}>
                      <Trash2 /> Revoke<span className="sr-only"> {k.name}</span>
                    </Button>
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        ) : null}
      </CardContent>
      <NewKeyDialog open={open} onOpenChange={setOpen} onCreated={setCreated} />
      <RevealDialog value={created} title="Your new API key" description="Use it as a bearer token: Authorization: Bearer <key>." label="API key" onClose={() => setCreated(null)} />
      <Dialog open={revoking !== null} onOpenChange={(o) => !o && setRevoking(null)}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>Revoke {revoking?.name}?</DialogTitle>
            <DialogDescription>Anything using this key stops working immediately.</DialogDescription>
          </DialogHeader>
          {revoke.isError ? <ErrorState error={revoke.error} /> : null}
          <DialogFooter>
            <Button variant="ghost" onClick={() => setRevoking(null)}>Cancel</Button>
            <Button variant="destructive" disabled={revoke.isPending} onClick={() => revoking && revoke.mutate(revoking.id)}>
              Revoke key
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </Card>
  );
}

function NewKeyDialog({ open, onOpenChange, onCreated }: { open: boolean; onOpenChange: (o: boolean) => void; onCreated: (key: string) => void }) {
  const invalidate = useInvalidate();
  const [name, setName] = useState("");
  const [scopes, setScopes] = useState<string[]>(["*"]);
  const [error, setError] = useState<string | null>(null);
  const create = useMutation({
    mutationFn: () => call(api.POST("/api/v1/api-keys", { body: { name: name.trim(), scopes: scopes as ("*" | "jobs:read" | "jobs:write" | "runs:read")[] } })),
    onSuccess: async (key) => {
      await invalidate();
      setName("");
      setScopes(["*"]);
      onOpenChange(false);
      if (key.key) onCreated(key.key);
    },
  });
  function submit(e: FormEvent) {
    e.preventDefault();
    const problem = validateKeyName(name) ?? (scopes.length === 0 ? "Pick at least one scope" : null);
    setError(problem);
    if (!problem) create.mutate();
  }
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>New API key</DialogTitle>
          <DialogDescription>The key is shown once, right after you create it.</DialogDescription>
        </DialogHeader>
        <form onSubmit={submit} noValidate className="grid gap-4">
          <Field label="Name" error={error && error !== "Pick at least one scope" ? error : null}>
            {(p) => <Input {...p} placeholder="ci-deploy" value={name} onChange={(e) => setName(e.target.value)} />}
          </Field>
          <fieldset className="grid gap-2">
            <legend className="mb-1 text-sm font-medium">Scopes</legend>
            {rules.keyScopes.map((s) => (
              <label key={s} className="flex items-center gap-2 text-sm">
                <input
                  type="checkbox"
                  className="size-4 accent-primary"
                  checked={scopes.includes(s)}
                  onChange={() => setScopes((cur) => (cur.includes(s) ? cur.filter((x) => x !== s) : [...cur, s]))}
                />
                <span className="font-mono">{s}</span>
                {s === "*" ? <span className="text-muted-foreground">full access</span> : null}
              </label>
            ))}
            {error === "Pick at least one scope" ? <p className="text-xs font-medium text-destructive">{error}</p> : null}
          </fieldset>
          {create.isError ? <ErrorState error={create.error} /> : null}
          <DialogFooter>
            <Button variant="ghost" onClick={() => onOpenChange(false)}>Cancel</Button>
            <Button type="submit" disabled={create.isPending}>
              <KeyRound /> Create key
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}

function Secrets() {
  const base = usePublicUrl();
  const invalidate = useInvalidate();
  const list = useQuery({ queryKey: keys.secrets(), queryFn: () => call(api.GET("/api/v1/secrets", { params: { query: { limit: rules.pageLimitMax } } })) });
  const [name, setName] = useState("");
  const [value, setValue] = useState("");
  const [touched, setTouched] = useState(false);
  const save = useMutation({
    mutationFn: () => call(api.PUT("/api/v1/secrets/{name}", { params: { path: { name } }, body: { value } })),
    onSuccess: async () => {
      setName("");
      setValue("");
      setTouched(false);
      await invalidate();
    },
  });
  const remove = useMutation({
    mutationFn: (n: string) => call(api.DELETE("/api/v1/secrets/{name}", { params: { path: { name: n } } })),
    onSuccess: invalidate,
  });
  const nameError = touched ? validateSecretName(name) : null;
  const valueError = touched ? validateSecretValue(value) : null;
  function submit(e: FormEvent) {
    e.preventDefault();
    setTouched(true);
    if (!validateSecretName(name) && !validateSecretValue(value)) save.mutate();
  }
  return (
    <Card>
      <CardHeader>
        <CardTitle className="sr-only">Secrets</CardTitle>
        <CardDescription>
          Reference a secret from a job as <code className="font-mono">{"{{secret.NAME}}"}</code>. Values are write-only: once saved they can’t be read back, only replaced or deleted.
        </CardDescription>
      </CardHeader>
      <CardContent className="grid gap-4">
        <form onSubmit={submit} noValidate className="grid gap-3 sm:grid-cols-[1fr_1.5fr_auto] sm:items-start" aria-label="Add or replace a secret">
          <Field label="Name" error={nameError}>
            {(p) => <Input {...p} className="font-mono" placeholder="REPORTS_TOKEN" autoComplete="off" value={name} onChange={(e) => setName(e.target.value)} />}
          </Field>
          <Field label="Value" error={valueError}>
            {(p) => <Input {...p} type="password" autoComplete="new-password" value={value} onChange={(e) => setValue(e.target.value)} />}
          </Field>
          <Button type="submit" className="sm:mt-[1.625rem]" disabled={save.isPending}>
            Save secret
          </Button>
        </form>
        {save.isError ? <ErrorState error={save.error instanceof ApiError ? save.error : new Error("Could not save the secret")} /> : null}
        {list.isPending ? <Loading /> : null}
        {list.isError ? <ErrorState error={list.error} retry={() => void list.refetch()} /> : null}
        {list.isSuccess && list.data.data.length === 0 ? <EmptyState title="No secrets" description="Store tokens here instead of pasting them into job configuration." curl={putSecretCurl(base)} label="put secret command" /> : null}
        {list.isSuccess && list.data.data.length > 0 ? (
          <Table className="min-w-[36rem]">
            <TableHeader>
              <TableRow>
                <TableHead>Name</TableHead>
                <TableHead>Updated</TableHead>
                <TableHead>
                  <span className="sr-only">Actions</span>
                </TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {list.data.data.map((s) => (
                <TableRow key={s.name}>
                  <TableCell className="font-mono text-xs">{s.name}</TableCell>
                  <TableCell>
                    <time dateTime={s.updated_at} title={formatDateTime(s.updated_at)}>{relativeTime(s.updated_at)}</time>
                  </TableCell>
                  <TableCell className="text-right">
                    <ConfirmAction label={`Delete secret ${s.name}`} description="Jobs referencing this secret will no longer receive its value." pending={remove.isPending} onConfirm={() => remove.mutate(s.name)}>
                      <Trash2 /> Delete<span className="sr-only"> {s.name}</span>
                    </ConfirmAction>
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        ) : null}
        {remove.isError ? <ErrorState error={remove.error} /> : null}
      </CardContent>
    </Card>
  );
}

function WorkspaceSettings() {
  const me = useMe();
  const invalidate = useInvalidate();
  const workspaces = me.data?.workspaces ?? [];
  const current = workspaces.find((w) => w.id === (localStorage.getItem("cronpad-workspace") ?? workspaces[0]?.id)) ?? workspaces[0];
  if (me.isPending) return <Loading />;
  if (!current) return <ErrorState error={new Error("No workspace found")} />;
  return <WorkspaceForm key={current.id} id={current.id} initialName={current.name} initialSlug={current.slug} onSaved={invalidate} />;
}

function WorkspaceForm({ id, initialName, initialSlug, onSaved }: { id: string; initialName: string; initialSlug: string; onSaved: () => Promise<void> }) {
  const client = useInvalidate();
  const [name, setName] = useState(initialName);
  const [errors, setErrors] = useState<{ name?: string }>({});
  const [saved, setSaved] = useState(false);
  const patch = useMutation({
    mutationFn: () => call(api.PATCH("/api/v1/workspaces/{ws}", { params: { path: { ws: id } }, body: { name: name.trim() } })),
    onSuccess: async () => {
      setSaved(true);
      await onSaved();
      await client();
    },
  });
  function submit(e: FormEvent) {
    e.preventDefault();
    setSaved(false);
    const problems = validateWorkspace(name);
    setErrors(problems);
    if (!problems.name) patch.mutate();
  }
  return (
    <Card>
      <CardHeader>
        <CardTitle className="sr-only">Workspace</CardTitle>
        <CardDescription>
          ID <span className="break-all font-mono">{id}</span> · slug <span className="break-all font-mono">{initialSlug}</span>
        </CardDescription>
      </CardHeader>
      <CardContent>
        <form onSubmit={submit} noValidate className="grid max-w-md gap-4">
          <Field label="Name" error={errors.name}>
            {(p) => <Input {...p} value={name} onChange={(e) => setName(e.target.value)} />}
          </Field>
          {patch.isError ? <ErrorState error={patch.error} /> : null}
          {saved ? <p role="status" className="text-sm text-success">Saved.</p> : null}
          <div>
            <Button type="submit" disabled={patch.isPending}>
              Save workspace
            </Button>
          </div>
        </form>
      </CardContent>
    </Card>
  );
}
