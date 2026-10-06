#!/usr/bin/env bash
# Golden renders: every tests/cases/<chart>/<case>/values.yaml is rendered
# with `helm template` and compared byte-for-byte against
# tests/golden/<chart>/<case>.yaml. A template change that alters output
# therefore shows up as a reviewable diff, with no cluster involved.
#
#   hack/golden.sh          compare (CI)
#   hack/golden.sh update   regenerate the golden files
set -euo pipefail

mode="${1:-check}"
root="$(cd "$(dirname "$0")/.." && pwd)"
fail=0

for values in "$root"/tests/cases/*/*/values.yaml; do
  case_dir="$(dirname "$values")"
  case_name="$(basename "$case_dir")"
  chart="$(basename "$(dirname "$case_dir")")"
  golden="$root/tests/golden/$chart/$case_name.yaml"

  # A case may list chart presets (charts/<chart>/presets/<name>.yaml), one
  # name per line in `presets`: they are values files listed BEFORE the
  # case's own values, the way a consumer lists them, so the case's values win.
  preset_args=()
  if [ -f "$case_dir/presets" ]; then
    while IFS= read -r preset; do
      [ -n "$preset" ] || continue
      preset_args+=(-f "$root/charts/$chart/presets/$preset.yaml")
    done < "$case_dir/presets"
  fi

  rendered="$(helm template "$chart" "$root/charts/$chart" \
      --namespace "$(cat "$case_dir/namespace" 2>/dev/null || echo default)" \
      ${preset_args[@]+"${preset_args[@]}"} -f "$values")"

  if [ "$mode" = update ]; then
    mkdir -p "$(dirname "$golden")"
    printf '%s\n' "$rendered" > "$golden"
    echo "updated $golden"
    continue
  fi

  if ! diff -u "$golden" <(printf '%s\n' "$rendered"); then
    echo "GOLDEN MISMATCH: $chart/$case_name — run 'just golden' and review the diff"
    fail=1
  fi
done

[ "$fail" = 0 ] && echo "golden renders match"
exit $fail
