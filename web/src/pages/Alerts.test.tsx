import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, within } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";

vi.mock("@tanstack/react-router", () => ({ useNavigate: () => vi.fn(), useSearch: () => ({}) }));

import { AlertsPage } from "./Alerts";

function renderPage() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={client}>
      <AlertsPage />
    </QueryClientProvider>,
  );
}

describe("AlertsPage", () => {
  afterEach(() => vi.unstubAllGlobals());

  it("renders a graceful unavailable state when the server answers 501", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () => new Response(JSON.stringify({ error: { code: "unavailable", message: "server detail that must not leak" } }), { status: 501, headers: { "Content-Type": "application/json" } })),
    );
    renderPage();
    expect(await screen.findByText(/Alerting isn’t available on this server/)).toBeInTheDocument();
    expect(screen.queryByText(/server detail/)).not.toBeInTheDocument();
    expect(screen.queryByText(/milestone|M5/i)).not.toBeInTheDocument();
  });

  it("lists channels and rules when the server supports alerting", async () => {
    const respond = (body: unknown) => new Response(JSON.stringify(body), { status: 200, headers: { "Content-Type": "application/json" } });
    vi.stubGlobal(
      "fetch",
      vi.fn(async (input: Request) =>
        input.url.includes("alert-channels")
          ? respond({ data: [{ id: "ch_1", kind: "webhook", name: "#ops", config: {} }], next_cursor: null })
          : respond({ data: [], next_cursor: null }),
      ),
    );
    renderPage();
    expect(await screen.findByText("#ops")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /New channel/ })).toBeInTheDocument();
  });

  it("edits a channel in place with its current values", async () => {
    const respond = (body: unknown, status = 200) => new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });
    const channel = { id: "ch_1", kind: "webhook", name: "#ops", config: { url: "https://hooks.example.com/a", format: "discord" } };
    const fetchMock = vi.fn(async (input: Request) => {
      if (input.method === "PATCH") return respond({ ...channel, name: "#ops-paging" });
      return input.url.includes("alert-channels") ? respond({ data: [channel], next_cursor: null }) : respond({ data: [], next_cursor: null });
    });
    vi.stubGlobal("fetch", fetchMock);
    renderPage();
    fireEvent.click(await screen.findByRole("button", { name: "Edit channel #ops" }));
    const dialog = screen.getByRole("dialog");
    expect(within(dialog).getByLabelText("Webhook URL")).toHaveValue("https://hooks.example.com/a");
    fireEvent.change(within(dialog).getByLabelText("Name"), { target: { value: "#ops-paging" } });
    fireEvent.click(within(dialog).getByRole("button", { name: "Save channel" }));
    await vi.waitFor(() => expect(fetchMock.mock.calls.some(([r]) => r.method === "PATCH")).toBe(true));
    const patch = fetchMock.mock.calls.find(([r]) => r.method === "PATCH")![0];
    expect(patch.url).toMatch(/\/api\/v1\/alert-channels\/ch_1$/);
    expect(await patch.json()).toEqual({ kind: "webhook", name: "#ops-paging", config: { url: "https://hooks.example.com/a", format: "discord" } });
  });
});
