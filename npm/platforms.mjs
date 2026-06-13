// Single source of the GOOS/GOARCH <-> npm os/cpu matrix for the `@petersr/pupptyeer`
// binary wrapper. Mirrors the build matrix in ../.goreleaser.yaml
// (linux/darwin/windows x amd64/arm64). Consumed by build.mjs; the runtime
// launcher (pupptyeer/lib/resolve.cjs) keeps its own inlined copy because it
// must be plain CommonJS with no build step.
//
// Everything is scoped under @petersr/ because npm rejects the unscoped
// `pupptyeer` name (too similar to `puppeteer`) and trips spam detection on
// unscoped `*-win32-*` names. `pkg` is the published npm name; `dir` is the
// (unscoped) filesystem directory under npm/ that build.mjs generates.

export const SCOPE = "@petersr";
export const META_PKG = "@petersr/pupptyeer";

export const PLATFORMS = [
  { goos: "linux", goarch: "amd64", dir: "pupptyeer-linux-x64", pkg: "@petersr/pupptyeer-linux-x64", os: "linux", cpu: "x64", ext: "" },
  { goos: "linux", goarch: "arm64", dir: "pupptyeer-linux-arm64", pkg: "@petersr/pupptyeer-linux-arm64", os: "linux", cpu: "arm64", ext: "" },
  { goos: "darwin", goarch: "amd64", dir: "pupptyeer-darwin-x64", pkg: "@petersr/pupptyeer-darwin-x64", os: "darwin", cpu: "x64", ext: "" },
  { goos: "darwin", goarch: "arm64", dir: "pupptyeer-darwin-arm64", pkg: "@petersr/pupptyeer-darwin-arm64", os: "darwin", cpu: "arm64", ext: "" },
  { goos: "windows", goarch: "amd64", dir: "pupptyeer-win32-x64", pkg: "@petersr/pupptyeer-win32-x64", os: "win32", cpu: "x64", ext: ".exe" },
  { goos: "windows", goarch: "arm64", dir: "pupptyeer-win32-arm64", pkg: "@petersr/pupptyeer-win32-arm64", os: "win32", cpu: "arm64", ext: ".exe" },
];
