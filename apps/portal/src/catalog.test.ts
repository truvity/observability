import { readdirSync, readFileSync } from "node:fs";
import { join } from "node:path";
import { describe, expect, it } from "vitest";
import { renderToStaticMarkup } from "react-dom/server";
import { buildCatalog, groupTiers, load, parseConfig } from "./catalog";
import { defaults } from "./defaults";
import { Portal } from "./Portal";
import type { Entry, PortalConfig } from "./types";

const dir = join(import.meta.dirname, "..", "testdata");
const read = (name: string): unknown => JSON.parse(readFileSync(join(dir, name), "utf8"));

describe("schema", () => {
  const files = readdirSync(dir).filter((f) => f.endsWith(".json"));

  it("has fixtures of both kinds", () => {
    expect(files.some((f) => f.startsWith("valid-"))).toBe(true);
    expect(files.some((f) => f.startsWith("invalid-"))).toBe(true);
  });

  for (const file of files.filter((f) => f.startsWith("valid-"))) {
    it(`accepts ${file}`, () => {
      expect(() => parseConfig(read(file))).not.toThrow();
    });
  }

  for (const file of files.filter((f) => f.startsWith("invalid-"))) {
    it(`refuses ${file}`, () => {
      expect(() => parseConfig(read(file))).toThrow(/portal.json is invalid/);
    });
  }
});

const entry = (name: string, tier: string, extra: Partial<Entry> = {}): Entry => ({
  name,
  title: name,
  url: `https://${name}.example.test/`,
  tier,
  ...extra,
});

describe("groupTiers", () => {
  it("orders by tierOrder, then by first appearance", () => {
    const tiers = groupTiers([entry("a", "x"), entry("b", "y"), entry("c", "z")], ["z"]);
    expect(tiers.map((t) => t.name)).toEqual(["z", "x", "y"]);
  });

  it("ignores a tierOrder name with no entries", () => {
    expect(groupTiers([entry("a", "x")], ["nope", "x"]).map((t) => t.name)).toEqual(["x"]);
  });

  it("lets a later entry of the same name replace the earlier one", () => {
    const tiers = groupTiers([entry("a", "x", { title: "old" }), entry("a", "x", { title: "new" })]);
    expect(tiers[0]?.entries.map((e) => e.title)).toEqual(["new"]);
  });
});

describe("buildCatalog", () => {
  it("is the generic default with no configuration, and names no estate", () => {
    const c = buildCatalog();
    expect(c.tiers).toEqual([]);
    expect(c.orientation).toEqual(defaults.orientation);
    expect(JSON.stringify(c)).not.toMatch(/truvity|kernel|devel/i);
  });

  it("adds to the defaults", () => {
    const extra = parseConfig({
      entries: [entry("a", "x")],
      orientation: [{ title: "N", body: "b" }],
    });
    const c = buildCatalog(extra);
    expect(c.orientation.map((n) => n.title)).toEqual([defaults.orientation[0]?.title, "N"]);
    expect(c.tiers.map((t) => t.name)).toEqual(["x"]);
  });

  it("drops the built-in sections with replaceDefaults", () => {
    const c = buildCatalog(parseConfig({ replaceDefaults: true, entries: [entry("a", "x")] }));
    expect(c.orientation).toEqual([]);
  });

  it("takes title, lede and issuer from the configuration", () => {
    const c = buildCatalog(read("valid-full.json") as PortalConfig);
    expect([c.title, c.lede, c.issuer]).toEqual([
      "Example estate",
      "What example.test serves.",
      "https://login.example.test",
    ]);
    expect(c.tiers.map((t) => t.name)).toEqual(["ops", "dev"]);
  });
});

describe("load", () => {
  const answer = (status: number, body?: unknown): typeof fetch =>
    (() => Promise.resolve(new Response(body === undefined ? null : JSON.stringify(body), { status }))) as typeof fetch;

  it("shows the defaults quietly when nothing is mounted", async () => {
    const l = await load(answer(404));
    expect(l.problem).toBeUndefined();
    expect(l.catalog.tiers).toEqual([]);
  });

  it("applies a valid document", async () => {
    const l = await load(answer(200, read("valid-full.json")));
    expect(l.problem).toBeUndefined();
    expect(l.catalog.tiers.length).toBe(2);
  });

  it("shows the defaults and says why for an invalid document", async () => {
    const l = await load(answer(200, read("invalid-javascript-url.json")));
    expect(l.problem).toMatch(/invalid/);
    expect(l.catalog.tiers).toEqual([]);
  });

  it("reports a server error", async () => {
    expect((await load(answer(500))).problem).toMatch(/500/);
  });

  it("reports a network failure", async () => {
    const failing = (() => Promise.reject(new Error("offline"))) as typeof fetch;
    expect((await load(failing)).problem).toMatch(/offline/);
  });
});

describe("Portal", () => {
  it("lists every entry with its badges, requirement and runbook", () => {
    const html = renderToStaticMarkup(Portal({ catalog: buildCatalog(read("valid-full.json") as PortalConfig) }));
    for (const want of ["Dashboards", "ops:viewer", "Runbook", ">tailnet<", ">api<", "kubectl get pods -A", "Handbook"]) {
      expect(html).toContain(want);
    }
  });

  it("escapes strings from the configuration", () => {
    const evil = buildCatalog({ title: "<img src=x onerror=alert(1)>" });
    expect(renderToStaticMarkup(Portal({ catalog: evil }))).not.toContain("<img");
  });

  it("shows the problem when the configuration was refused", () => {
    expect(renderToStaticMarkup(Portal({ catalog: buildCatalog(), problem: "bad" }))).toContain('role="alert"');
  });
});
