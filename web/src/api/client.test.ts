import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { ApiError, call, createApi, readCookie, setWorkspace } from "./client";

function recorder(status = 200, body: unknown = {}) {
  const seen: Request[] = [];
  const fetchStub = vi.fn(async (request: Request) => {
    seen.push(request);
    return new Response(status === 204 ? null : JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });
  });
  return { seen, fetchStub: fetchStub as unknown as typeof fetch };
}

describe("API client session headers", () => {
  beforeEach(() => {
    document.cookie = "cp_csrf=token-123; path=/";
    setWorkspace(null);
  });
  afterEach(() => {
    document.cookie = "cp_csrf=; expires=Thu, 01 Jan 1970 00:00:00 GMT; path=/";
    setWorkspace(null);
  });

  it("sends the cp_csrf cookie as X-CSRF-Token on mutations", async () => {
    const { seen, fetchStub } = recorder(204);
    const api = createApi({ baseUrl: "http://localhost", fetch: fetchStub });
    await api.POST("/api/v1/jobs/{id}/pause", { params: { path: { id: "job_1" } } });
    await api.DELETE("/api/v1/jobs/{id}", { params: { path: { id: "job_1" } } });
    expect(seen).toHaveLength(2);
    for (const request of seen) expect(request.headers.get("X-CSRF-Token")).toBe("token-123");
  });

  it("does not send the token on reads", async () => {
    const { seen, fetchStub } = recorder(200, { data: [], next_cursor: null });
    const api = createApi({ baseUrl: "http://localhost", fetch: fetchStub });
    await api.GET("/api/v1/jobs", {});
    expect(seen[0]!.headers.get("X-CSRF-Token")).toBeNull();
  });

  it("sends the active workspace on every request", async () => {
    setWorkspace("ws_other");
    const { seen, fetchStub } = recorder(200, { data: [], next_cursor: null });
    const api = createApi({ baseUrl: "http://localhost", fetch: fetchStub });
    await api.GET("/api/v1/jobs", {});
    expect(seen[0]!.headers.get("Cronpad-Workspace")).toBe("ws_other");
  });

  it("fetches monthly usage with the active workspace and no CSRF token", async () => {
    setWorkspace("ws_metered");
    const usage = { month: "2026-10", runs: 42, limit: null, over_quota_policy: "allow", reset_at: "2026-11-01T00:00:00Z" };
    const { seen, fetchStub } = recorder(200, usage);
    const api = createApi({ baseUrl: "http://localhost", fetch: fetchStub });
    expect(await call(api.GET("/api/v1/usage"))).toEqual(usage);
    expect(seen[0]!.headers.get("Cronpad-Workspace")).toBe("ws_metered");
    expect(seen[0]!.headers.get("X-CSRF-Token")).toBeNull();
  });

  it("reads cookies by exact name", () => {
    expect(readCookie("cp_csrf", "a=1; cp_csrf=abc%3D; b=2")).toBe("abc=");
    expect(readCookie("csrf", "cp_csrf=abc")).toBeUndefined();
  });
});

describe("call", () => {
  it("turns the error envelope into an ApiError", async () => {
    const { fetchStub } = recorder(400, { error: { code: "validation_failed", message: "bad", fields: { name: "required" } } });
    const api = createApi({ baseUrl: "http://localhost", fetch: fetchStub });
    const error = await call(api.GET("/api/v1/jobs", {})).catch((e: unknown) => e);
    expect(error).toBeInstanceOf(ApiError);
    expect(error).toMatchObject({ status: 400, code: "validation_failed", message: "bad", fields: { name: "required" } });
  });
});
