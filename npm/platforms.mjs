// Single source of the GOOS/GOARCH <-> npm os/cpu matrix for the `pupptyeer`
// binary wrapper. Mirrors the build matrix in ../.goreleaser.yaml
// (linux/darwin/windows x amd64/arm64). Consumed by build.mjs; the runtime
// launcher (pupptyeer/lib/resolve.cjs) keeps its own inlined copy because it
// must be plain CommonJS with no build step.

export const META_PKG = "pupptyeer";

export const PLATFORMS = [
  { goos: "linux", goarch: "amd64", pkg: "pupptyeer-linux-x64", os: "linux", cpu: "x64", ext: "" },
  { goos: "linux", goarch: "arm64", pkg: "pupptyeer-linux-arm64", os: "linux", cpu: "arm64", ext: "" },
  { goos: "darwin", goarch: "amd64", pkg: "pupptyeer-darwin-x64", os: "darwin", cpu: "x64", ext: "" },
  { goos: "darwin", goarch: "arm64", pkg: "pupptyeer-darwin-arm64", os: "darwin", cpu: "arm64", ext: "" },
  { goos: "windows", goarch: "amd64", pkg: "pupptyeer-win32-x64", os: "win32", cpu: "x64", ext: ".exe" },
  { goos: "windows", goarch: "arm64", pkg: "pupptyeer-win32-arm64", os: "win32", cpu: "arm64", ext: ".exe" },
];
