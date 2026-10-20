import { act, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, beforeEach, expect, it, vi } from "vitest";

const navigate = vi.fn();
const jobsResult = { data: undefined as unknown, isPlaceholderData: false, isFetching: false };
vi.mock("@tanstack/react-router", () => ({ useNavigate: () => navigate }));
vi.mock("@/api/hooks", () => ({ useJobs: () => jobsResult }));

import { CommandPalette } from "./CommandPalette";

beforeEach(() => {
  vi.useFakeTimers();
  navigate.mockReset();
  jobsResult.data = { pages: [{ data: [{ id: "job_1", name: "nightly-report", state: "active" }] }] };
  jobsResult.isPlaceholderData = false;
});
afterEach(() => vi.useRealTimers());

function open() {
  const onOpenChange = vi.fn();
  render(<CommandPalette open onOpenChange={onOpenChange} onSignOut={vi.fn()} onShowShortcuts={vi.fn()} />);
  return { input: screen.getByRole("combobox", { name: "Type a command or search jobs" }), onOpenChange };
}

it("filters commands and runs the highlighted one with Enter", () => {
  const { input, onOpenChange } = open();
  fireEvent.change(input, { target: { value: "alerts" } });
  expect(screen.getAllByRole("option").map((o) => o.textContent)).toEqual(["Alerts · channels", "Alerts · rules"]);
  fireEvent.keyDown(input, { key: "Enter" });
  expect(onOpenChange).toHaveBeenCalledWith(false);
  expect(navigate).toHaveBeenCalledWith({ to: "/alerts", search: {} });
});

it("never offers jobs from an older search when Enter comes before the new results", () => {
  const { input } = open();
  // Results on screen still belong to the empty query: they must not be picked for "settings".
  fireEvent.change(input, { target: { value: "settings" } });
  expect(screen.queryByText("nightly-report")).not.toBeInTheDocument();
  fireEvent.keyDown(input, { key: "Enter" });
  expect(navigate).toHaveBeenCalledWith({ to: "/settings", search: {} });
});

it("lists matching jobs once the search for the typed text has answered", () => {
  const { input } = open();
  fireEvent.change(input, { target: { value: "nightly" } });
  act(() => vi.advanceTimersByTime(200));
  fireEvent.keyDown(input, { key: "Enter" });
  expect(navigate).toHaveBeenCalledWith({ to: "/jobs/$jobId", params: { jobId: "job_1" }, search: {} });
});

it("links straight to a page's tab", () => {
  const { input } = open();
  fireEvent.change(input, { target: { value: "secrets" } });
  fireEvent.keyDown(input, { key: "Enter" });
  expect(navigate).toHaveBeenCalledWith({ to: "/settings", search: { tab: "secrets" } });
});

it("searches jobs by name and opens the tab named after it", () => {
  const { input } = open();
  fireEvent.change(input, { target: { value: "nightly runs" } });
  act(() => vi.advanceTimersByTime(200));
  expect(screen.getAllByRole("option").map((o) => o.textContent)).toEqual(["nightly-report · runs"]);
  fireEvent.keyDown(input, { key: "Enter" });
  expect(navigate).toHaveBeenCalledWith({ to: "/jobs/$jobId", params: { jobId: "job_1" }, search: { tab: "runs" } });
});
