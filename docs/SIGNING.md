# Signatures

## Verifying a release

Every release archive is signed with [cosign](https://www.sigstore.dev/)
keyless signing from the release workflow. Each
`bough_<version>_<os>_<arch>.tar.gz` asset has a `.sig` and a `.pem`
next to it. Verify the archive before you extract it:

```bash
v=0.28.0; a=bough_${v}_darwin_arm64.tar.gz
gh release download v$v --repo threecorp/bough -p "$a" -p "$a.sig" -p "$a.pem"
cosign verify-blob "$a" \
  --signature "$a.sig" --certificate "$a.pem" \
  --certificate-identity-regexp '^https://github.com/threecorp/bough/\.github/workflows/release\.yml@refs/tags/v' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com
# Verified OK
```

`checksums.txt` in the same release carries the SHA-256 of every archive.

## Plugin binaries are not verified

bough spawns whatever `bough-plugin-<kind>` it finds on `PATH` and does
not check a signature first. `internal/pluginsign` has cosign and
minisign verification helpers, but no command calls them and there is
no configuration for them. Until that is wired, verify the release
archive as above and keep `PATH` to binaries you trust — see
[SECURITY.md](SECURITY.md).
