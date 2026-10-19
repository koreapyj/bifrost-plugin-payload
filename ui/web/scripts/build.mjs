// Bundles src/main.tsx into dist/app.js (+ dist/app.css) and copies index.html.
// engine.wasm.gz and wasm_exec.js are produced by ui/build.sh.
import { build } from "esbuild";
import { copyFileSync, mkdirSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const root = join(dirname(fileURLToPath(import.meta.url)), "..");
const dist = join(root, "dist");
mkdirSync(dist, { recursive: true });

await build({
  entryPoints: { app: join(root, "src/main.tsx") },
  outdir: dist,
  bundle: true,
  minify: true,
  format: "iife",
  target: ["es2020"],
  jsx: "automatic",
  legalComments: "eof",
  define: { "process.env.NODE_ENV": '"production"' },
  logLevel: "info",
});
copyFileSync(join(root, "src/index.html"), join(dist, "index.html"));
