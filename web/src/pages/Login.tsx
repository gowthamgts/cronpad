import { useQueryClient } from "@tanstack/react-query";
import { Navigate, useNavigate } from "@tanstack/react-router";
import { useState, type FormEvent } from "react";
import { ApiError, devLogin } from "@/api/client";
import { useFeatures } from "@/api/hooks";
import { Field } from "@/components/Field";
import { Logo } from "@/components/Logo";
import { ThemeToggle } from "@/components/ThemeToggle";
import { ErrorState, Loading } from "@/components/states";
import { Button, buttonVariants } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { usePageTitle } from "@/lib/usePageTitle";

const PROVIDER_LABELS: Record<string, string> = { google: "Google", github: "GitHub" };

/** Why the server sent the browser back from a provider callback (`/login?error=`). */
const REFUSALS: Record<string, string> = {
  signup_closed: "This server doesn't take new accounts. Ask its administrator to turn on sign-up (auth.allow_signup).",
  domain_not_allowed: "Your email address isn't from a domain this server allows. Sign in with an address from an allowed domain.",
};

interface LoginPageProps {
  /** Onboarding for an empty server: the first sign-in creates its first account. */
  setup?: boolean;
  redirect?: string;
  error?: string;
}

export function LoginPage({ setup = false, redirect, error: refusal }: LoginPageProps) {
  usePageTitle(setup ? "Set up cronpad" : "Sign in");
  const features = useFeatures();
  const client = useQueryClient();
  const navigate = useNavigate();
  const [name, setName] = useState("developer");
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  async function submit(e: FormEvent) {
    e.preventDefault();
    if (!name.trim()) {
      setError("Enter a name");
      return;
    }
    setBusy(true);
    setError(null);
    try {
      await devLogin(name.trim());
      await client.invalidateQueries();
      // redirect is a path inside the app; ignore anything that is not.
      await navigate({ to: redirect?.startsWith("/") && !redirect.startsWith("//") ? redirect : "/" });
    } catch (err) {
      setError(err instanceof ApiError ? err.message : "Sign-in failed");
    } finally {
      setBusy(false);
    }
  }

  const providers = features.data?.auth.providers ?? [];
  const devEnabled = features.data?.auth.dev_login ?? false;
  // Each page sends people to the other once the server's setup state is known; not mid
  // sign-in, where the first account just ended setup and the dashboard is next.
  if (!busy && features.data && features.data.auth.setup !== setup) {
    const search = redirect ? { redirect } : {};
    return setup ? <Navigate to="/login" search={search} replace /> : <Navigate to="/setup" search={search} replace />;
  }

  return (
    <main className="band graph grid min-h-screen place-items-center px-4 py-24">
      <div className="absolute right-4 top-4 text-ink">
        <ThemeToggle />
      </div>
      <div className="w-full max-w-[25rem]">
        <div className="mb-8 flex items-center justify-center gap-2 text-xl font-bold">
          <Logo className="size-8" />
          cronpad
        </div>
        <Card className="text-band-ink">
          {setup ? (
            <CardHeader>
              <p className="marker text-band-soft">first run</p>
              <h1 className="text-2xl font-medium leading-tight tracking-tight" aria-label="Set up cronpad">set up cronpad.</h1>
              <CardDescription className="text-band-soft">No one has signed in to this server yet. Sign in to create the first account and its workspace.</CardDescription>
            </CardHeader>
          ) : (
            <CardHeader>
              <p className="marker text-band-soft">your workspace</p>
              <h1 className="text-2xl font-medium leading-tight tracking-tight" aria-label="Sign in">sign in to cronpad.</h1>
              <CardDescription className="text-band-soft">Schedule jobs and watch every run.</CardDescription>
            </CardHeader>
          )}
          <CardContent className="grid gap-4">
            {!setup && refusal ? (
              <p role="alert" className="rounded border border-destructive px-3 py-2 text-sm">
                {REFUSALS[refusal] ?? "Sign-in was refused."}
              </p>
            ) : null}
            {features.isPending ? <Loading label="Checking sign-in options" /> : null}
            {features.isError ? <ErrorState error={features.error} retry={() => void features.refetch()} /> : null}
            {providers.map((p) => (
              <a key={p} href={`/auth/${p}/login`} className={buttonVariants({ variant: "outline", size: "lg" })}>
                Continue with {p === "oidc:default" ? (features.data?.auth.oidc_display_name || "SSO") : (PROVIDER_LABELS[p] ?? p)}
              </a>
            ))}
            {devEnabled ? (
              <form onSubmit={submit} className="grid gap-3" aria-labelledby="dev-login-title" noValidate>
                {providers.length > 0 ? <hr className="my-1" /> : null}
                <CardTitle id="dev-login-title" className="text-sm">
                  Development login
                </CardTitle>
                <p className="text-xs text-band-soft">Anyone can sign in with any name on this server.</p>
                <Field label="Name" error={error}>
                  {(p) => <Input {...p} autoComplete="username" value={name} onChange={(e) => setName(e.target.value)} />}
                </Field>
                <Button type="submit" disabled={busy}>
                  {busy ? "Signing in…" : "Sign in"}
                </Button>
              </form>
            ) : null}
            {features.isSuccess && providers.length === 0 && !devEnabled ? (
              <p className="text-sm text-band-soft">No sign-in method is configured on this server.</p>
            ) : null}
            {setup && providers.length > 0 ? (
              <p className="text-sm text-band-soft">
                {features.data?.auth.signup
                  ? "Sign-up is open: anyone who can sign in here gets their own account. Limit it with auth.allowed_email_domains."
                  : "Once your account exists, sign-up closes. Nobody else can create an account unless auth.allow_signup is turned on."}
              </p>
            ) : null}
          </CardContent>
        </Card>
      </div>
    </main>
  );
}
