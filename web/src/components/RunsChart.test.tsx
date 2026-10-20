import { render, screen } from "@testing-library/react";
import { expect, it } from "vitest";
import { RunsChart, bucketRuns } from "./RunsChart";

it("names the range and marks periods older than the loaded runs instead of drawing zeros", () => {
  const now = Date.parse("2026-11-10T12:30:00Z");
  const buckets = bucketRuns([{ scheduled_at: "2026-11-10T12:10:00Z", status: "succeeded" }], now, "30d");
  const { container } = render(<RunsChart buckets={buckets} range="30d" loadedFrom={Date.parse("2026-11-08T00:00:00Z")} />);
  expect(screen.getByRole("img")).toHaveAccessibleName("Runs per day over the last 30 days: 1 runs, 0 failed");
  expect(screen.getByText(/Older runs not loaded/)).toBeInTheDocument();
  // Oct 12 to Nov 7 (27 days) end by Nov 8 and are shaded.
  expect(container.querySelectorAll("rect.fill-sunken")).toHaveLength(27);
});
