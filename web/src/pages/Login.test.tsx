import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";

const navigated = vi.fn();
vi.mock("@tanstack/react-router", () => ({
  useNavigate: () => vi.fn(),
  Navigate: (props: { to: string }) => {
    navigated(props.to);
    return null;
  },
}));

import { LoginPage } from "./Login";

function features(auth: { setup: boolean; signup: boolean; providers?: string[] }) {
  const body = { public_url: "", sinks: {}, defaults: {}, auth: { providers: ["google"], dev_login: false, ...auth } };
  vi.stubGlobal("fetch", vi.fn(async () => new Response(JSON.stringify(body), { status: 200, headers: { "Content-Type": "application/json" } })));
}

function renderPage(props: Parameters<typeof LoginPage>[0]) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={client}>
      <LoginPage {...props} />
    </QueryClientProvider>,
  );
}

describe("LoginPage", () => {
  afterEach(() => {
    vi.unstubAllGlobals();
    navigated.mockReset();
  });

  it("onboards the first user and says sign-up closes after them", async () => {
    features({ setup: true, signup: false });
    renderPage({ setup: true });
    expect(await screen.findByRole("link", { name: "Continue with Google" })).toBeInTheDocument();
    expect(screen.getByRole("heading", { name: "Set up cronpad" })).toBeInTheDocument();
    expect(screen.getByText(/Once your account exists, sign-up closes/)).toBeInTheDocument();
    expect(navigated).not.toHaveBeenCalled();
  });

  it("warns that open sign-up lets anyone in", async () => {
    features({ setup: true, signup: true });
    renderPage({ setup: true });
    expect(await screen.findByText(/Sign-up is open/)).toBeInTheDocument();
  });

  it("hands over between setup and sign-in", async () => {
    features({ setup: true, signup: false });
    renderPage({});
    await vi.waitFor(() => expect(navigated).toHaveBeenCalledWith("/setup"));
    navigated.mockReset();
    features({ setup: false, signup: false });
    renderPage({ setup: true });
    await vi.waitFor(() => expect(navigated).toHaveBeenCalledWith("/login"));
  });

  it("explains a refused sign-up", async () => {
    features({ setup: false, signup: false });
    renderPage({ error: "signup_closed" });
    expect(await screen.findByRole("alert")).toHaveTextContent("This server doesn't take new accounts");
  });
});
