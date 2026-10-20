import { describe, expect, it, vi } from "vitest";
import { normalizeZone, zoneOffset, zoneOptions } from "./timezones";
describe("timezone options", () => {
  it("normalizes legacy IANA names", () => {
    for (const [legacy, current] of [["Asia/Calcutta", "Asia/Kolkata"], ["Europe/Kiev", "Europe/Kyiv"], ["Asia/Saigon", "Asia/Ho_Chi_Minh"], ["Asia/Katmandu", "Asia/Kathmandu"], ["Asia/Rangoon", "Asia/Yangon"]]) expect(normalizeZone(legacy!)).toBe(current);
  });
  it("keeps UTC first, groups regions and makes legacy names searchable", () => {
    const options = zoneOptions();
    expect(options[0]?.value).toBe("UTC");
    expect(options.find((o) => o.value === "Asia/Kolkata")).toMatchObject({group:"Asia", hint:"UTC+05:30", keywords: expect.stringContaining("Asia/Calcutta")});
    expect(new Set(options.map((o) => o.value)).size).toBe(options.length);
  });
  it("formats offsets and handles unavailable timezone inventory", () => {
    expect(zoneOffset("UTC")).toBe("UTC+00:00");
    const spy=vi.spyOn(Intl,"supportedValuesOf").mockImplementation(()=>{throw new Error("unsupported");});
    expect(zoneOptions().map((o)=>o.value)).toEqual(["UTC"]);spy.mockRestore();
  });
});
