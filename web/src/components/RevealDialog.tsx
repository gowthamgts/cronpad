import { CopyBlock } from "@/components/CopyBlock";
import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";

/** Shows a credential once; closing it discards the value from the UI. */
export function RevealDialog({ value, title, description, label, onClose }: { value: string | null; title: string; description: string; label: string; onClose: () => void }) {
  return (
    <Dialog open={value !== null} onOpenChange={(open) => !open && onClose()}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{title}</DialogTitle>
          <DialogDescription>{description}</DialogDescription>
        </DialogHeader>
        {value ? <CopyBlock text={value} label={label} /> : null}
        <p className="text-sm font-medium">You won’t be able to see this again. Store it somewhere safe before closing.</p>
        <DialogFooter>
          <Button onClick={onClose}>I’ve saved it</Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
