import type { TagOption } from "@/components/ui/tag-input";

const STATUS: [number, string][] = [
  [200, "OK"], [201, "Created"], [202, "Accepted"], [204, "No Content"], [301, "Moved Permanently"], [302, "Found"], [304, "Not Modified"],
  [400, "Bad Request"], [401, "Unauthorized"], [403, "Forbidden"], [404, "Not Found"], [408, "Request Timeout"], [409, "Conflict"], [410, "Gone"], [422, "Unprocessable Content"], [425, "Too Early"], [429, "Too Many Requests"],
  [500, "Internal Server Error"], [501, "Not Implemented"], [502, "Bad Gateway"], [503, "Service Unavailable"], [504, "Gateway Timeout"],
];
// Everything else in 100–599 is still a valid code; list it unnamed so typing "47" finds 470–479.
const named = new Map(STATUS);
const everyStatus = (): TagOption[] => {
  const rest: TagOption[] = [];
  for (let code = 100; code <= 599; code += 1) if (!named.has(code)) rest.push({ value: String(code) });
  return rest;
};
const classOf = (code: number) => `${Math.floor(code / 100)}xx`;
const byCode = ([code, label]: [number, string]): TagOption => ({ value: String(code), label, coveredBy: classOf(code) });
const bare = (o: TagOption): TagOption => ({ ...o, coveredBy: classOf(Number(o.value)) });

/** Success codes: class shortcuts first, then the named codes, then the rest. */
export const SUCCESS_CODE_OPTIONS: TagOption[] = [
  { value: "2xx", label: "any 200–299 (default)" },
  { value: "3xx", label: "any redirect" },
  { value: "4xx", label: "any client error" },
  { value: "5xx", label: "any server error" },
  ...STATUS.map(byCode),
  ...everyStatus().map(bare),
];

/** Retry-on: keywords and classes first, then the usual transient codes, then the rest. */
export const RETRY_ON_OPTIONS: TagOption[] = [
  { value: "network", label: "connection failed" },
  { value: "timeout", label: "request timed out" },
  { value: "5xx", label: "any server error" },
  { value: "4xx", label: "any client error" },
  ...[429, 408, 503, 502, 504, 500].map((c) => byCode([c, named.get(c)!])),
  ...STATUS.filter(([c]) => ![429, 408, 503, 502, 504, 500].includes(c)).map(byCode),
  ...everyStatus().map(bare),
];

const EXIT: Record<number, string> = {
  0: "success", 1: "general error", 2: "misuse of shell builtin", 64: "usage error", 65: "bad input data", 69: "service unavailable", 70: "internal software error",
  73: "can't create output", 74: "I/O error", 75: "temporary failure", 78: "configuration error", 111: "temporary failure (daemontools)", 126: "not executable", 127: "command not found",
  130: "interrupted (SIGINT)", 137: "killed (SIGKILL)", 139: "segfault (SIGSEGV)", 143: "terminated (SIGTERM)",
};
const EXIT_FIRST = [0, 1, 2, 75, 111, 64, 78, 126, 127, 130, 137, 143];
const exitOption = (n: number): TagOption => ({ value: String(n), label: EXIT[n] });

/** 0–255, with the well-known codes first so the empty-field suggestions are useful. */
export const EXIT_CODE_OPTIONS: TagOption[] = [
  ...EXIT_FIRST.map(exitOption),
  ...Array.from({ length: 256 }, (_, n) => n).filter((n) => !EXIT_FIRST.includes(n)).map(exitOption),
];

