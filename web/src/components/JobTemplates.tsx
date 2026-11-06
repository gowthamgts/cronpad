import { Link } from "@tanstack/react-router";
import { ArrowRight } from "lucide-react";
import { useFeatures } from "@/api/hooks";
import { buttonVariants } from "@/components/ui/button";
import { JOB_TEMPLATES, type JobTemplateKey } from "@/lib/jobTemplates";

/** Starter jobs for an empty workspace: one click opens a prefilled form, so the first job only needs a URL or command. */
export function JobTemplates() {
  const features = useFeatures();
  const keys = (Object.keys(JOB_TEMPLATES) as JobTemplateKey[]).filter((k) => k !== "command" || features.data?.sinks.command);
  return (
    <div className="grid gap-3">
      <ul className="grid gap-2 sm:grid-cols-2 lg:grid-cols-3" aria-label="Start from a template">
        {keys.map((key) => (
          <li key={key} className="grid">
            <Link
              to="/jobs/new"
              search={{ template: key }}
              className="group grid gap-1 rounded-md border border-line bg-card p-3 text-left transition-colors hover:border-ink focus-visible:outline focus-visible:outline-2 focus-visible:outline-ring"
            >
              <span className="flex items-center justify-between gap-2 text-sm font-bold">
                {JOB_TEMPLATES[key].title}
                <ArrowRight aria-hidden="true" className="size-4 shrink-0 text-ink-soft transition-transform group-hover:translate-x-0.5" />
              </span>
              <span className="text-xs leading-relaxed text-ink-soft">{JOB_TEMPLATES[key].description}</span>
            </Link>
          </li>
        ))}
      </ul>
      <div>
        <Link to="/jobs/new" className={buttonVariants({ variant: "outline" })}>
          Start from scratch
        </Link>
      </div>
    </div>
  );
}

/** Points the API example at the settings tab where the key it needs is made. */
export function ApiKeyNote() {
  return (
    <>
      with an{" "}
      <Link to="/settings" className="link">
        API key
      </Link>
    </>
  );
}
