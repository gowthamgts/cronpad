import { Check, Copy } from "lucide-react";
import { useState } from "react";
import { ScrollPre } from "@/components/ScrollPre";
import { Button } from "@/components/ui/button";

/** A code block with a copy button. */
export function CopyBlock({ text, label, wrap = false }: { text: string; label: string; wrap?: boolean }) {
  const [copied, setCopied] = useState(false);
  async function copy() {
    try {
      await navigator.clipboard.writeText(text);
      setCopied(true);
      setTimeout(() => setCopied(false), 1500);
    } catch {
      /* clipboard unavailable: the text is still selectable */
    }
  }
  return (
    <div className="relative min-w-0">
      <ScrollPre className={`p-4 pr-16 leading-relaxed${wrap ? " whitespace-pre-wrap break-all" : ""}`} aria-label={label}>
        <code>{text}</code>
      </ScrollPre>
      <Button variant="ghost" size="icon" className="absolute right-1 top-1 size-11 rounded border border-line bg-surface sm:size-9" onClick={copy} aria-label={copied ? "Copied" : `Copy ${label}`}>
        {copied ? <Check /> : <Copy />}
      </Button>
    </div>
  );
}
