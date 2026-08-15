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

# A shipped binary that cannot LAUNCH its engine plugins is useless, and
# nothing above notices: every check so far runs the host binary alone.
# The plugins ship in the same archive and are reached only by exec, so an
# archive missing one, an unrunnable one (wrong arch, stripped signature,
# an in-place overwrite that invalidated the kernel's cached signature),
# or one whose handshake has drifted all look identical to a green run.
# This exercises the thing an operator's first `--worktree` does.
echo "== engine plugins: present, and they actually run =="
plugin_dir="$(cd "$(dirname "$BOUGH")" && pwd -P)"
missing=""
for kind in mysql postgres redis elasticsearch compose; do
  bin="$plugin_dir/bough-plugin-$kind"
  [ -x "$bin" ] || { missing="$missing $kind"; continue; }
  # A plugin refuses to be run directly and exits non-zero saying so. That
  # refusal IS the pass: it proves the process started. What must never
  # happen is a signal — 137 (SIGKILL) is how macOS reports a binary whose
  # code signature no longer matches its bytes, and it is silent otherwise.
  # errexit off across the call: a plugin's refusal to run directly is a
  # NON-ZERO exit, which is exactly the outcome being measured. Letting
  # set -e act on it would abort the smoke on the healthy path.
  set +e
  out="$("$bin" </dev/null 2>&1)"
  rc=$?
  set -e
  case "$rc" in
    0|1) ;;
    *)   fail "bough-plugin-$kind did not start (exit $rc): ${out:-<no output>}" ;;
  esac
  printf '%s' "$out" | grep -qi 'plugin' \
    || fail "bough-plugin-$kind started but did not identify itself: ${out:-<no output>}"
done
[ -z "$missing" ] || fail "archive is missing engine plugin(s):$missing"
ok "5 engine plugins present and startable"

# The host discovers plugins over the go-plugin handshake, which is a
# different code path from exec'ing them: a version-skewed protocol starts
# fine and fails only here. This is the call `bough create` makes first.
echo "== plugin discovery over the handshake =="
set +e
discovered="$(cd "$MONO" && PATH="$plugin_dir:$PATH" "$BOUGH" plugins list 2>&1)"
drc=$?
set -e
[ "$drc" -eq 0 ] || fail "bough plugins list exited $drc: $discovered"
for kind in mysql postgres redis elasticsearch compose; do
  printf '%s' "$discovered" | grep -q "^$kind\b" \
    || fail "handshake did not discover $kind:
$discovered"
done
ok "5 engine plugins discovered over the handshake"

echo "SMOKE PASS"
