import { act, render, screen } from "@testing-library/react";
import { beforeEach, describe, expect, it } from "vitest";
import { ApiError, call, createApi, setWorkspace } from "@/api/client";
import { ErrorState } from "./states";
import { SuspensionBanner } from "./SuspensionBanner";

function client(status: number, error: object = {}) {
  return createApi({ baseUrl: "http://localhost", fetch: async () => new Response(JSON.stringify(error), { status, headers: { "Content-Type": "application/json" } }) });
}

beforeEach(() => { sessionStorage.clear(); setWorkspace(null); });

describe("SuspensionBanner", () => {
  it("persists across navigation and remounts, keeps reads working, and exposes the reason on forms", async () => {
    const api = client(403, { error: { code: "workspace_suspended", message: "Workspace suspended", fields: { reason: "Abuse review" } } });
    const view = render(<SuspensionBanner />);
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
    let error: unknown;
    await act(async () => {
      error = await call(api.POST("/api/v1/jobs/{id}/pause", { params: { path: { id: "job_1" } } })).catch((e: unknown) => e);
    });
    expect(error).toBeInstanceOf(ApiError);
    expect(screen.getByRole("alert")).toHaveTextContent("This workspace is suspended: Abuse review");
    await call(client(200, { data: [], next_cursor: null }).GET("/api/v1/jobs"));
    view.unmount();
    render(<SuspensionBanner />);
    expect(screen.getByRole("alert")).toHaveTextContent("Abuse review");
    render(<ErrorState error={error} />);
    expect(screen.getAllByText("This workspace is suspended: Abuse review")).toHaveLength(2);
  });

  it("shows a reason-free notice and isolates workspaces", async () => {
    const api = client(403, { error: { code: "workspace_suspended", message: "workspace suspended" } });
    setWorkspace("ws_one");
    render(<SuspensionBanner />);
    await act(async () => { await api.DELETE("/api/v1/jobs/{id}", { params: { path: { id: "job_1" } } }); });
    expect(screen.getByText("This workspace is suspended")).toBeInTheDocument();
    act(() => setWorkspace("ws_two"));
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
    act(() => setWorkspace("ws_one"));
    expect(screen.getByRole("alert")).toBeInTheDocument();
  });

  it("does not infer suspension from unrelated forbidden errors or reads", async () => {
    render(<SuspensionBanner />);
    await act(async () => {
      await client(403, { error: { code: "forbidden" } }).DELETE("/api/v1/jobs/{id}", { params: { path: { id: "job_1" } } });
      await client(403, { error: { code: "workspace_suspended" } }).GET("/api/v1/jobs");
    });
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
  });
});
