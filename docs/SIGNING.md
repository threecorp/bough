# Plugin signing (design, not yet wired up)

bough plugins are third-party code: any binary on `PATH` named
`bough-plugin-<kind>` is a subprocess the host spawns with the
operator's file-system + network capabilities — today that means
the four bundled engine plugins (mysql / postgres / redis /
elasticsearch). Signing is meant to be the supply-chain control
point operators use to verify that a plugin came from the source
they trust before letting it run.

> **Status: designed, not enforced.** `internal/pluginsign` implements
> the cosign / minisign verification calls below and the config
> schema parses, but no command currently calls it — `bough plugins`
> only has a `list` subcommand today (no `verify`, no spawn-time
> enforce gate). Treat everything past this notice as the intended
> design, not current behaviour.

## Schemes (round 4 priority A9)

bough accepts two signature schemes side-by-side:

| Scheme | Best for | Tooling |
|---|---|---|
| **cosign** (Sigstore) | official bough releases (GoReleaser keyless via GitHub Actions OIDC), enterprise CI, multi-tenant registries | `cosign verify-blob --bundle <sig> <binary>` |
| **minisign** (Ed25519) | solo / local plugin authors, air-gapped deploys, pinned-public-key flows | `minisign -V -m <binary> -x <sig> -p <pubkey>` |

Pick either. The reference is `docs/SIGNING.md` (this file); plugin
authors should mention which scheme they ship in their own
`docs/INTEGRATION.md`.

## Configuration

```yaml
instinct:
  plugin_security:
    require_signed: false              # parses; nothing reads it yet
    accepted_signature_schemes:        # both supported by the library
      - cosign
      - minisign
    untrusted_warning: true
    allowlist: []                      # bin-name → bypass the signing notice
```

This schema lives under the (otherwise unrelated, retired) `instinct:`
root section for historical reasons — see the [attic](attic/) for
where that section came from. Setting `require_signed: true` here
has no effect today: no command path calls `internal/pluginsign` to
enforce it against `bough create`'s engine plugin spawns.

The intended design, once wired up: every engine plugin spawn would
run through an enforce gate that:

1. **Skips verification** when the binary name is on
   `plugin_security.allowlist` (= the operator's "I vendored this
   one myself, do not verify" signal).
2. **Tries each scheme** in `accepted_signature_schemes` in order
   (defaults to `[cosign, minisign]`). The first success wins.
3. **Fails open with a stderr NOTICE** when the verifier binary is
   missing on PATH, so flipping the flag without installing cosign /
   minisign does not lock you out of your own host. A
   `fail_close_on_missing_verifier` flag for enterprise deploys that
   need a hard gate is part of the design but unimplemented.
4. **Refuses to spawn** when at least one verifier ran and reported
   a non-verified result. The error mentions which schemes were
   tried and how to recover (= add to allowlist or re-sign).

Cosign keyless verification needs the OIDC identity + issuer the
GoReleaser pipeline signed under (bough's own release identity:
`https://github.com/threecorp/bough/.github/workflows/release.yml@<ref>`,
issuer `https://token.actions.githubusercontent.com`). `internal
/pluginsign.Request` carries `CertIdentity` / `CertOIDCIssuer` /
`CertPath` fields for this — there is no env-var or config-file
loader for them yet since nothing constructs a `Request` at all:
`internal/pluginsign` has no test files either, so this path has
zero coverage today, not just zero callers outside its own tests.

## Current CLI

```sh
bough plugins list
```

lists every `bough-plugin-<kind>` binary bough finds on `PATH`.
There is no `bough plugins verify` subcommand today — verifying a
binary means invoking `cosign verify-blob` / `minisign -V` directly
(see the table above), not through bough.

## Why two schemes

Sigstore (cosign) is the de-facto Go OSS standard in 2025–2026:
GoReleaser's `keyless` integration uses GitHub Actions OIDC, so
official bough releases get a verifiable supply-chain trail without
anyone managing private keys. minisign is small, portable, and
Ed25519-based — perfect for a solo plugin author who just wants
`minisign -S` once and `minisign -V` on every machine that pulls
the binary.

Neither scheme is "the right one" — operators pick the flow that
matches their threat model. The bough host accepts both so plugin
authors do not have to agree.

## See also

- [SECURITY.md](SECURITY.md) — the broader third-party plugin trust
  model (= why "bin on PATH" is not enough on its own).
- [GoReleaser sign docs](https://goreleaser.com/customization/sign/) —
  the official bough release pipeline lives here.
- [Sigstore](https://www.sigstore.dev/) — cosign / Fulcio / Rekor
  story.
- [minisign](https://github.com/jedisct1/minisign) — the Ed25519
  signer.
