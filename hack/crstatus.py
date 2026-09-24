"""Report how the operator has dealt with the custom resources it owns.

Modes:
  pending  one `Kind/name` per resource the operator has not touched yet
  report   fail, naming every resource the operator REFUSED
  names    one `Kind:name` per resource, for the workload check

The question here is whether the operator ACCEPTED the resource, which is
not the same as whether the workload is up. `failed` is a refusal.
`expanding` is acceptance -- the operator has created the workload and is
waiting for its pods, which in a throwaway cluster means waiting on image
pulls that have nothing to do with whether the chart is correct. Demanding
`operational` would be demanding that a test cluster finish rolling out
several hundred megabytes of images before it agrees the spec was valid.

That distinction is the whole point of 0.3.1: the API server accepted the
object, the operator REFUSED what pruning left, and the only visible
consequence was a Deployment that never appeared. A refusal is loud in
this field; a slow rollout is not a refusal.
"""

# The operator's own vocabulary. Anything not listed is treated as a
# refusal rather than assumed benign: a status this does not recognise is
# a status nobody here has reasoned about.
ACCEPTED = {"operational", "expanding"}
REFUSED = "failed"

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
        # Untouched means no status at all: the operator has not looked at
        # it yet, so there is nothing to judge.
        print(
            " ".join(
                f"{i['kind']}/{i['metadata']['name']}={state(i)}"
                for i in items
                if state(i) == "<none>"
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
        if state(i) not in ACCEPTED
    ]

    if bad:
        print("the operator did not accept:", file=sys.stderr)
        print("\n".join(bad), file=sys.stderr)
        return 1

    states = ", ".join(sorted({state(i) for i in items}))
    print(f"operator accepted {len(items)} custom resources ({states})")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
