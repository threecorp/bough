# Plugin signing (v0.6.0)

bough plugins are third-party code: any binary on `PATH` named
`bough-plugin-<kind>` is a subprocess the host spawns with the
operator's file-system + network capabilities. The v0.6 signing
surface is the supply-chain control point operators use to verify
that a plugin came from the source they trust before letting it
run.

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
    require_signed: false              # v0.6 default; v0.7 considers true
    accepted_signature_schemes:        # both supported by default
      - cosign
      - minisign
    untrusted_warning: true            # v0.5 behaviour, unchanged
    allowlist: []                      # bin-name → bypass the signing notice
```

`require_signed: false` keeps v0.6 opt-in. The flag itself is
already accepted by the host config, but the **spawn-time enforce
gate** (= refuse-to-spawn when verification fails) lands in v0.6.x —
v0.6.0 only ships the verify CLI for dry-run, so flipping
`require_signed: true` today is a no-op until you upgrade. Run
`bough plugin verify <binary>` against each plugin in your install
path now; the timeline below tells you when bough starts blocking
unverified spawns.

## Verify CLI

```sh
# cosign verify against the GoReleaser bundle alongside the binary.
bough plugins verify /usr/local/bin/bough-plugin-memory-mem0

# minisign verify with an explicit public key (signer-provided).
bough plugins verify /usr/local/bin/bough-plugin-memory-foo \
    --scheme minisign --pubkey ~/.config/bough/minisign.pub
```

A `✓ cosign verified ...` line means the binary is good. A
non-zero exit means verification failed and the operator should
investigate before enabling enforcement.

## Fail-open today, strict mode tomorrow

v0.6.0 prints a `[NOTICE]` and continues when the verifier binary
is missing — an operator who set `require_signed: true` without
installing `cosign` or `minisign` sees what is missing rather than
a hard refusal. v0.6.x adds a strict mode (`fail_close_on_missing
_verifier: true`) for enterprise deploys that need an unforgeable
gate.

## Timeline (round 4 priority A11)

| Version | Behaviour |
|---|---|
| **v0.6.0** | `require_signed: false`; `bough plugins verify` available; opt-in enforcement when the operator flips the flag and installs cosign / minisign. |
| **v0.6.x** | Stronger warning for unsigned third-party plugins; strict mode for the enterprise enforcement story. |
| **v0.7** | Official bough plugins ship signed; `require_signed: true` recommended for production. Third-party plugins still optional per config. |
| **v0.8+** | Enterprise profile defaults `require_signed: true`; community plugin authors expected to ship a signature alongside every binary. |

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
