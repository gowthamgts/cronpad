import { describe, expect, it } from "vitest";
import { parseCurl, shellWords } from "./curlImport";

describe("shellWords", () => {
  it("handles quotes, escapes, $'…' strings and line continuations", () => {
    expect(shellWords(`curl 'a b' "c \\"d\\" $X" e\\ f \\\n  $'g\\nh\\'i' -H'X: y'`)).toEqual(["curl", "a b", 'c "d" $X', "e f", "g\nh'i", "-HX: y"]);
  });

  it("rejects unclosed quotes and chained commands", () => {
    expect(() => shellWords("curl 'oops")).toThrow(/single quote/);
    expect(() => shellWords('curl "oops')).toThrow(/double quote/);
    expect(() => shellWords("curl https://a.test | jq")).toThrow(/single curl command/);
    expect(shellWords("curl https://a.test/?a=1&b=2")).toEqual(["curl", "https://a.test/?a=1&b=2"]);
  });
});

describe("parseCurl", () => {
  it("imports a JSON POST", () => {
    const r = parseCurl(`curl -X POST https://api.example.com/hook \\
      -H "Authorization: Bearer {{secret.TOKEN}}" \\
      -H 'Content-Type: application/json' \\
      -d '{"run": "{{run.id}}"}'`);
    expect(r).toEqual({
      method: "POST",
      url: "https://api.example.com/hook",
      headers: [{ name: "Authorization", value: "Bearer {{secret.TOKEN}}" }, { name: "Content-Type", value: "application/json" }],
      body: '{"run": "{{run.id}}"}',
      followRedirects: false,
      warnings: [],
    });
  });

  it("follows curl's defaults for method, scheme and -d content type", () => {
    expect(parseCurl("curl example.com/ping")).toMatchObject({ method: "GET", url: "http://example.com/ping", headers: [], body: "" });
    const form = parseCurl("curl -d a=1 --data b=2 https://x.test");
    expect(form).toMatchObject({ method: "POST", body: "a=1&b=2", headers: [{ name: "Content-Type", value: "application/x-www-form-urlencoded" }] });
    expect(parseCurl(`curl --json '{"a":1}' https://x.test`).headers).toEqual([{ name: "Content-Type", value: "application/json" }, { name: "Accept", value: "application/json" }]);
    expect(parseCurl("curl -G -d q=1 https://x.test/s?p=2")).toMatchObject({ method: "GET", url: "https://x.test/s?p=2&q=1", body: "" });
  });

  it("handles bundled short options and attached values", () => {
    expect(parseCurl("curl -sSL -XPUT -d x https://x.test")).toMatchObject({ method: "PUT", followRedirects: true, body: "x", warnings: [] });
  });

  it("converts --user, cookies and referer into headers", () => {
    const r = parseCurl("curl -u me:pw -b 'a=1' -b 'b=2' -e https://ref.test https://x.test");
    expect(r.headers).toEqual([{ name: "Cookie", value: "a=1; b=2" }, { name: "Referer", value: "https://ref.test" }, { name: "Authorization", value: `Basic ${btoa("me:pw")}` }]);
    expect(r.warnings[0]).toMatch(/secret/);
  });

  it("drops headers cronpad sets and warns about unknown options", () => {
    const r = parseCurl("curl -A bot -H 'Host: x' -H 'Accept-Encoding: gzip' -H 'Cronpad-Job-Id: j' -H 'X-Removed:' --frobnicate https://x.test");
    expect(r.headers).toEqual([]);
    expect(r.warnings).toHaveLength(5);
    expect(r.warnings.some((w) => w.includes("--frobnicate"))).toBe(true);
  });

  it("imports a browser's Copy as cURL output", () => {
    const r = parseCurl(`curl 'https://x.test/api' \\
  -H 'accept: */*' \\
  -H 'content-type: application/json' \\
  --data-raw $'{"name":"it\\'s"}'`);
    expect(r).toMatchObject({ method: "POST", body: `{"name":"it's"}`, headers: [{ name: "accept", value: "*/*" }, { name: "content-type", value: "application/json" }] });
  });

  it("explains what it can't import", () => {
    expect(() => parseCurl("wget https://x.test")).toThrow(/Start the command with curl/);
    expect(() => parseCurl("curl -X POST")).toThrow(/No URL/);
    expect(() => parseCurl("curl https://a.test https://b.test")).toThrow(/more than one URL/);
    expect(() => parseCurl("curl -X OPTIONS https://x.test")).toThrow(/OPTIONS isn't supported/);
    expect(() => parseCurl("curl -d @body.json https://x.test")).toThrow(/local file/);
    expect(() => parseCurl("curl --data-urlencode name@file https://x.test")).toThrow(/local file/);
    expect(() => parseCurl("curl -F f=@a.png https://x.test")).toThrow(/Multipart/);
    expect(() => parseCurl("curl -H")).toThrow(/-H needs a value/);
  });

  it("url-encodes --data-urlencode content", () => {
    expect(parseCurl("curl --data-urlencode 'q=a b&c' --data-urlencode '=x@y' https://x.test").body).toBe("q=a%20b%26c&x%40y");
  });
});
