# Doctrine

The design and ownership rules for this repository are documented in the
shared [component contract](https://github.com/truvity/policy/blob/master/docs/contracts/component.md).

## Guard rails enforced in CI

- **No tracker keys.** `hack/leak-canary.sh` fails on any token shaped like
  a ticket key (two to six capitals, a dash, digits) anywhere in the tree,
  CHANGELOG.md included. Write the prose so it stands without the key.
  Public vocabulary of that shape (vulnerability ids, hash and licence
  names) is on the script's commented allow-list.
- **A changed golden is a declared default change.** Modifying (not adding)
  a file under `tests/golden/` changes what an existing consumer renders.
  The change must add a line containing `**Behaviour change` to
  CHANGELOG.md, naming what moved and the opt-out. CI enforces it on pull
  requests; locally, `just default-change-guard` compares against
  `origin/master` (or pass another base ref).
