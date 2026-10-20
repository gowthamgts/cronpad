import { act, fireEvent, render, screen } from "@testing-library/react";
import { useState } from "react";
import { describe, expect, it, vi, beforeEach, afterEach } from "vitest";
import { ApiError } from "@/api/client";
import { byteOffsetToIndex, DEBOUNCE_MS, problemRange, ScheduleInput, type PreviewFn, type ScheduleValue } from "./ScheduleInput";

const FIRES = ["2026-11-10T03:30:00Z", "2026-11-11T03:30:00Z"];

function Harness({ preview, timezone = "Asia/Kolkata", viewerZone = "America/Los_Angeles", initial = "", random }: { random?: () => number; preview: PreviewFn; timezone?: string; viewerZone?: string; initial?: string }) {
  const [value, setValue] = useState<ScheduleValue>({ expr: initial, mode: "auto" });
  return <ScheduleInput value={value} onChange={setValue} timezone={timezone} viewerZone={viewerZone} preview={preview} random={random} />;
}

const ok: PreviewFn = async ({ timezone }) => ({ kind_detected: "natural", canonical: "FREQ=DAILY", description: "At 09:00 every day", timezone, next: FIRES });

async function flush(ms: number) {
  await act(async () => {
    await vi.advanceTimersByTimeAsync(ms);
  });
}

describe("ScheduleInput", () => {
  beforeEach(() => vi.useFakeTimers());
  afterEach(() => vi.useRealTimers());

  it("debounces preview requests by 200ms and sends only the latest text", async () => {
    const preview = vi.fn(ok);
    render(<Harness preview={preview} />);
    const input = screen.getByLabelText("Schedule", { selector: "input" });

    for (const text of ["e", "ev", "every day"]) {
      fireEvent.change(input, { target: { value: text } });
      await flush(DEBOUNCE_MS - 50);
    }
    expect(preview).not.toHaveBeenCalled();

    await flush(DEBOUNCE_MS);
    expect(preview).toHaveBeenCalledTimes(1);
    expect(preview).toHaveBeenCalledWith({ expr: "every day", kind: "auto", timezone: "Asia/Kolkata" });
    expect(screen.getByTestId("schedule-description")).toHaveTextContent("At 09:00 every day");
  });

  it("underlines the offending token and offers the suggestion", async () => {
    const preview: PreviewFn = async () => {
      throw new ApiError(400, "validation_failed", "invalid schedule", {}, [{ kind: "natural", position: 6, length: 6, message: 'unknown word "tuesdy"', suggestion: "tuesday" }]);
    };
    render(<Harness preview={preview} initial="every tuesdy at 9am" />);
    await flush(DEBOUNCE_MS + 10);

    expect(screen.getByTestId("schedule-underline")).toHaveTextContent("tuesdy");
    expect(screen.getByText('unknown word "tuesdy"')).toBeInTheDocument();
    expect(screen.getByLabelText("Schedule", { selector: "input" })).toHaveAttribute("aria-invalid", "true");

    fireEvent.click(screen.getByRole("button", { name: "tuesday" }));
    expect(screen.getByLabelText("Schedule", { selector: "input" })).toHaveValue("every tuesday at 9am");
  });

  it("shows the next fires in the job time zone and in the viewer's", async () => {
    render(<Harness preview={ok} initial="every day at 9am" />);
    await flush(DEBOUNCE_MS + 10);

    const job = screen.getByTestId("fires-job").querySelectorAll("li");
    const viewer = screen.getByTestId("fires-viewer").querySelectorAll("li");
    expect(job).toHaveLength(2);
    expect(viewer).toHaveLength(2);
    // 03:30 UTC is 09:00 in Kolkata and 19:30 the previous evening in Los Angeles.
    expect(job[0]).toHaveTextContent("9:00 AM");
    expect(job[0]?.querySelector("time")).toHaveAttribute("title", expect.stringContaining("GMT+5:30"));
    expect(viewer[0]).toHaveTextContent("7:30 PM");
    expect(viewer[0]).toHaveTextContent("Nov 9");
    expect(screen.getByText("In Asia/Kolkata")).toBeInTheDocument();
    expect(screen.getByText("In your time zone (America/Los_Angeles)")).toBeInTheDocument();
  });

  it("shows one list when the viewer is already in the job time zone", async () => {
    render(<Harness preview={ok} initial="every day at 9am" viewerZone="Asia/Kolkata" />);
    await flush(DEBOUNCE_MS + 10);

    expect(screen.getByTestId("fires-job").querySelectorAll("li")).toHaveLength(2);
    expect(screen.queryByTestId("fires-viewer")).not.toBeInTheDocument();
    expect(screen.getByText("In Asia/Kolkata (your time zone)")).toBeInTheDocument();
  });

  it("keeps empty previews compact and reserves result space as soon as typing starts", async () => {
    render(<Harness preview={ok} />);
    const area = screen.getByRole("status");
    expect(area.className).not.toContain("min-h-[15rem]");
    expect(area).toHaveTextContent("Choose a preset");
    fireEvent.change(screen.getByLabelText("Schedule", { selector: "input" }), { target: { value: "every day at 9am" } });
    expect(screen.getByRole("status").className).toContain("min-h-[15rem]");
    await flush(DEBOUNCE_MS + 10);
    expect(screen.getByRole("status").className).toContain("min-h-[15rem]");
    fireEvent.change(screen.getByLabelText("Schedule", { selector: "input" }), { target: { value: "" } });
    expect(screen.getByRole("status").className).not.toContain("min-h-[15rem]");
  });

  it("re-requests when the time zone changes", async () => {
    const preview = vi.fn(ok);
    const { rerender } = render(<Harness preview={preview} initial="every day at 9am" timezone="UTC" />);
    await flush(DEBOUNCE_MS + 10);
    rerender(<Harness preview={preview} initial="every day at 9am" timezone="Europe/Paris" />);
    await flush(DEBOUNCE_MS + 10);
    expect(preview.mock.calls.map((c) => c[0].timezone)).toEqual(["UTC", "Europe/Paris"]);
  });
});

describe("error positions", () => {
  it("maps UTF-8 byte offsets to string indexes", () => {
    expect(byteOffsetToIndex("héllo wörld", 7)).toBe(6); // é takes two bytes
    expect(byteOffsetToIndex("abc", 99)).toBe(3);
  });

  it("falls back to the word at the position when the server sends no length", () => {
    expect(problemRange("every tuesdy at 9am", { position: 6, message: "x" })).toEqual([6, 12]);
    expect(problemRange("abc", { position: 3, message: "x" })).toEqual([3, 3]);
  });
});


describe("randomized presets", () => {
  it("randomizes only a clicked preset and keeps subsequent typed expressions unchanged", () => {
    const random = vi.fn(() => 0.39);
    render(<Harness preview={ok} initial="0 0 * * *" random={random} />);
    const input = screen.getByLabelText("Schedule", { selector: "input" });
    expect(input).toHaveValue("0 0 * * *");
    expect(random).not.toHaveBeenCalled();
    fireEvent.click(screen.getByRole("button", { name: "hourly" }));
    expect(input).toHaveValue("23 * * * *");
    fireEvent.click(screen.getByRole("button", { name: "daily" }));
    expect(input).toHaveValue("23 9 * * *");
    fireEvent.change(input, { target: { value: "0 0 * * *" } });
    expect(input).toHaveValue("0 0 * * *");
    expect(random).toHaveBeenCalledTimes(2);
    expect(screen.getByText(/spread out to avoid the top-of-the-hour rush/)).toBeInTheDocument();
  });
});

describe("syntax switching", () => {
  beforeEach(() => vi.useFakeTimers());
  afterEach(() => vi.useRealTimers());

  const input = () => screen.getByLabelText("Schedule", { selector: "input" });
  const syntax = (name: string) => fireEvent.click(screen.getByRole("radio", { name }));

  it("converts the expression when the syntax changes and sends the new kind", async () => {
    const preview = vi.fn(ok);
    render(<Harness preview={preview} initial="every weekday at 9am" />);
    syntax("Cron");
    expect(input()).toHaveValue("0 9 * * 1-5");
    syntax("RRULE");
    expect(input()).toHaveValue("FREQ=WEEKLY;BYDAY=MO,TU,WE,TH,FR;BYHOUR=9;BYMINUTE=0");
    await flush(DEBOUNCE_MS + 10);
    expect(preview).toHaveBeenLastCalledWith({ expr: "FREQ=WEEKLY;BYDAY=MO,TU,WE,TH,FR;BYHOUR=9;BYMINUTE=0", kind: "rrule", timezone: "Asia/Kolkata" });
    expect(screen.queryByTestId("schedule-notice")).toBeNull();
  });

  it("keeps the text and warns when the new syntax can't say it, clearing the warning on edit", () => {
    render(<Harness preview={ok} initial="17 * * * *" />);
    syntax("Natural");
    expect(input()).toHaveValue("17 * * * *");
    expect(screen.getByTestId("schedule-notice")).toHaveTextContent("Couldn't convert this to Natural");
    fireEvent.change(input(), { target: { value: "every hour" } });
    expect(screen.queryByTestId("schedule-notice")).toBeNull();
  });

  it("writes presets in the selected syntax and warns when a minute can't be spread", () => {
    render(<Harness preview={ok} random={() => 0.39} />);
    syntax("Quartz");
    fireEvent.click(screen.getByRole("button", { name: "weekdays 9am" }));
    expect(input()).toHaveValue("0 0 9 ? * MON-FRI");
    syntax("Natural");
    fireEvent.click(screen.getByRole("button", { name: "hourly" }));
    expect(input()).toHaveValue("every hour");
    expect(screen.getByTestId("schedule-notice")).toHaveTextContent("top of the hour");
    syntax("Cron");
    fireEvent.click(screen.getByRole("button", { name: "hourly" }));
    expect(input()).toHaveValue("23 * * * *");
    expect(screen.queryByTestId("schedule-notice")).toBeNull();
  });
});

describe("syntax switching with the preview", () => {
  beforeEach(() => vi.useFakeTimers());
  afterEach(() => vi.useRealTimers());

  const input = () => screen.getByLabelText("Schedule", { selector: "input" });
  const syntax = (name: string) => fireEvent.click(screen.getByRole("radio", { name }));

  it.each([
    ["Natural", "natural", "every weekday at 9am"], // already natural, so left as typed
    ["Cron", "cron", "0 9 * * 1-5"],
    ["Quartz", "quartz", "0 0 9 ? * MON-FRI"],
    ["RRULE", "rrule", "FREQ=WEEKLY;BYDAY=MO,TU,WE,TH,FR;BYHOUR=9;BYMINUTE=0"],
  ])("previews the converted %s expression with that kind", async (label, kind, expr) => {
    const preview = vi.fn(ok);
    render(<Harness preview={preview} initial="every weekday at 9am" timezone="Europe/Berlin" />);
    syntax(label);
    await flush(DEBOUNCE_MS + 10);
    expect(input()).toHaveValue(expr);
    expect(preview).toHaveBeenLastCalledWith({ expr, kind, timezone: "Europe/Berlin" });
  });

  it("previews as auto without forcing a kind", async () => {
    const preview = vi.fn(ok);
    render(<Harness preview={preview} initial="0 9 * * *" />);
    syntax("Cron");
    syntax("Auto");
    await flush(DEBOUNCE_MS + 10);
    expect(preview).toHaveBeenLastCalledWith({ expr: "0 9 * * *", kind: "auto", timezone: "Asia/Kolkata" });
  });

  it("reports the schedule valid after an exact conversion, and invalid after one the server rejects", async () => {
    const onValidity = vi.fn();
    const preview: PreviewFn = async ({ expr, kind, timezone }) => {
      // Stand-in for the server: cron text is invalid as natural.
      if (kind === "natural" && /^\d/.test(expr)) throw new ApiError(400, "validation_failed", "invalid schedule", {}, [{ kind: "natural", position: 0, length: 2, message: "unknown word" }]);
      return { kind_detected: kind, canonical: expr, description: "ok", timezone, next: FIRES };
    };
    function Valid() {
      const [value, setValue] = useState<ScheduleValue>({ expr: "17 * * * *", mode: "cron" });
      return <ScheduleInput value={value} onChange={setValue} timezone="UTC" preview={preview} onValidity={onValidity} />;
    }
    render(<Valid />);
    await flush(DEBOUNCE_MS + 10);
    expect(onValidity).toHaveBeenLastCalledWith(true);
    syntax("Natural"); // cannot be converted: text kept, so the server rejects it
    await flush(DEBOUNCE_MS + 10);
    expect(screen.getByTestId("schedule-notice")).toHaveTextContent("Couldn't convert this to Natural");
    expect(screen.getByTestId("schedule-problem")).toBeInTheDocument();
    expect(onValidity).toHaveBeenLastCalledWith(false);
    syntax("Cron"); // back to the syntax the text is written in
    fireEvent.change(input(), { target: { value: "0 * * * *" } });
    await flush(DEBOUNCE_MS + 10);
    expect(screen.queryByTestId("schedule-notice")).toBeNull();
    expect(onValidity).toHaveBeenLastCalledWith(true);
  });

  it("keeps the warning in view while the preview error is shown, until the user edits", async () => {
    const preview: PreviewFn = async () => {
      throw new ApiError(400, "validation_failed", "invalid schedule", {}, [{ kind: "natural", position: 0, length: 2, message: "nope" }]);
    };
    render(<Harness preview={preview} initial="17 * * * *" />);
    syntax("Natural");
    await flush(DEBOUNCE_MS + 10);
    expect(screen.getByTestId("schedule-notice")).toBeInTheDocument();
    await flush(5000);
    expect(screen.getByTestId("schedule-notice")).toBeInTheDocument();
    fireEvent.change(input(), { target: { value: "every hour" } });
    expect(screen.queryByTestId("schedule-notice")).toBeNull();
  });

  it("does not touch the time zone, and a time zone change keeps the converted text", async () => {
    const preview = vi.fn(ok);
    const { rerender } = render(<Harness preview={preview} initial="every day at 9am" timezone="Asia/Kolkata" />);
    syntax("Cron");
    rerender(<Harness preview={preview} initial="every day at 9am" timezone="America/New_York" />);
    await flush(DEBOUNCE_MS + 10);
    expect(input()).toHaveValue("0 9 * * *");
    expect(preview.mock.calls.at(-1)?.[0].timezone).toBe("America/New_York");
  });

  it("a preset after a syntax switch is written in the new syntax and previewed with its kind", async () => {
    const preview = vi.fn(ok);
    render(<Harness preview={preview} random={() => 0.39} />);
    syntax("Quartz");
    fireEvent.click(screen.getByRole("button", { name: "daily" }));
    await flush(DEBOUNCE_MS + 10);
    expect(preview).toHaveBeenLastCalledWith({ expr: "0 23 9 * * ?", kind: "quartz", timezone: "Asia/Kolkata" });
  });

  it("offers the controls in a stable order: syntax, then presets", () => {
    render(<Harness preview={ok} />);
    const toggle = screen.getByRole("group", { name: "Schedule syntax" });
    const presets = screen.getByRole("group", { name: "Schedule presets" });
    expect(toggle.compareDocumentPosition(presets) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
  });
});
