import { useEffect, useState } from "react";
import { getWorkspace, readSuspension, SUSPENSION_EVENT } from "@/api/client";

/** Keep the notice across navigation and reloads, scoped to the active workspace. */
export function SuspensionBanner() {
  const [message, setMessage] = useState(readSuspension);
  useEffect(() => {
    const update = (event: Event) => {
      const detail = (event as CustomEvent<{ workspace: string | null; message: string }>).detail;
      if (detail && detail.workspace !== getWorkspace()) return;
      setMessage(detail?.message ?? readSuspension());
    };
    window.addEventListener(SUSPENSION_EVENT, update);
    return () => window.removeEventListener(SUSPENSION_EVENT, update);
  }, []);
  if (!message) return null;
  return (
    <div role="alert" className="mb-5 rounded-md border border-failure/50 bg-bad-soft p-3 text-ink">
      <p className="font-bold">{message}</p>
      <p className="mt-1 text-sm">You can still view this workspace. Contact your operator to restore changes and scheduled runs.</p>
    </div>
  );
}
