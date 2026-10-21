import { useMemo, useState } from "react";
import { Field } from "@/components/Field";
import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Textarea } from "@/components/ui/input";
import { parseCurl, type CurlImport } from "@/lib/curlImport";

interface Props {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  onImport: (result: CurlImport) => void;
}

/** Paste a curl command, check what it turns into, then fill the webhook fields with it. */
export function CurlImportDialog({ open, onOpenChange, onImport }: Props) {
  const [text, setText] = useState("");
  const parsed = useMemo((): { result: CurlImport; error?: undefined } | { error: string; result?: undefined } | null => {
    if (!text.trim()) return null;
    try { return { result: parseCurl(text) }; }
    catch (e) { return { error: e instanceof Error ? e.message : String(e) }; }
  }, [text]);
  const close = (next: boolean) => { if (!next) setText(""); onOpenChange(next); };
  const r = parsed?.result;

  return (
    <Dialog open={open} onOpenChange={close}>
      <DialogContent className="max-w-2xl">
        <DialogHeader>
          <DialogTitle>Import from curl</DialogTitle>
          <DialogDescription>Paste a curl command, e.g. from your browser’s “Copy as cURL (bash)”. It replaces the method, URL, headers and body.</DialogDescription>
        </DialogHeader>
        <Field id="curl-command" label="curl command" error={parsed?.error}>
          {(p) => <Textarea {...p} rows={8} spellCheck={false} className="font-mono text-xs" placeholder={`curl -X POST https://api.example.com/hook \\\n  -H "Authorization: Bearer {{secret.TOKEN}}" \\\n  --json '{"run": "{{run.id}}"}'`} value={text} onChange={(e) => setText(e.target.value)} />}
        </Field>
        {r ? (
          <div role="status" className="grid gap-2 rounded-md border border-line p-3 text-xs">
            <p className="break-all font-mono"><span className="font-bold">{r.method}</span> {r.url}</p>
            <p className="text-muted-foreground">
              {r.headers.length === 1 ? "1 header" : `${r.headers.length} headers`} · {r.body ? `${r.body.length}-character body` : "no body"}{r.followRedirects ? " · follows redirects" : ""}
            </p>
            {r.warnings.length > 0 ? <ul className="grid list-disc gap-1 pl-4 text-muted-foreground">{r.warnings.map((w) => <li key={w}>{w}</li>)}</ul> : null}
          </div>
        ) : null}
        <DialogFooter>
          <Button variant="outline" onClick={() => close(false)}>Cancel</Button>
          <Button disabled={!r} onClick={() => { if (r) { onImport(r); close(false); } }}>Import</Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
