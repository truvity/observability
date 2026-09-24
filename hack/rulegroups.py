"""Print `<alerter>:<group>` for every rule group the operator selected.

The operator writes each vmalert's selected rules into its own configmap,
so that configmap is the only honest answer to "which rules would this
alerter load" — the selector says what should happen, this says what did.

The content arrives base64'd under binaryData and is sometimes gzipped,
which is worth knowing: reading `data` instead returns an empty mapping
and makes a fully-loaded alerter look like it selected nothing.
"""

import base64
import gzip
import json
import sys

import yaml


def main(path: str) -> None:
    with open(path) as handle:
        items = json.load(handle).get("items", [])

    for configmap in items:
        name = configmap["metadata"]["name"]
        if "rulefiles" not in name:
            continue

        which = "logs" if "logs" in name else "metrics"
        raw = b""

        for value in (configmap.get("binaryData") or {}).values():
            raw = base64.b64decode(value)
        if not raw:
            for value in (configmap.get("data") or {}).values():
                raw = value.encode()

        if raw[:2] == b"\x1f\x8b":
            raw = gzip.decompress(raw)

        document = yaml.safe_load(raw.decode() or "") or {}
        for group in document.get("groups") or []:
            print(f"{which}:{group['name']}")


if __name__ == "__main__":
    main(sys.argv[1])
