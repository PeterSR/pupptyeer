# Publishing

pupptyeer ships these public artifacts, all versioned in lockstep with the project release (the
`vX.Y.Z` git tag - see the "client versions move with the release" rule in `CLAUDE.md`):

| Artifact | Registry | Source | Auth (steady state) |
| --- | --- | --- | --- |
| GitHub Release (binaries) | GitHub | goreleaser | `GITHUB_TOKEN` (built in) |
| `pupptyeer-client` | npm | `clients/typescript` | npm OIDC trusted publishing |
| `pupptyeer-client` | PyPI | `clients/python` | PyPI OIDC trusted publishing |
| `pupptyeer` (umbrella alias) | PyPI | `clients/python-umbrella` | PyPI OIDC trusted publishing |
| `@petersr/pupptyeer` (+ 6 platform pkgs) | npm | `npm/` | npm OIDC trusted publishing |

The PyPI `pupptyeer` is a thin alias that just depends on and re-exports `pupptyeer-client`; it
exists to hold the bare name. `pupptyeer-client` is the canonical Python package.

The goal is **tokenless** releases via OIDC trusted publishing, driven by `v*` tags through
`.github/workflows/release.yml`. The publish jobs are gated behind repo variables so the workflow
is safe to land before the registries are set up:

- `PUBLISH_NPM=1` enables the two npm jobs.
- `PUBLISH_PYPI=1` enables the PyPI job.

(Set them under **Settings -> Secrets and variables -> Actions -> Variables**.)

## First release (one-time bootstrap)

### PyPI - tokenless from the start

PyPI supports a *pending* trusted publisher, so the very first publish can be CI-driven with no
token:

1. On PyPI: **Your projects -> Publishing -> Add a pending publisher**, once per project. Add one
   for `pupptyeer-client` and one for `pupptyeer` (the umbrella). Both use owner `PeterSR`, repo
   `pupptyeer`, workflow `release.yml`, environment blank.
2. Set repo variable `PUBLISH_PYPI=1`.
3. Tag and push (`git tag v0.5.0 && git push --tags`). The `publish-pypi-client` and
   `publish-pypi-umbrella` jobs build and upload; each project is created on first use.

### npm - manual first publish, then trusted publishing

npm has no pending-publisher equivalent: trusted publishing must be enabled on a package that
already exists, so each npm package's **first** publish is done by hand from a logged-in machine.

```sh
npm login        # or: npm whoami to confirm you're already logged in

# 1) the TS client
cd clients/typescript
npm publish --access public
cd -

# 2) the binary wrapper - build the per-platform packages first (needs Go), then
#    publish the 6 platform packages before the meta package that depends on them.
node npm/build.mjs 0.5.0          # use the real version
for d in npm/pupptyeer-*/; do ( cd "$d" && npm publish --access public ); done
( cd npm/pupptyeer && npm publish --access public )
```

Then enable OIDC for each package so CI handles every later release:

3. For `pupptyeer-client`, `@petersr/pupptyeer`, and each `@petersr/pupptyeer-<os>-<arch>`: on
   npmjs.com open the package **Settings -> Trusted publishing -> GitHub Actions**, owner `PeterSR`,
   repo `pupptyeer`, workflow `release.yml`.
4. Set repo variable `PUBLISH_NPM=1`.

(Prefer a token over OIDC? Skip steps 3-4, add an `NPM_TOKEN` granular automation secret, and set
`NODE_AUTH_TOKEN: ${{ secrets.NPM_TOKEN }}` on the npm jobs instead. OIDC is recommended: no
long-lived secret, and `--provenance` works out of the box.)

## Every release after that

1. Bump the version in all client surfaces (kept in lockstep):
   `clients/typescript/package.json`, `clients/python/pupptyeer_client.py` (`__version__`),
   `clients/go` (`client.Version`), `clients/python-umbrella/pupptyeer.py` (`__version__`),
   and `npm/pupptyeer/package.json` (its `version` and `optionalDependencies` are also rewritten by
   `npm/build.mjs` at publish). The daemon and `pupptyeer-mcp` are tag-driven via the
   `-X main.version` ldflag and need no manual bump.
2. Commit, then `git tag vX.Y.Z && git push origin main --tags`.
3. The `release` workflow publishes everything. The PyPI job fails fast if `__version__` does not
   equal the tag.

## Local validation (no upload)

```sh
# TS client: inspect the exact tarball contents
cd clients/typescript && npm pack --dry-run

# Python client: build + metadata check
cd clients/python && python -m build && python -m twine check dist/*

# Binary wrapper: build all platform packages, then exercise the launcher.
# Install the host-platform package locally so require.resolve can find it
# (a real `npm i pupptyeer` gets it via optionalDependencies).
node npm/build.mjs 0.5.0
cd npm/pupptyeer && npm i ../pupptyeer-linux-x64 --no-save   # match your host os/arch
node bin/pupptyeer.cjs version                               # resolves + execs the binary
```
