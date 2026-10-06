import type { Catalog, Entry } from "./types";

function Card({ entry }: { entry: Entry }) {
  return (
    <div className="card">
      <a className="name" href={entry.url}>
        {entry.title}
        {entry.tailnet && (
          <span className="badge" title="Not reachable from the internet">
            tailnet
          </span>
        )}
        {entry.catalog && (
          <span className="badge" title="A host that is served; its gate, if any, is its own">
            api
          </span>
        )}
      </a>
      {entry.description && <div className="desc">{entry.description}</div>}
      {entry.host && <div className="host">{entry.host}</div>}
      {entry.requires && entry.requires.length > 0 && (
        <div className="gate">held by {entry.requires.join(", ")}</div>
      )}
      {entry.runbook && (
        <a className="runbook" href={entry.runbook}>
          Runbook &rarr;
        </a>
      )}
    </div>
  );
}

/** The page. Pure: everything it shows is in `catalog`, and React escapes every string. */
export function Portal({ catalog, problem }: { catalog: Catalog; problem?: string }) {
  return (
    <>
      <header>
        <div className="head">
          <h1>{catalog.title}</h1>
          <p className="lede">{catalog.lede}</p>
        </div>
      </header>
      <main>
        {problem && (
          <section>
            <div className="note problem" role="alert">
              <h3>The extra configuration was not applied</h3>
              <p>{problem}</p>
            </div>
          </section>
        )}
        {catalog.orientation.length > 0 && (
          <section>
            <h2>Start here</h2>
            <div className="grid">
              {catalog.orientation.map((n) => (
                <div className="note" key={n.title}>
                  <h3>{n.title}</h3>
                  <p>{n.body}</p>
                </div>
              ))}
            </div>
          </section>
        )}
        {catalog.tiers.map((tier) => (
          <section key={tier.name}>
            <h2>{tier.name}</h2>
            <div className="grid">
              {tier.entries.map((e) => (
                <Card entry={e} key={e.name} />
              ))}
            </div>
          </section>
        ))}
        {catalog.commandLine.length > 0 && (
          <section>
            <h2>From a terminal</h2>
            <div className="grid">
              {catalog.commandLine.map((r) => (
                <div className="recipe" key={r.title}>
                  <h3>{r.title}</h3>
                  <p>{r.body}</p>
                  <pre>{r.command}</pre>
                </div>
              ))}
            </div>
          </section>
        )}
        {catalog.guides.length > 0 && (
          <section>
            <h2>Documentation</h2>
            <div className="grid">
              {catalog.guides.map((g) => (
                <div className="note" key={g.title}>
                  <h3>{g.title}</h3>
                  <ul className="links">
                    {g.links.map((l) => (
                      <li key={l.url}>
                        <a href={l.url}>{l.title}</a>
                      </li>
                    ))}
                  </ul>
                </div>
              ))}
            </div>
          </section>
        )}
        {catalog.issuer && (
          <footer>
            <p>
              Sign-in is <a href={catalog.issuer}>{catalog.issuer}</a>.
            </p>
          </footer>
        )}
      </main>
    </>
  );
}
