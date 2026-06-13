// Build the per-platform npm packages for the `pupptyeer` binary wrapper.
//
//   node npm/build.mjs <version>
//
// For each platform in platforms.mjs this cross-compiles the daemon/CLI
// (./cmd/pupptyeer, root module) and the MCP front-end (the ./mcp module) with
// the same flags as .goreleaser.yaml, drops both binaries into
// npm/<pkg>/bin/, and writes npm/<pkg>/package.json pinned to <version>. It
// also rewrites npm/pupptyeer/package.json so its own version and every
// optionalDependencies entry equal <version>.
//
// The generated npm/pupptyeer-*/ dirs are release artifacts (gitignored); CI
// runs this on a tag, then `npm publish`es each platform package followed by
// the meta package. Run it locally to smoke-test the wrapper.

import { execFileSync } from "node:child_process";
import { mkdirSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { PLATFORMS } from "./platforms.mjs";

const here = dirname(fileURLToPath(import.meta.url));
const repoRoot = resolve(here, "..");

const version = process.argv[2];
if (!version || /^v/.test(version)) {
  console.error("usage: node npm/build.mjs <version>   (semver, no leading 'v')");
  process.exit(1);
}

const ldflags = `-s -w -X main.version=${version}`;

function goBuild({ goos, goarch, dir, mainPath, outFile }) {
  mkdirSync(dirname(outFile), { recursive: true });
  const args = ["build", "-trimpath", "-ldflags", ldflags, "-o", outFile, mainPath];
  // The mcp module builds with `-C mcp`; the root module from repoRoot.
  if (dir) args.unshift("-C", dir);
  execFileSync("go", args, {
    cwd: repoRoot,
    stdio: "inherit",
    env: { ...process.env, GOOS: goos, GOARCH: goarch, CGO_ENABLED: "0" },
  });
}

function writeJson(file, obj) {
  writeFileSync(file, JSON.stringify(obj, null, 2) + "\n");
}

for (const p of PLATFORMS) {
  const pkgDir = join(here, p.pkg);
  const binDir = join(pkgDir, "bin");
  console.log(`==> ${p.pkg} (${p.goos}/${p.goarch})`);
  rmSync(pkgDir, { recursive: true, force: true });
  mkdirSync(binDir, { recursive: true });

  // ./cmd/pupptyeer from the root module.
  goBuild({
    goos: p.goos,
    goarch: p.goarch,
    mainPath: "./cmd/pupptyeer",
    outFile: join(binDir, `pupptyeer${p.ext}`),
  });
  // The pupptyeer-mcp binary lives in the separate ./mcp module.
  goBuild({
    goos: p.goos,
    goarch: p.goarch,
    dir: "mcp",
    mainPath: ".",
    // -C mcp changes the working dir, so the -o path must be absolute.
    outFile: join(binDir, `pupptyeer-mcp${p.ext}`),
  });

  writeJson(join(pkgDir, "package.json"), {
    name: p.pkg,
    version,
    description: `Prebuilt pupptyeer binaries for ${p.os} ${p.cpu}. Installed automatically by the "pupptyeer" package.`,
    repository: {
      type: "git",
      url: "git+https://github.com/PeterSR/pupptyeer.git",
    },
    homepage: "https://github.com/PeterSR/pupptyeer#readme",
    license: "MIT",
    author: "PeterSR",
    os: [p.os],
    cpu: [p.cpu],
    files: ["bin"],
    publishConfig: { access: "public" },
  });
}

// Sync the meta package: its own version and all optionalDependencies pins.
const metaFile = join(here, "pupptyeer", "package.json");
const meta = JSON.parse(readFileSync(metaFile, "utf8"));
meta.version = version;
meta.optionalDependencies = Object.fromEntries(PLATFORMS.map((p) => [p.pkg, version]));
writeJson(metaFile, meta);

console.log(`\nBuilt ${PLATFORMS.length} platform packages + meta at version ${version}.`);
