import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { SkipReason } from "./SkipReason";

it.each([
  ["quota_exceeded", "Monthly run quota exceeded"],
  ["workspace_suspended", "Workspace suspended"],
  ["previous_run_in_progress", "Previous run still in progress"],
  ["misfire", "Missed scheduled time"],
])("labels %s", (skip_reason, label) => {
  render(<SkipReason run={{ status: "skipped", skip_reason }} />);
  expect(screen.getByText(`Skipped: ${label}`)).toBeInTheDocument();
});

describe("optional skip reason", () => {
  it("tolerates older run responses and ignores reasons for other statuses", () => {
    const { container, rerender } = render(<SkipReason run={{ status: "skipped" }} />);
    expect(container).toBeEmptyDOMElement();
    rerender(<SkipReason run={{ status: "succeeded", skip_reason: "misfire" }} />);
    expect(container).toBeEmptyDOMElement();
  });
});
