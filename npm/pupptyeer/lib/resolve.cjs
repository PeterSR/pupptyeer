"use strict";

// Map this Node process's platform to the per-platform npm package that ships
// its prebuilt binaries. Keep this table in step with ../../platforms.mjs (this
// copy stays plain CommonJS so the wrapper needs no build step).
const SUPPORTED = {
  "linux-x64": "pupptyeer-linux-x64",
  "linux-arm64": "pupptyeer-linux-arm64",
  "darwin-x64": "pupptyeer-darwin-x64",
  "darwin-arm64": "pupptyeer-darwin-arm64",
  "win32-x64": "pupptyeer-win32-x64",
  "win32-arm64": "pupptyeer-win32-arm64",
};

function platformKey() {
  return `${process.platform}-${process.arch}`;
}

// Resolve the absolute path to a bundled binary ("pupptyeer" or
// "pupptyeer-mcp") inside the matching platform package. Throws with an
// actionable message if the platform is unsupported or its package is absent.
function resolveBinary(name) {
  const key = platformKey();
  const pkg = SUPPORTED[key];
  if (!pkg) {
    throw new Error(
      `pupptyeer: unsupported platform "${key}". Supported: ${Object.keys(SUPPORTED).join(", ")}.\n` +
        `Build from source instead: https://github.com/PeterSR/pupptyeer`
    );
  }
  const ext = process.platform === "win32" ? ".exe" : "";
  try {
    return require.resolve(`${pkg}/bin/${name}${ext}`);
  } catch (err) {
    throw new Error(
      `pupptyeer: the platform package "${pkg}" is not installed.\n` +
        `It should install automatically as an optional dependency of "pupptyeer".\n` +
        `If you used --no-optional or --omit=optional, reinstall without it, or run:\n` +
        `  npm i ${pkg}\n` +
        `Underlying error: ${err && err.message ? err.message : err}`
    );
  }
}

module.exports = { resolveBinary, platformKey, SUPPORTED };
