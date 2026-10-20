import { type ScheduleValue } from "@cronpad/schedule";
import { ScheduleNotice, SchedulePresets, ScheduleSyntaxToggle } from "@cronpad/schedule/ui";
import { fireEvent, render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import { describe, expect, it, vi } from "vitest";

/** Mirrors how both apps hold the value and the notice. */
function App({ initial, random, onSubmit }: { initial: ScheduleValue; random?: () => number; onSubmit?: () => void }) {
  const [value, setValue] = useState(initial);
  const [notice, setNotice] = useState<string | null>(null);
  const change = (next: ScheduleValue, message: string | null) => {
    setNotice(message);
    setValue(next);
  };
  return (
    <form
      onSubmit={(e) => {
        e.preventDefault();
        onSubmit?.();
      }}
    >
      <input aria-label="expr" value={value.expr} onChange={(e) => change({ ...value, expr: e.target.value }, null)} />
      <output aria-label="mode">{value.mode}</output>
      <ScheduleSyntaxToggle name="syntax" value={value} onChange={change} />
      <ScheduleNotice message={notice} />
      <SchedulePresets value={value} onChange={change} random={random} />
    </form>
  );
}

const expr = () => screen.getByLabelText("expr");
const mode = () => screen.getByLabelText("mode");
const radio = (name: string) => screen.getByRole("radio", { name });
const preset = (name: string) => screen.getByRole("button", { name });

describe("ScheduleSyntaxToggle", () => {
  it("renders a labelled group of the five syntaxes with the current one checked", () => {
    render(<App initial={{ expr: "", mode: "cron" }} />);
    const group = screen.getByRole("group", { name: "Schedule syntax" });
    expect(within(group).getAllByRole("radio").map((r) => (r as HTMLInputElement).value)).toEqual(["auto", "natural", "cron", "quartz", "rrule"]);
    expect(radio("Cron")).toBeChecked();
    for (const other of ["Auto", "Natural", "Quartz", "RRULE"]) expect(radio(other)).not.toBeChecked();
  });

  it("converts on every click and reports no notice when the conversion is exact", () => {
    render(<App initial={{ expr: "every weekday at 9am", mode: "natural" }} />);
    fireEvent.click(radio("Cron"));
    expect(expr()).toHaveValue("0 9 * * 1-5");
    expect(mode()).toHaveTextContent("cron");
    fireEvent.click(radio("Quartz"));
    expect(expr()).toHaveValue("0 0 9 ? * MON-FRI");
    fireEvent.click(radio("RRULE"));
    expect(expr()).toHaveValue("FREQ=WEEKLY;BYDAY=MO,TU,WE,TH,FR;BYHOUR=9;BYMINUTE=0");
    fireEvent.click(radio("Natural"));
    expect(expr()).toHaveValue("every weekday at 09:00");
    expect(screen.queryByTestId("schedule-notice")).toBeNull();
  });

  it("shows a caveat, and still converts, when run times can shift between clock and interval syntaxes", () => {
    render(<App initial={{ expr: "every 5 minutes", mode: "natural" }} />);
    fireEvent.click(radio("Cron"));
    expect(expr()).toHaveValue("*/5 * * * *");
    expect(screen.getByTestId("schedule-notice")).toHaveTextContent(/follow the clock in the job's time zone/);
    fireEvent.click(radio("Quartz")); // same family: nothing shifts
    expect(expr()).toHaveValue("0 */5 * * * ?");
    expect(screen.queryByTestId("schedule-notice")).toBeNull();
    fireEvent.click(radio("RRULE")); // clock to interval again
    expect(screen.getByTestId("schedule-notice")).toHaveTextContent(/run times can shift/);
  });

  it("switching to Auto keeps the text and never warns", () => {
    render(<App initial={{ expr: "0 9 * * 1-5", mode: "cron" }} />);
    fireEvent.click(radio("Auto"));
    expect(expr()).toHaveValue("0 9 * * 1-5");
    expect(mode()).toHaveTextContent("auto");
    expect(screen.queryByTestId("schedule-notice")).toBeNull();
  });

  it("converts out of Auto by detecting what was typed", () => {
    render(<App initial={{ expr: "*/5 * * * *", mode: "auto" }} />);
    fireEvent.click(radio("RRULE"));
    expect(expr()).toHaveValue("FREQ=MINUTELY;INTERVAL=5");
  });

  it("still switches syntax and warns when the text cannot be converted", () => {
    render(<App initial={{ expr: "*/7 * * * *", mode: "cron" }} />);
    fireEvent.click(radio("Quartz"));
    expect(expr()).toHaveValue("*/7 * * * *");
    expect(mode()).toHaveTextContent("quartz");
    expect(screen.getByTestId("schedule-notice")).toHaveTextContent(/Couldn't convert this to Quartz: every 7 minutes restarts each hour/);
  });

  it("replaces the previous warning with the next result, and clears it on success", () => {
    render(<App initial={{ expr: "17 * * * *", mode: "cron" }} />);
    fireEvent.click(radio("Natural"));
    expect(screen.getByTestId("schedule-notice")).toHaveTextContent("to Natural");
    fireEvent.click(radio("Quartz"));
    expect(screen.getByTestId("schedule-notice")).toHaveTextContent("to Quartz");
    fireEvent.click(radio("Auto"));
    expect(screen.queryByTestId("schedule-notice")).toBeNull();
  });

  it("converts blank input without a notice", () => {
    render(<App initial={{ expr: "", mode: "natural" }} />);
    for (const name of ["Cron", "Quartz", "RRULE", "Natural", "Auto"]) {
      fireEvent.click(radio(name));
      expect(expr()).toHaveValue("");
      expect(screen.queryByTestId("schedule-notice")).toBeNull();
    }
  });

  it("is keyboard operable: arrow keys move the checked syntax and convert", async () => {
    const user = userEvent.setup();
    render(<App initial={{ expr: "every day at 9am", mode: "natural" }} />);
    radio("Natural").focus();
    await user.keyboard("{ArrowRight}");
    expect(radio("Cron")).toBeChecked();
    expect(expr()).toHaveValue("0 9 * * *");
    await user.keyboard("{ArrowRight}");
    expect(expr()).toHaveValue("0 0 9 * * ?");
    await user.keyboard("{ArrowLeft}{ArrowLeft}");
    expect(radio("Natural")).toBeChecked();
    expect(expr()).toHaveValue("every day at 09:00");
  });

  it("clicking the checked syntax again does nothing", () => {
    const onChange = vi.fn();
    render(<ScheduleSyntaxToggle name="x" value={{ expr: "0 9 * * *", mode: "cron" }} onChange={onChange} />);
    fireEvent.click(radio("Cron"));
    expect(onChange).not.toHaveBeenCalled();
  });

  it("passes the converted value and notice to onChange, and never mutates the input value", () => {
    const onChange = vi.fn();
    const value = Object.freeze({ expr: "0 9 * * *", mode: "cron" as const });
    render(<ScheduleSyntaxToggle name="x" value={value} onChange={onChange} />);
    fireEvent.click(radio("Quartz"));
    expect(onChange).toHaveBeenCalledWith({ expr: "0 0 9 * * ?", mode: "quartz" }, null);
    expect(value).toEqual({ expr: "0 9 * * *", mode: "cron" });
  });

  it("keeps two instances independent when given different names", () => {
    render(
      <>
        <div data-testid="a">
          <ScheduleSyntaxToggle name="a" value={{ expr: "", mode: "cron" }} onChange={() => {}} />
        </div>
        <div data-testid="b">
          <ScheduleSyntaxToggle name="b" value={{ expr: "", mode: "rrule" }} onChange={() => {}} />
        </div>
      </>,
    );
    expect(within(screen.getByTestId("a")).getByRole("radio", { name: "Cron" })).toBeChecked();
    expect(within(screen.getByTestId("b")).getByRole("radio", { name: "RRULE" })).toBeChecked();
    expect(within(screen.getByTestId("b")).getByRole("radio", { name: "Cron" })).not.toBeChecked();
  });

  it("applies a caller's class to the group", () => {
    render(<ScheduleSyntaxToggle name="x" className="custom-class" value={{ expr: "", mode: "auto" }} onChange={() => {}} />);
    expect(screen.getByRole("group", { name: "Schedule syntax" })).toHaveClass("custom-class");
  });
});

describe("SchedulePresets", () => {
  it("renders the five presets as a labelled group of plain buttons", () => {
    render(<App initial={{ expr: "", mode: "auto" }} />);
    const group = screen.getByRole("group", { name: "Schedule presets" });
    const buttons = within(group).getAllByRole("button");
    expect(buttons.map((b) => b.textContent)).toEqual(["every 5 min", "hourly", "weekdays 9am", "daily", "1st of month"]);
    for (const b of buttons) expect(b).toHaveAttribute("type", "button");
  });

  it("does not submit the surrounding form", () => {
    const onSubmit = vi.fn();
    render(<App initial={{ expr: "", mode: "cron" }} onSubmit={onSubmit} />);
    for (const name of ["every 5 min", "hourly", "weekdays 9am", "daily", "1st of month"]) fireEvent.click(preset(name));
    expect(onSubmit).not.toHaveBeenCalled();
  });

  it.each([
    ["auto", { "every 5 min": "every 5 minutes", "weekdays 9am": "every weekday at 09:00", "1st of month": "on the 1st of every month at 09:00", hourly: "23 * * * *", daily: "23 9 * * *" }],
    ["natural", { "every 5 min": "every 5 minutes", "weekdays 9am": "every weekday at 09:00", "1st of month": "on the 1st of every month at 09:00", hourly: "every hour", daily: "every day at 09:23" }],
    ["cron", { "every 5 min": "*/5 * * * *", "weekdays 9am": "0 9 * * 1-5", "1st of month": "0 9 1 * *", hourly: "23 * * * *", daily: "23 9 * * *" }],
    ["quartz", { "every 5 min": "0 */5 * * * ?", "weekdays 9am": "0 0 9 ? * MON-FRI", "1st of month": "0 0 9 1 * ?", hourly: "0 23 * * * ?", daily: "0 23 9 * * ?" }],
    ["rrule", { "every 5 min": "FREQ=MINUTELY;INTERVAL=5", "weekdays 9am": "FREQ=WEEKLY;BYDAY=MO,TU,WE,TH,FR;BYHOUR=9;BYMINUTE=0", "1st of month": "FREQ=MONTHLY;BYMONTHDAY=1;BYHOUR=9;BYMINUTE=0", hourly: "FREQ=HOURLY;INTERVAL=1;BYMINUTE=23", daily: "FREQ=DAILY;BYHOUR=9;BYMINUTE=23" }],
  ] as const)("in %s every preset writes that syntax and keeps the mode", (syntax, expected) => {
    render(<App initial={{ expr: "", mode: syntax }} random={() => 0.39} />);
    for (const [name, text] of Object.entries(expected)) {
      fireEvent.click(preset(name));
      expect(expr(), `${syntax} ${name}`).toHaveValue(text);
      expect(mode()).toHaveTextContent(syntax);
    }
  });

  it("replaces whatever was typed, even an invalid expression", () => {
    render(<App initial={{ expr: "garbage", mode: "cron" }} />);
    fireEvent.click(preset("weekdays 9am"));
    expect(expr()).toHaveValue("0 9 * * 1-5");
  });

  it("warns for hourly in Natural, and the warning clears with the next clean preset", () => {
    render(<App initial={{ expr: "", mode: "natural" }} random={() => 0.39} />);
    fireEvent.click(preset("hourly"));
    expect(expr()).toHaveValue("every hour");
    expect(screen.getByTestId("schedule-notice")).toHaveTextContent("top of the hour");
    fireEvent.click(preset("daily"));
    expect(screen.queryByTestId("schedule-notice")).toBeNull();
  });

  it("spreads a new minute on every click", () => {
    const samples = [0.1, 0.5, 0.9];
    let i = 0;
    render(<App initial={{ expr: "", mode: "cron" }} random={() => samples[i++ % samples.length] ?? 0} />);
    const seen = [1, 2, 3].map(() => {
      fireEvent.click(preset("hourly"));
      return (expr() as HTMLInputElement).value;
    });
    expect(seen).toEqual(["6 * * * *", "30 * * * *", "54 * * * *"]);
  });

  it("lets the user edit after a preset and clears the notice", () => {
    render(<App initial={{ expr: "", mode: "natural" }} />);
    fireEvent.click(preset("hourly"));
    fireEvent.change(expr(), { target: { value: "every 2 hours" } });
    expect(screen.queryByTestId("schedule-notice")).toBeNull();
  });
});

describe("ScheduleNotice", () => {
  it("renders nothing without a message", () => {
    const { container } = render(<ScheduleNotice message={null} />);
    expect(container).toBeEmptyDOMElement();
  });

  it("renders the message as a polite status with a hidden warning glyph", () => {
    render(<ScheduleNotice message="Something to know" />);
    const notice = screen.getByRole("status");
    expect(notice).toHaveTextContent("Something to know");
    expect(notice).toHaveAttribute("data-testid", "schedule-notice");
    expect(within(notice).getByText("⚠")).toHaveAttribute("aria-hidden", "true");
  });

  it("renders an empty string as nothing", () => {
    const { container } = render(<ScheduleNotice message="" />);
    expect(container).toBeEmptyDOMElement();
  });
});
