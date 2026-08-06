#!/usr/bin/env bash
# Drive bough's user-facing entry point — the WorktreeCreate /
# WorktreeRemove hook contract a Claude Code host invokes — against a real
# binary, and assert the host's own acceptance criteria.
#
# This exists because unit tests answer "does the code do what it says" and
# nothing answered "does the shipped binary still work where it is used".
# A release once left `claude --worktree` failing at its final step while
# every test was green: everything bough provisions had succeeded, and the
# host refused the path bough handed back. That is checked here.
#
# Usage:
#   scripts/entrypoint-smoke.sh <path-to-bough-binary>
#
# Optional:
#   BOUGH_EXPECT_VERSION=v1.2.3   also assert `bough --version` reports it
#
# Exit 0 = the entry point works. Any assertion failure exits non-zero with
# the measured value printed, so CI shows what was wrong, not just that
# something was.
set -euo pipefail

BOUGH="${1:?usage: entrypoint-smoke.sh <path-to-bough-binary>}"
[ -x "$BOUGH" ] || { echo "not executable: $BOUGH" >&2; exit 1; }

WORK="$(mktemp -d)"
trap 'rm -rf "${WORK:?}"' EXIT
MONO="$WORK/mono"

fail() { echo "SMOKE FAIL: $*" >&2; exit 1; }
ok()   { echo "  ok — $*"; }

echo "== binary =="
echo "  path   : $BOUGH"
echo "  version: $("$BOUGH" --version)"
if [ -n "${BOUGH_EXPECT_VERSION:-}" ]; then
  want="${BOUGH_EXPECT_VERSION#v}"
  got="$("$BOUGH" --version | tr -d '\n')"
  case "$got" in
    *"$want"*) ok "version reports $want" ;;
    *) fail "version mismatch: binary says '$got', release is '$want'" ;;
  esac
fi

echo "== fixture: a git monorepo whose sub-repos are separate checkouts =="
mkdir -p "$MONO"
git -C "$MONO" init -q -b main
git -C "$MONO" -c user.email=ci@example.com -c user.name=ci commit -q --allow-empty -m init
for r in alpha beta; do
  mkdir -p "$MONO/$r"
  git -C "$MONO/$r" init -q -b main
  git -C "$MONO/$r" -c user.email=ci@example.com -c user.name=ci commit -q --allow-empty -m init
done
cat > "$MONO/.bough.yaml" <<'YAML'
schema_version: 2
monorepo_root: "."
repositories:
  - name: alpha
    branch_strategy: main
  - name: beta
    branch_strategy: main
registry:
  path: ".bough-ports.json"
YAML
ok "monorepo at $MONO"

echo "== WorktreeCreate: the exact contract the host invokes =="
# The host pipes {name, cwd} on stdin and cd's into the LAST line of stdout.
OUT="$(printf '{"name":"F-Smoke","cwd":%s}' "\"$MONO\"" \
  | (cd "$MONO" && "$BOUGH" hook handle --event WorktreeCreate) )" \
  || fail "hook handle --event WorktreeCreate exited non-zero"
WT="$(printf '%s\n' "$OUT" | tail -1)"
[ -n "$WT" ] || fail "hook printed no worktree path"
[ -d "$WT" ] || fail "hook printed '$WT' but no such directory exists"
ok "hook emitted $WT"

echo "== the host's acceptance predicate =="
# A host refuses an isolation worktree whose working tree git resolves
# somewhere else — commands run there would write outside the worktree.
# Resolution is the whole test: asserting anything beyond it (e.g. that the
# git dir is not the container's own) rejects shapes the host accepts.
TOP="$(git -C "$WT" rev-parse --show-toplevel 2>/dev/null || true)"
[ -n "$TOP" ] || fail "$WT is not inside any git work tree"
# Compare resolved paths: macOS /tmp is a symlink to /private/tmp, and the
# two spellings name the same directory.
[ "$(cd "$TOP" && pwd -P)" = "$(cd "$WT" && pwd -P)" ] \
  || fail "git resolves $WT to $TOP — a host refuses this (work-tree-elsewhere)"
ok "resolves to itself, git dir $(git -C "$WT" rev-parse --absolute-git-dir)"

echo "== the environment it was supposed to provision =="
for r in alpha beta; do
  [ -e "$WT/$r/.git" ] || fail "sub-repo worktree missing: $WT/$r"
done
ok "sub-repo worktrees materialised"

echo "== WorktreeRemove: tears down and leaves no record behind =="
printf '{"worktree_path":%s}' "\"$WT\"" \
  | (cd "$MONO" && "$BOUGH" hook handle --event WorktreeRemove) >/dev/null \
  || fail "hook handle --event WorktreeRemove exited non-zero"
[ ! -d "$WT" ] || fail "worktree still on disk after WorktreeRemove: $WT"
if git -C "$MONO" worktree list --porcelain | grep -qF "$WT"; then
  fail "the monorepo still records the removed container in git worktree list"
fi
ok "removed, no stale worktree record"

echo "SMOKE PASS"
