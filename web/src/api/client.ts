import createClient, { type Middleware } from "openapi-fetch";
import type { components, paths } from "./schema";

export type Schemas = components["schemas"];

export const SUSPENSION_EVENT = "cronpad-suspension";
const suspensionKey = (workspace: string | null) => `cronpad-suspension:${workspace ?? "personal"}`;

export function readSuspension(): string | null {
  try { return sessionStorage.getItem(suspensionKey(getWorkspace())); }
  catch { return null; }
}

function suspensionMessage(error: { message?: string; reason?: string; fields?: Record<string, string> }): string {
  const reason = error.reason ?? error.fields?.reason ?? error.fields?.suspended_reason;
  if (reason) return `This workspace is suspended: ${reason}`;
  if (error.message?.startsWith("This workspace is suspended:")) return error.message;
  return error.message && !/^(this )?workspace (is )?suspended[.!]?$/i.test(error.message)
    ? `This workspace is suspended: ${error.message}`
    : "This workspace is suspended";
}

export const WORKSPACE_STORAGE_KEY = "cronpad-workspace";
const MUTATING = new Set(["POST", "PUT", "PATCH", "DELETE"]);

export function readCookie(name: string, source: string = document.cookie): string | undefined {
  for (const part of source.split(";")) {
    const [key, ...rest] = part.trim().split("=");
    if (key === name) return decodeURIComponent(rest.join("="));
  }
  return undefined;
}

export function getWorkspace(): string | null {
  try {
    return localStorage.getItem(WORKSPACE_STORAGE_KEY);
  } catch {
    return null;
  }
}

export function setWorkspace(id: string | null) {
  try {
    if (id) localStorage.setItem(WORKSPACE_STORAGE_KEY, id);
    else localStorage.removeItem(WORKSPACE_STORAGE_KEY);
  } catch {
    /* storage unavailable: the personal workspace is used */
  }
  window.dispatchEvent(new Event(SUSPENSION_EVENT));
}

/** Adds the session CSRF token to mutations and the active workspace to every call. */
export const sessionHeaders: Middleware = {
  onRequest({ request }) {
    if (MUTATING.has(request.method)) {
      const token = readCookie("cp_csrf");
      if (token) request.headers.set("X-CSRF-Token", token);
    }
    const workspace = getWorkspace();
    if (workspace) request.headers.set("Cronpad-Workspace", workspace);
    return request;
  },
  async onResponse({ request, response }) {
    if (MUTATING.has(request.method) && response.status === 403) {
      const body = await response.clone().json().catch(() => null) as { error?: { code?: string; message?: string; reason?: string; fields?: Record<string, string> } } | null;
      if (body?.error?.code === "workspace_suspended") {
        const message = suspensionMessage(body.error);
        const workspace = request.headers.get("Cronpad-Workspace");
        try { sessionStorage.setItem(suspensionKey(workspace), message); } catch { /* banner still receives the event */ }
        window.dispatchEvent(new CustomEvent(SUSPENSION_EVENT, { detail: { workspace, message } }));
      }
    }
    return response;
  },
};

export interface ScheduleProblem {
  kind?: string;
  position?: number;
  length?: number;
  message: string;
  suggestion?: string;
}

export class ApiError extends Error {
  constructor(
    readonly status: number,
    readonly code: string,
    message: string,
    readonly fields: Record<string, string> = {},
    readonly problems: ScheduleProblem[] = [],
  ) {
    super(message);
    this.name = "ApiError";
  }
}

export function createApi(options: { baseUrl?: string; fetch?: typeof fetch } = {}) {
  const client = createClient<paths>({
    baseUrl: options.baseUrl ?? window.location.origin,
    // Resolved lazily so tests can stub window.fetch after import.
    fetch: options.fetch ?? ((request) => window.fetch(request)),
  });
  client.use(sessionHeaders);
  return client;
}

export const api = createApi();

interface Result<T> {
  data?: T;
  error?: unknown;
  response: Response;
}

/** Unwraps an openapi-fetch result, throwing a typed ApiError for non-2xx responses. */
export async function call<T>(promise: Promise<Result<T>>): Promise<T> {
  let result: Result<T>;
  try {
    result = await promise;
  } catch (cause) {
    throw new ApiError(0, "network", cause instanceof Error ? cause.message : "Network request failed");
  }
  if (result.response.ok) return result.data as T;
  const body = (result.error ?? {}) as { error?: { code?: string; message?: string; fields?: Record<string, string>; reason?: string; errors?: ScheduleProblem[] } };
  const e = body.error ?? {};
  throw new ApiError(
    result.response.status,
    e.code ?? "internal",
    e.code === "workspace_suspended" ? suspensionMessage(e) : e.message ?? result.response.statusText ?? "Request failed",
    e.fields,
    e.errors,
  );
}

/** Posts the dev-login form (urlencoded, as the server expects). */
export async function devLogin(name: string): Promise<void> {
  const response = await window.fetch("/auth/dev/login", {
    method: "POST",
    headers: { "Content-Type": "application/x-www-form-urlencoded" },
    body: new URLSearchParams({ name }),
  });
  if (!response.ok) {
    const body = (await response.json().catch(() => ({}))) as { error?: { message?: string } };
    throw new ApiError(response.status, "forbidden", body.error?.message ?? "Sign-in refused");
  }
}

export async function logout(): Promise<void> {
  const response = await window.fetch("/auth/logout", {
    method: "POST",
    headers: { "X-CSRF-Token": readCookie("cp_csrf") ?? "" },
  });
  if (!response.ok && response.status !== 401) throw new ApiError(response.status, "internal", "Sign-out failed");
}
