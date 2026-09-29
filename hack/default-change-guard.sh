#!/usr/bin/env bash
# hack/default-change-guard.sh [BASE_REF]
#
# The rule: a change to an EXISTING golden under tests/golden/ is a change
# to a default render, and a default render is what every consumer already
# running the chart gets on upgrade. Such a change must be declared in
# CHANGELOG.md with a `**Behaviour change` line, added in the same change.
# Adding a NEW golden (a new case) changes no existing render and is fine.
#
# BASE_REF defaults to origin/master. The comparison is against the
# merge-base with HEAD, so a branch that master has moved past is judged by
# its own changes only. CI passes the pull request's base branch.
set -euo pipefail

base="${1:-origin/master}"
if ! git rev-parse --verify -q "$base^{commit}" >/dev/null; then
  echo "default-change-guard: base ref '$base' not found — fetch it first" >&2
  exit 2
fi
mb=$(git merge-base "$base" HEAD)

# --diff-filter=M: modified only. Deletions and renames of a golden are
# also render changes, so they are counted with it (D, R); only A is free.
mapfile -t changed < <(git diff --name-only --diff-filter=MDR "$mb" HEAD -- tests/golden/)

if [ ${#changed[@]} -eq 0 ]; then
  echo "default-change-guard: no existing golden changed — nothing to declare"
  exit 0
fi

if git diff -U0 "$mb" HEAD -- CHANGELOG.md | grep -qE '^\+[-*] +\*\*Behaviour change'; then
  echo "default-change-guard: ${#changed[@]} existing golden(s) changed and CHANGELOG.md declares a **Behaviour change"
  exit 0
fi

echo "default-change-guard: existing golden(s) changed, but this change adds no '**Behaviour change' line to CHANGELOG.md:" >&2
printf '    %s\n' "${changed[@]:0:10}" >&2
[ ${#changed[@]} -gt 10 ] && echo "    … and $((${#changed[@]} - 10)) more" >&2
cat >&2 <<'MSG'

A change to an existing golden is a change to a default render. Declare it
in the newest CHANGELOG.md entry as a bullet containing `**Behaviour change`
(what moved, and the opt-out that restores the previous output). If the
golden should not have moved, fix the change instead.
MSG
exit 1
