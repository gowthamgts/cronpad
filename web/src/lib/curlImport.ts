import type { Header } from "./jobForm";

/** The webhook fields a curl command fills in, plus notes on anything it could not carry over. */
export interface CurlImport {
  method: string;
  url: string;
  headers: Header[];
  body: string;
  followRedirects: boolean;
  warnings: string[];
}

const METHODS = ["GET", "POST", "PUT", "PATCH", "DELETE"];

// Set by cronpad or the HTTP client on every request; the server rejects them in a job.
const RESERVED = new Set([
  "host", "content-length", "connection", "transfer-encoding", "upgrade", "proxy-authorization", "proxy-authenticate", "te", "trailer",
  "webhook-id", "webhook-timestamp", "webhook-signature", "idempotency-key", "traceparent", "tracestate", "user-agent",
]);

// Options that change nothing cronpad sends, with and without an argument.
const IGNORED_FLAGS = new Set([
  "-s", "--silent", "-S", "--show-error", "-v", "--verbose", "-i", "--include", "-k", "--insecure", "-f", "--fail", "--fail-with-body",
  "-#", "--progress-bar", "-N", "--no-buffer", "--compressed", "-g", "--globoff", "-0", "--http1.0", "--http1.1", "--http2", "--http3",
  "-4", "--ipv4", "-6", "--ipv6", "-n", "--netrc", "-O", "--remote-name", "-J", "--remote-header-name", "--location-trusted", "-q", "--disable",
]);
const IGNORED_WITH_ARG = new Set([
  "-o", "--output", "-w", "--write-out", "-m", "--max-time", "--connect-timeout", "--retry", "--retry-delay", "--retry-max-time",
  "-x", "--proxy", "-U", "--proxy-user", "--cacert", "--capath", "-E", "--cert", "--key", "--cert-type", "--key-type", "-c", "--cookie-jar",
  "--resolve", "--connect-to", "--max-redirs", "-D", "--dump-header", "--trace", "--trace-ascii", "--limit-rate", "-r", "--range",
  "--interface", "--dns-servers", "-K", "--config", "-C", "--continue-at", "-Y", "--speed-limit", "-y", "--speed-time",
]);
const SHORT_WITH_ARG = new Set("XHdbeuAFTomwxUEcDrKCYy".split(""));
const DATA = new Set(["-d", "--data", "--data-ascii", "--data-binary", "--data-raw", "--data-urlencode", "--json"]);

/** Splits a POSIX-shell command line into words: quotes, backslash escapes, $'…' strings and line continuations. */
export function shellWords(input: string): string[] {
  const words: string[] = [];
  let word = "";
  let inWord = false;
  let i = 0;
  const s = input.replace(/\r\n?/g, "\n");
  while (i < s.length) {
    const ch = s[i];
    if (ch === "\\") {
      // A backslash before a newline continues the line; otherwise it makes the next character literal.
      if (s[i + 1] === "\n") { i += 2; continue; }
      if (i + 1 < s.length) { word += s[i + 1]; inWord = true; }
      i += 2;
    } else if (ch === "'") {
      const end = s.indexOf("'", i + 1);
      if (end < 0) throw new Error("Unclosed single quote");
      word += s.slice(i + 1, end);
      inWord = true;
      i = end + 1;
    } else if (ch === "$" && s[i + 1] === "'") {
      const [text, end] = ansiC(s, i + 2);
      word += text;
      inWord = true;
      i = end;
    } else if (ch === '"') {
      i++;
      for (;;) {
        if (i >= s.length) throw new Error("Unclosed double quote");
        const c = s[i];
        if (c === '"') { i++; break; }
        if (c === "\\" && i + 1 < s.length && '"\\$`\n'.includes(s[i + 1]!)) {
          if (s[i + 1] !== "\n") word += s[i + 1];
          i += 2;
        } else { word += c; i++; }
      }
      inWord = true;
    } else if (ch === " " || ch === "\t" || ch === "\n") {
      if (inWord) words.push(word);
      word = "";
      inWord = false;
      i++;
    } else if (ch === "#" && !inWord) {
      while (i < s.length && s[i] !== "\n") i++;
    } else if ((ch === "|" || ch === ";" || ch === "&" || ch === ">" || ch === "<") && !inWord) {
      throw new Error("Paste a single curl command, without pipes, redirects or chained commands");
    } else {
      word += ch;
      inWord = true;
      i++;
    }
  }
  if (inWord) words.push(word);
  return words;
}

/** Reads a bash $'…' string starting after the opening quote; returns its text and the index after the closing quote. */
function ansiC(s: string, start: number): [string, number] {
  const simple: Record<string, string> = { n: "\n", t: "\t", r: "\r", "\\": "\\", "'": "'", '"': '"', a: "\x07", b: "\b", e: "\x1b", E: "\x1b", f: "\f", v: "\v", "?": "?" };
  let out = "";
  let i = start;
  while (i < s.length) {
    const c = s[i];
    if (c === "'") return [out, i + 1];
    if (c !== "\\") { out += c; i++; continue; }
    const n = s[i + 1] ?? "";
    if (n in simple) { out += simple[n]!; i += 2; continue; }
    const hex = n === "x" ? /^[0-9a-fA-F]{1,2}/ : n === "u" ? /^[0-9a-fA-F]{1,4}/ : n === "U" ? /^[0-9a-fA-F]{1,8}/ : null;
    const m = hex?.exec(s.slice(i + 2));
    if (m) { out += String.fromCodePoint(parseInt(m[0], 16)); i += 2 + m[0].length; continue; }
    const oct = /^[0-7]{1,3}/.exec(s.slice(i + 1));
    if (oct) { out += String.fromCharCode(parseInt(oct[0], 8)); i += 1 + oct[0].length; continue; }
    out += c;
    i++;
  }
  throw new Error("Unclosed $'…' string");
}

const utf8Base64 = (s: string) => btoa(String.fromCharCode(...new TextEncoder().encode(s)));

/** Turns a pasted curl command (bash syntax, as browsers' "Copy as cURL" produce) into webhook settings. */
export function parseCurl(input: string): CurlImport {
  const words = shellWords(input.trim().replace(/^\$\s+/, ""));
  if (words[0] !== "curl") throw new Error("Start the command with curl");
  const warnings: string[] = [];
  const headers: Header[] = [];
  const data: string[] = [];
  const urls: string[] = [];
  let method = "";
  let followRedirects = false;
  let get = false;
  let json = false;
  let user = "";

  const addHeader = (raw: string) => {
    const colon = raw.indexOf(":");
    const semi = raw.indexOf(";");
    // "Name;" sends an empty header; "Name:" with nothing after it tells curl to drop the header.
    if (colon < 0 && semi > 0 && raw.slice(semi + 1).trim() === "") { headers.push({ name: raw.slice(0, semi).trim(), value: "" }); return; }
    if (colon <= 0) { warnings.push(`Skipped header “${raw}”: expected Name: value`); return; }
    const value = raw.slice(colon + 1).trim();
    if (value === "") return;
    headers.push({ name: raw.slice(0, colon).trim(), value });
  };
  const addData = (opt: string, value: string) => {
    if (opt === "--json") json = true;
    if (opt === "--data-urlencode") {
      const eq = value.indexOf("=");
      const at = value.indexOf("@");
      // "@file" and "name@file" read a local file; after an "=" the rest is literal content.
      if (at >= 0 && (eq < 0 || at < eq)) throw new Error("--data-urlencode with @ reads a local file; paste its contents instead");
      value = eq < 0 ? encodeURIComponent(value) : `${value.slice(0, eq + 1)}${encodeURIComponent(value.slice(eq + 1))}`;
      if (eq === 0) value = value.slice(1);
    } else if (opt !== "--data-raw" && value.startsWith("@")) {
      throw new Error(`${opt} ${value} reads a local file; paste its contents instead`);
    }
    data.push(value);
  };

  const handle = (opt: string, next: () => string): void => {
    switch (opt) {
      case "-X": case "--request": method = next().toUpperCase(); return;
      case "-H": case "--header": addHeader(next()); return;
      case "-u": case "--user": user = next(); return;
      case "-A": case "--user-agent": next(); warnings.push("Dropped the user agent: cronpad sends its own"); return;
      case "-b": case "--cookie": { const v = next(); if (v.includes("=")) headers.push({ name: "Cookie", value: v }); else warnings.push(`Skipped --cookie ${v}: cookie files can't be imported`); return; }
      case "-e": case "--referer": headers.push({ name: "Referer", value: next().replace(/;auto$/, "") }); return;
      case "-L": case "--location": followRedirects = true; return;
      case "-G": case "--get": get = true; return;
      case "--url": urls.push(next()); return;
      case "-I": case "--head": throw new Error("HEAD requests aren't supported; use GET");
      case "-F": case "--form": case "--form-string": next(); throw new Error("Multipart form uploads (-F) aren't supported; send a JSON or URL-encoded body");
      case "-T": case "--upload-file": next(); throw new Error("File uploads (-T) aren't supported; paste the body with -d instead");
    }
    if (DATA.has(opt)) { addData(opt, next()); return; }
    if (IGNORED_WITH_ARG.has(opt)) { next(); return; }
    if (IGNORED_FLAGS.has(opt)) return;
    warnings.push(`Ignored unsupported option ${opt}`);
  };

  for (let i = 1; i < words.length; i++) {
    const w = words[i]!;
    const next = () => {
      if (++i >= words.length) throw new Error(`${w} needs a value`);
      return words[i]!;
    };
    if (w.startsWith("--") && w.length > 2) handle(w, next);
    else if (w.startsWith("-") && w.length > 1) {
      // Short options can be bundled (-sSL) and take their value attached (-XPOST) or as the next word.
      for (let j = 1; j < w.length; j++) {
        const opt = `-${w[j]!}`;
        if (SHORT_WITH_ARG.has(w[j]!)) {
          const rest = w.slice(j + 1);
          handle(opt, rest ? () => rest : next);
          break;
        }
        handle(opt, next);
      }
    } else urls.push(w);
  }

  if (urls.length === 0) throw new Error("No URL found in the command");
  if (urls.length > 1) throw new Error("The command has more than one URL; a webhook calls exactly one");
  let url = urls[0]!;
  // curl assumes http:// when the scheme is missing.
  if (!/^[a-z][a-z0-9+.-]*:\/\//i.test(url)) url = `http://${url}`;

  let body = data.join("&");
  if (get && data.length > 0) {
    url += (url.includes("?") ? "&" : "?") + body;
    body = "";
  }
  if (!method) method = get ? "GET" : data.length > 0 ? "POST" : "GET";
  if (!METHODS.includes(method)) throw new Error(`Method ${method} isn't supported; use ${METHODS.join(", ")}`);

  if (user) {
    const value = user.includes(":") ? user : `${user}:`;
    headers.push({ name: "Authorization", value: `Basic ${utf8Base64(value)}` });
    warnings.push("Turned --user into a Basic Authorization header; consider moving the credentials to a secret");
  }

  const has = (name: string) => headers.some((h) => h.name.toLowerCase() === name);
  if (json) {
    if (!has("content-type")) headers.push({ name: "Content-Type", value: "application/json" });
    if (!has("accept")) headers.push({ name: "Accept", value: "application/json" });
  } else if (data.length > 0 && !get && !has("content-type")) {
    // What curl sends for -d; without it cronpad would default to application/json.
    headers.push({ name: "Content-Type", value: "application/x-www-form-urlencoded" });
  }

  const kept: Header[] = [];
  for (const h of headers) {
    const lower = h.name.toLowerCase();
    if (RESERVED.has(lower) || lower.startsWith("cronpad-")) { warnings.push(`Dropped the ${h.name} header: cronpad sets it`); continue; }
    if (lower === "accept-encoding") { warnings.push("Dropped Accept-Encoding: compression is handled automatically"); continue; }
    const same = kept.find((k) => k.name.toLowerCase() === lower);
    // A job stores one value per header name, so repeats are combined the way HTTP allows.
    if (same) same.value += lower === "cookie" ? `; ${h.value}` : `, ${h.value}`;
    else kept.push({ ...h });
  }

  return { method, url, headers: kept, body, followRedirects, warnings };
}
