import Ajv from "ajv";
import { defaults } from "./defaults";
import schema from "../schema/portal.schema.json";
import type { Catalog, Entry, PortalConfig, Tier } from "./types";

const validator = new Ajv({ allErrors: true, strict: true }).compile<PortalConfig>(schema);

/**
 * Parse and validate a configuration document against the published schema.
 * Throws an Error whose message lists every violation, so the page can say
 * what is wrong rather than silently showing the defaults.
 */
export function parseConfig(input: unknown): PortalConfig {
  if (!validator(input)) {
    const problems = (validator.errors ?? []).map(
      (e) => `${e.instancePath || "/"} ${e.message ?? "is invalid"}`,
    );
    throw new Error(`portal.json is invalid: ${problems.join("; ")}`);
  }
  return input;
}

/** Group entries into tiers: `tierOrder` first, the rest by first appearance. */
export function groupTiers(entries: Entry[], tierOrder: string[] = []): Tier[] {
  // A later entry of the same name replaces the earlier one in place.
  const byName = new Map<string, Entry>();
  for (const entry of entries) {
    byName.set(entry.name, entry);
  }

  const byTier = new Map<string, Entry[]>();
  for (const entry of byName.values()) {
    const list = byTier.get(entry.tier);
    if (list) {
      list.push(entry);
    } else {
      byTier.set(entry.tier, [entry]);
    }
  }

  const names = [
    ...tierOrder.filter((n) => byTier.has(n)),
    ...[...byTier.keys()].filter((n) => !tierOrder.includes(n)),
  ];
  return names.map((name) => ({ name, entries: byTier.get(name) ?? [] }));
}

/**
 * Merge the extra configuration into the built-in catalog.
 *
 * Additive by default: the extra notes, recipes and guides follow the
 * built-in ones, and the extra entries make the tiers. `replaceDefaults`
 * drops the built-in sections first.
 */
export function buildCatalog(extra: PortalConfig = {}, base: Catalog = defaults): Catalog {
  const start: Catalog = extra.replaceDefaults
    ? { ...base, orientation: [], commandLine: [], guides: [], tiers: [] }
    : base;

  const baseEntries = start.tiers.flatMap((t) => t.entries);

  return {
    title: extra.title ?? start.title,
    lede: extra.lede ?? start.lede,
    issuer: extra.issuer ?? start.issuer,
    tiers: groupTiers([...baseEntries, ...(extra.entries ?? [])], [
      ...(extra.tierOrder ?? []),
    ]),
    orientation: [...start.orientation, ...(extra.orientation ?? [])],
    commandLine: [...start.commandLine, ...(extra.commandLine ?? [])],
    guides: [...start.guides, ...(extra.guides ?? [])],
  };
}

export const configPath = "/config/portal.json";

export interface Loaded {
  catalog: Catalog;
  /** Set when the config was present but unusable; the defaults are shown. */
  problem?: string;
}

/**
 * Load the runtime configuration. Not mounted (404) is a normal state and
 * shows the built-in catalog; anything else that goes wrong shows the
 * built-in catalog AND says so.
 */
export async function load(fetcher: typeof fetch = fetch): Promise<Loaded> {
  let response: Response;
  try {
    response = await fetcher(configPath, { cache: "no-store" });
  } catch (err) {
    return { catalog: buildCatalog(), problem: `${configPath} could not be fetched: ${String(err)}` };
  }
  if (response.status === 404) {
    return { catalog: buildCatalog() };
  }
  if (!response.ok) {
    return { catalog: buildCatalog(), problem: `${configPath} answered ${response.status}` };
  }
  try {
    return { catalog: buildCatalog(parseConfig(await response.json())) };
  } catch (err) {
    return { catalog: buildCatalog(), problem: err instanceof Error ? err.message : String(err) };
  }
}
