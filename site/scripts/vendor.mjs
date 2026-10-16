#!/usr/bin/env node
// Vendors the third-party browser libraries the documentation pages need into
// site/vendor/, so the site stays a self-contained static artifact with no
// build step and no runtime CDN dependency.
//
// Usage:
//   node site/scripts/vendor.mjs            # install the pinned versions, then copy
//   node site/scripts/vendor.mjs --check    # verify site/vendor/ matches the pins
//
// The pinned versions below are the contract: bump them here, re-run the
// script, and commit the refreshed site/vendor/ tree together with the change.

import { execFileSync } from "node:child_process";
import { cp, mkdir, readFile, readdir, rm, stat, writeFile } from "node:fs/promises";
import { createRequire } from "node:module";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const HERE = dirname(fileURLToPath(import.meta.url));
const SITE = resolve(HERE, "..");
const VENDOR = join(SITE, "vendor");

/** Pinned dependency set. Keep in sync with site/vendor/README.md. */
const PINS = {
  marked: "18.1.1",
  mermaid: "12.2.0",
  "highlight.js": "11.12.0",
  esbuild: "0.28.2",
};

/** Files copied out of node_modules, keyed as [source, destination]. */
const COPIES = [
  ["marked/LICENSE", "marked/LICENSE"],
  ["marked/lib/marked.umd.js", "marked/marked.umd.js"],
  ["mermaid/LICENSE", "mermaid/LICENSE"],
  ["mermaid/dist/mermaid.esm.min.mjs", "mermaid/mermaid.esm.min.mjs"],
  ["highlight.js/LICENSE", "highlight/LICENSE"],
];

const check = process.argv.includes("--check");

/** Resolves the scratch node_modules used as the copy source. */
async function sourceModules() {
  const scratch = join(VENDOR, ".scratch");
  await rm(scratch, { recursive: true, force: true });
  await mkdir(scratch, { recursive: true });
  await writeFile(join(scratch, "package.json"), `${JSON.stringify({ name: "go-taas-vendor", private: true }, null, 2)}\n`);
  const pinned = Object.entries(PINS).map(([name, version]) => `${name}@${version}`);
  process.stdout.write(`vendoring: installing ${pinned.join(" ")}\n`);
  execFileSync("npm", ["install", "--no-audit", "--no-fund", "--silent", ...pinned], {
    cwd: scratch,
    stdio: ["ignore", "inherit", "inherit"],
  });
  return join(scratch, "node_modules");
}

/** Returns the installed version of every pinned package. */
async function installedVersions(modules) {
  const require = createRequire(join(modules, "index.js"));
  const found = {};
  for (const name of Object.keys(PINS)) {
    found[name] = require(join(modules, name, "package.json")).version;
  }
  return found;
}

/** Copies one node_modules file into site/vendor/, preserving the tree. */
async function copyFile(modules, from, to) {
  const dst = join(VENDOR, to);
  await mkdir(dirname(dst), { recursive: true });
  await cp(join(modules, from), dst);
  return dst;
}

/** Bundles highlight.js' common language set into a single browser script. */
async function bundleHighlight(modules) {
  const out = join(VENDOR, "highlight", "highlight.min.js");
  await mkdir(dirname(out), { recursive: true });
  execFileSync(
    join(modules, ".bin", "esbuild"),
    [
      join(modules, "highlight.js", "lib", "common.js"),
      "--bundle",
      "--minify",
      "--format=iife",
      "--global-name=hljs",
      `--outfile=${out}`,
      "--log-level=error",
    ],
    { stdio: ["ignore", "inherit", "inherit"] },
  );
  return out;
}

/** Copies the mermaid ESM entry plus every chunk it may dynamically import. */
async function copyMermaidChunks(modules) {
  const to = join(VENDOR, "mermaid", "chunks", "mermaid.esm.min");
  await rm(to, { recursive: true, force: true });
  await mkdir(dirname(to), { recursive: true });
  // Copying the whole minified chunk directory (source maps excluded) keeps the
  // lazy-loaded diagram bundles self-contained without guessing which dynamic
  // import each diagram type resolves to.
  await cp(join(modules, "mermaid", "dist", "chunks", "mermaid.esm.min"), to, {
    recursive: true,
    filter: (src) => !src.endsWith(".map"),
  });
  return to;
}

/** Counts the vendored files and their total size, excluding scratch state. */
async function inventory(dir) {
  let files = 0;
  let bytes = 0;
  async function walk(current) {
    for (const entry of await readdir(current, { withFileTypes: true })) {
      if (entry.name === ".scratch") continue;
      const path = join(current, entry.name);
      if (entry.isDirectory()) {
        await walk(path);
      } else {
        files += 1;
        bytes += (await stat(path)).size;
      }
    }
  }
  await walk(dir);
  return { files, bytes };
}

async function main() {
  if (check) {
    const manifest = JSON.parse(await readFile(join(VENDOR, "versions.json"), "utf8"));
    const mismatched = Object.entries(PINS).filter(([name, version]) => manifest[name] !== version);
    if (mismatched.length > 0) {
      process.stderr.write(
        `site/vendor/versions.json does not match the pins in site/scripts/vendor.mjs:\n` +
          mismatched.map(([n, v]) => `  ${n}: pinned ${v}, vendored ${manifest[n] ?? "missing"}\n`).join("") +
          "Run: node site/scripts/vendor.mjs\n",
      );
      process.exit(1);
    }
    process.stdout.write("site/vendor/ matches the pinned versions\n");
    return;
  }

  const modules = await sourceModules();
  const versions = await installedVersions(modules);

  for (const [from, to] of COPIES) {
    await copyFile(modules, from, to);
  }
  await copyMermaidChunks(modules);
  await bundleHighlight(modules);

  await writeFile(
    join(VENDOR, "versions.json"),
    `${JSON.stringify({ ...versions, generatedBy: "site/scripts/vendor.mjs" }, null, 2)}\n`,
  );

  const { files, bytes } = await inventory(VENDOR);
  process.stdout.write(`vendored ${files} files (${(bytes / 1024 / 1024).toFixed(1)} MiB) into site/vendor/\n`);
}

await main();
