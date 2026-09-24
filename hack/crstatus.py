"""Report how the operator has dealt with the custom resources it owns.

Modes:
  pending  one `Kind/name=status` per resource NOT yet operational
  report   fail, naming every resource the operator did not accept
  names    one `Kind:name` per resource, for the workload check

A resource the operator refuses reports `failed` here and creates nothing,
which is the shape of the 0.3.1 incident: the API server accepted the
object, the operator rejected what pruning left, and the only visible
consequence was a Deployment that never appeared.
"""

import json
import sys


def resources(path: str) -> list:
    with open(path) as handle:
        return json.load(handle).get("items", [])


def state(item: dict) -> str:
    return (item.get("status") or {}).get("updateStatus") or "<none>"


def main() -> int:
    path, mode = sys.argv[1], sys.argv[2]
    items = resources(path)

    if mode == "pending":
        print(
            " ".join(
                f"{i['kind']}/{i['metadata']['name']}={state(i)}"
                for i in items
                if state(i) != "operational"
            )
        )
        return 0

    if mode == "names":
        for item in items:
            print(f"{item['kind']}:{item['metadata']['name']}")
        return 0

    if not items:
        print(
            "no custom resource was installed at all — the chart or the values moved",
            file=sys.stderr,
        )
        return 1

    bad = [
        f"  {i['kind']}/{i['metadata']['name']}: {state(i)} — "
        f"{(i.get('status') or {}).get('reason', 'no reason given')}"
        for i in items
        if state(i) != "operational"
    ]

    if bad:
        print("the operator did not accept:", file=sys.stderr)
        print("\n".join(bad), file=sys.stderr)
        return 1

    print(f"operator accepted {len(items)} custom resources")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
