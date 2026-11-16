// Copies the diagram engines into public/vendor/, to be served as they are.
//
// Mermaid and Graphviz (WASM) are large: bundled, they multiply the build's time
// and memory several times over (Mermaid alone is over 200 prebuilt chunks, one
// group per diagram type). The file viewer loads them on demand, only when a
// diagram is opened, so they are served as static files instead of being part
// of the build. Run by `npm run build` and `npm run dev`.

import { existsSync, mkdirSync, rmSync } from "node:fs";
import { cp } from "node:fs/promises";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const root = join(dirname(fileURLToPath(import.meta.url)), "..");
const target = join(root, "public", "vendor");

const copies = [
  ["node_modules/mermaid/dist/mermaid.esm.min.mjs", "mermaid/mermaid.esm.min.mjs"],
  ["node_modules/mermaid/dist/chunks/mermaid.esm.min", "mermaid/chunks/mermaid.esm.min"],
  ["node_modules/@viz-js/viz/dist/viz.js", "viz/viz.js"]
];

rmSync(target, { force: true, recursive: true });
for (const [from, to] of copies) {
  const source = join(root, from);
  if (!existsSync(source)) {
    console.error(`copy-vendor: ${from} is missing; run npm ci`);
    process.exit(1);
  }
  const destination = join(target, to);
  mkdirSync(dirname(destination), { recursive: true });
  // Use fs/promises cp instead of cpSync: in Node 22 on Docker Desktop VirtioFS,
  // libuv's synchronous recursive directory copy leaves destination files with
  // mode 0200 (--w-------), causing Nitro's asset copy chmod to fail with EACCES.
  await cp(source, destination, { recursive: true });
}
console.log("copy-vendor: diagram engines copied to public/vendor");
