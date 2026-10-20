import { useState, type ReactNode } from "react";
import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
export function ConfirmAction({ label, description, pending, onConfirm, children }: { label: string; description: string; pending?: boolean; onConfirm: () => void; children: ReactNode }) {
  const [open, setOpen] = useState(false);
  return <><Button variant="ghost" size="sm" aria-label={label} disabled={pending} onClick={() => setOpen(true)}>{children}</Button><Dialog open={open} onOpenChange={setOpen}><DialogContent><DialogHeader><DialogTitle>{label}?</DialogTitle><DialogDescription>{description}</DialogDescription></DialogHeader><DialogFooter><Button variant="outline" onClick={() => setOpen(false)}>Cancel</Button><Button variant="destructive" disabled={pending} onClick={() => {onConfirm();setOpen(false);}}>Confirm deletion</Button></DialogFooter></DialogContent></Dialog></>;
}
