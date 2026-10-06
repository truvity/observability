import { useEffect, useState } from "react";
import { buildCatalog, load, type Loaded } from "./catalog";
import { Portal } from "./Portal";

export function App() {
  const [loaded, setLoaded] = useState<Loaded>({ catalog: buildCatalog() });

  useEffect(() => {
    let live = true;
    void load().then((l) => {
      if (live) setLoaded(l);
    });
    return () => {
      live = false;
    };
  }, []);

  return <Portal catalog={loaded.catalog} problem={loaded.problem} />;
}
