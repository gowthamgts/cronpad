import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import { describe, expect, it } from "vitest";
import { SinkEditor } from "@/components/SinkEditor";
import { emptyForm } from "@/lib/jobForm";

function App() {
  const [values, setValues] = useState(emptyForm("UTC"));
  return <SinkEditor values={values} onChange={setValues} errors={{}} commandAllowed={false} />;
}

describe("SinkEditor curl import", () => {
  it("fills the webhook fields from a pasted curl command", async () => {
    const user = userEvent.setup();
    render(<App />);
    await user.click(screen.getByRole("button", { name: "Import from curl" }));
    const box = screen.getByLabelText("curl command");
    await user.click(box);
    await user.paste(`curl -X PUT https://api.example.com/hook -H 'X-Team: ops' -A bot -d '{"a":1}'`);
    expect(screen.getByRole("status")).toHaveTextContent("PUT https://api.example.com/hook");
    expect(screen.getByText(/Dropped the user agent/)).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Import" }));

    expect(screen.queryByLabelText("curl command")).not.toBeInTheDocument();
    expect(screen.getByLabelText("URL")).toHaveValue("https://api.example.com/hook");
    expect(screen.getByLabelText("Header 1 name")).toHaveValue("X-Team");
    expect(screen.getByLabelText("Header 2 value")).toHaveValue("application/x-www-form-urlencoded");
    expect(screen.getByLabelText("Body")).toHaveValue('{"a":1}');
  });

  it("shows why a command can't be imported", async () => {
    const user = userEvent.setup();
    render(<App />);
    await user.click(screen.getByRole("button", { name: "Import from curl" }));
    await user.click(screen.getByLabelText("curl command"));
    await user.paste("curl -X OPTIONS https://x.test");
    expect(screen.getByText(/OPTIONS isn't supported/)).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Import" })).toBeDisabled();
  });
});
