import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import type { Usage } from "@/api/client";
import { UsageMeter } from "./UsageMeter";

const usage: Usage = { month: "2026-10", runs: 20, limit: 100, over_quota_policy: "block", reset_at: "2026-11-01T00:00:00Z" };

describe("UsageMeter", () => {
  it.each([0, null])("shows unlimited usage without a bar for limit %s", (limit) => {
    render(<UsageMeter usage={{ ...usage, limit }} />);
    expect(screen.getByText("20 runs this month · unlimited")).toBeInTheDocument();
    expect(screen.queryByRole("progressbar")).not.toBeInTheDocument();
  });

  it("shows the monthly counter, accessible bar and UTC reset date", () => {
    render(<UsageMeter usage={usage} />);
    expect(screen.getByText("20 / 100 runs this month")).toBeInTheDocument();
    expect(screen.getByRole("progressbar", { name: "Monthly runs used" })).toHaveAttribute("aria-valuenow", "20");
    expect(screen.getByText(/Usage period 2026-10/)).toHaveTextContent(/Nov 1/);
    expect(screen.queryByText(/at least 80%/)).not.toBeInTheDocument();
  });

  it.each([80, 99])("warns at %i percent", (runs) => {
    const { container } = render(<UsageMeter usage={{ ...usage, runs }} />);
    expect(screen.getByText(/at least 80%/)).toBeInTheDocument();
    expect(container.firstChild).toHaveClass("bg-warn-soft");
  });

  it.each([100, 125])("explains blocked occurrences at %i percent and caps the bar", (runs) => {
    const { container } = render(<UsageMeter usage={{ ...usage, runs }} />);
    expect(screen.getByText(/New occurrences are skipped/)).toBeInTheDocument();
    expect(screen.getByRole("progressbar")).toHaveAttribute("aria-valuenow", "100");
    expect(container.firstChild).toHaveClass("bg-bad-soft");
  });

  it("explains overage when runs are allowed beyond the limit", () => {
    render(<UsageMeter usage={{ ...usage, runs: 120, over_quota_policy: "allow" }} />);
    expect(screen.getByText(/New occurrences are counted as overage/)).toBeInTheDocument();
    expect(screen.queryByText(/are skipped/)).not.toBeInTheDocument();
  });
});
