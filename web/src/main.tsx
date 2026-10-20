import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { RouterProvider } from "@tanstack/react-router";
import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import { ApiError } from "@/api/client";
import { followSharedTheme, followSystemTheme } from "@/lib/theme";
import { createAppRouter } from "./router";
import "./index.css";

const queryClient = new QueryClient({
  defaultOptions: {
    queries: {
      staleTime: 5_000,
      // Client errors won't fix themselves; only retry flaky network and server failures.
      retry: (count, error) => !(error instanceof ApiError && error.status >= 400 && error.status < 500) && count < 2,
    },
  },
});
const router = createAppRouter(queryClient);

// A session that expires mid-use sends the user back to sign in.
queryClient.getQueryCache().subscribe((event) => {
  const error = event.type === "updated" ? event.query.state.error : null;
  if (error instanceof ApiError && error.status === 401 && !router.state.location.pathname.endsWith("/login")) {
    void router.navigate({ to: "/login", search: { redirect: router.state.location.href } });
  }
});

followSystemTheme();
followSharedTheme();

createRoot(document.getElementById("root")!).render(
  <StrictMode>
    <QueryClientProvider client={queryClient}>
      <RouterProvider router={router} />
    </QueryClientProvider>
  </StrictMode>,
);
