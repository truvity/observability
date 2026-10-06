import type { Catalog } from "./types";

/**
 * The built-in catalog: generic, and carrying nothing about any estate.
 *
 * It lists no applications, because which ones exist is the estate's to say.
 * It says where to say it. An install that mounts nothing shows this page,
 * which is the correct, empty answer.
 */
export const defaults: Catalog = {
  title: "Platform",
  lede: "Everything this estate serves, by tier. Every link meets its own gate.",
  tiers: [],
  orientation: [
    {
      title: "This is the default catalog",
      body:
        "Nothing is listed yet. The page loads extra entries and sections from " +
        "/config/portal.json when the file is mounted; with the Helm chart, set " +
        "portal.extraEntries and the sections in the chart's values.",
    },
  ],
  commandLine: [],
  guides: [],
};
