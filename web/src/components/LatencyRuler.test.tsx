import { fireEvent, render, screen } from "@testing-library/react";
import { expect, it } from "vitest";
import { InfoTip } from "@/components/ui/info-tip";
import { LatencyMethod, LatencyRuler } from "./LatencyRuler";
it("names the measurements and provides the methodology without inventing a target", () => {
  render(<LatencyRuler values={{p50:51,p95:66,p99:74}} />);
  expect(screen.getByRole("img")).toHaveAccessibleName("Start lag over the last 24 hours: p50 51 ms, p95 66 ms, p99 74 ms. Scale 0 to 93 ms.");
  expect(screen.getByText("median (p50)")).toBeVisible();
});
it("represents unavailable percentiles without plotting a measured value", () => {
  const {container}=render(<LatencyRuler values={{p50:null,p95:null,p99:null}} />);
  expect(screen.getByRole("img")).toHaveAccessibleName(expect.stringContaining("p50 —"));
  expect(container.querySelector(".bg-accent")).toHaveStyle({width:"0%"});
});
it("keeps the methodology behind an info tip that opens on focus and closes on Escape", () => {
  render(<InfoTip label="How start lag is measured"><LatencyMethod span="7 days" /></InfoTip>);
  const button = screen.getByRole("button", { name: "How start lag is measured" });
  expect(screen.queryByRole("tooltip")).not.toBeInTheDocument();
  fireEvent.focus(button);
  expect(screen.getByRole("tooltip")).toHaveTextContent("cover the last 7 days");
  expect(button).toHaveAccessibleDescription(/scheduled time to when its request was sent/);
  fireEvent.keyDown(window, { key: "Escape" });
  expect(screen.queryByRole("tooltip")).not.toBeInTheDocument();
});
