/** What /config/portal.json may hold. Mirrors schema/portal.schema.json. */
export interface Entry {
  name: string;
  title: string;
  description?: string;
  url: string;
  host?: string;
  tier: string;
  tailnet?: boolean;
  catalog?: boolean;
  requires?: string[];
  runbook?: string;
}

export interface Note {
  title: string;
  body: string;
}

export interface Recipe {
  title: string;
  body: string;
  command: string;
}

export interface Guide {
  title: string;
  links: { title: string; url: string }[];
}

export interface PortalConfig {
  version?: 1;
  title?: string;
  lede?: string;
  issuer?: string;
  replaceDefaults?: boolean;
  tierOrder?: string[];
  entries?: Entry[];
  orientation?: Note[];
  commandLine?: Recipe[];
  guides?: Guide[];
}

export interface Tier {
  name: string;
  entries: Entry[];
}

/** The page's whole input, after the built-ins and the extra config are merged. */
export interface Catalog {
  title: string;
  lede: string;
  issuer?: string;
  tiers: Tier[];
  orientation: Note[];
  commandLine: Recipe[];
  guides: Guide[];
}
