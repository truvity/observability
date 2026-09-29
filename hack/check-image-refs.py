"""Refuse a chart `image.repository` default the release does not build.

.goreleaser.yaml's `kos:` list, nested under the `ko-docker-repo`
.github/workflows/release.yaml passes it (ko names each image after its
build's own main directory basename), is the whole truth about what the
release job actually builds and pushes. A chart's `image.repository`
default naming anything else would render, install, and pull nothing —
exactly the trap charts/alert-ingress/values.yaml's own comment used to
warn about when this repository shipped no image at all. This script is
the one place the three files (.goreleaser.yaml, release.yaml, every
chart's values.yaml) are compared, so nobody has to keep them in sync by
hand, and a future chart or build can drift silently otherwise.

Only a chart whose `image.repository` default looks like this
repository's own release (ghcr.io/truvity/observability/...) is checked.
Most charts here wrap upstream software with no such default at all (see
docs/reference.md) — a different, legitimate shape this script leaves
alone.
"""

import pathlib
import sys

import yaml

ROOT = pathlib.Path(__file__).resolve().parent.parent
OWN_IMAGE_PREFIX = "ghcr.io/truvity/observability/"


def expected_images() -> tuple[str, set[str]]:
    goreleaser = yaml.safe_load((ROOT / ".goreleaser.yaml").read_text())
    release_wf = yaml.safe_load((ROOT / ".github/workflows/release.yaml").read_text())

    ko_docker_repo = (
        release_wf.get("jobs", {})
        .get("release", {})
        .get("with", {})
        .get("ko-docker-repo", "ghcr.io/truvity")
    )

    builds = {b["id"]: b for b in goreleaser.get("builds", []) if b.get("id")}
    images = set()
    for ko in goreleaser.get("kos", []):
        build = builds.get(ko.get("build"))
        if build is None:
            sys.exit(f"::error::.goreleaser.yaml kos entry names an unknown build {ko.get('build')!r}")
        basename = build["main"].rstrip("/").rsplit("/", 1)[-1]
        images.add(f"{ko_docker_repo}/{basename}")
    return ko_docker_repo, images


def main() -> int:
    ko_docker_repo, expected = expected_images()
    failed = False

    for chart_dir in sorted((ROOT / "charts").iterdir()):
        values_path = chart_dir / "values.yaml"
        if not chart_dir.is_dir() or not values_path.exists():
            continue
        values = yaml.safe_load(values_path.read_text()) or {}
        image = values.get("image")
        repository = image.get("repository") if isinstance(image, dict) else None
        if not repository or not repository.startswith(OWN_IMAGE_PREFIX):
            continue
        if repository not in expected:
            print(
                f"::error::charts/{chart_dir.name}/values.yaml sets image.repository={repository!r}, "
                f"which .goreleaser.yaml's kos (as ko-docker-repo {ko_docker_repo!r}) "
                f"does not build: {sorted(expected) or '(no images built)'}",
                file=sys.stderr,
            )
            failed = True

    if failed:
        return 1
    print(f"image references OK: {sorted(expected)}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
