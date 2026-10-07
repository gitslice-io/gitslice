// Release downloads are addressed through gitslice.io, so install.sh, gs
// upgrade and the docs never name the store that holds the bytes.
//
// Releases are built off GitHub (ops/release) and stored in R2 under
// releases/<tag>/, with releases/latest.json naming the newest; the worker
// serves them from its RELEASES binding (releaseResponse). Releases made before
// that live on the GitHub mirror's releases, so anything not in R2 is
// redirected there. Redirects are temporary (302) so the store can change.
export const releasesBase = "https://github.com/gitslice-io/gitslice/releases";

// Tags and asset names: letters, digits, '.', '_', '+' and '-', starting with
// a letter or digit. This keeps redirects inside releasesBase (no '/' or '..').
const segment = /^[A-Za-z0-9][A-Za-z0-9._+-]*$/;

export function releaseRedirectTarget(request: Request): string | null {
  if (request.method !== "GET" && request.method !== "HEAD") {
    return null;
  }
  const { pathname } = new URL(request.url);
  if (pathname !== "/releases" && !pathname.startsWith("/releases/")) {
    return null;
  }
  const parts = pathname.split("/").filter(Boolean).slice(1);
  if (parts.length === 0) {
    return releasesBase;
  }
  if (parts.length === 1 && parts[0] === "latest") {
    return `${releasesBase}/latest`;
  }
  if (parts.length === 3 && parts[0] === "latest" && parts[1] === "download" && segment.test(parts[2])) {
    return `${releasesBase}/latest/download/${parts[2]}`;
  }
  if (parts.length === 2 && parts[0] === "tag" && segment.test(parts[1])) {
    return `${releasesBase}/tag/${parts[1]}`;
  }
  if (parts.length === 3 && parts[0] === "download" && segment.test(parts[1]) && segment.test(parts[2])) {
    return `${releasesBase}/download/${parts[1]}/${parts[2]}`;
  }
  return null;
}

export function releaseRedirect(request: Request): Response | null {
  const target = releaseRedirectTarget(request);
  if (!target) {
    return null;
  }
  return new Response(null, {
    status: 302,
    headers: { Location: target, "Cache-Control": "public, max-age=60" }
  });
}

// The part of an R2 bucket binding that serving releases uses.
export interface ReleaseObject {
  body: ReadableStream | null;
  size: number;
  text(): Promise<string>;
}

export interface ReleaseBucket {
  get(key: string): Promise<ReleaseObject | null>;
}

const textTypes: Record<string, string> = {
  ".tar.gz": "application/gzip",
  ".zip": "application/zip",
  ".txt": "text/plain; charset=utf-8",
  ".json": "application/json"
};

function contentType(asset: string) {
  const match = Object.keys(textTypes).find((suffix) => asset.endsWith(suffix));
  return match ? textTypes[match] : "application/octet-stream";
}

async function latestTag(bucket: ReleaseBucket): Promise<string | null> {
  const latest = await bucket.get("releases/latest.json");
  if (!latest) {
    return null;
  }
  try {
    const tag = (JSON.parse(await latest.text()) as { tag?: unknown }).tag;
    return typeof tag === "string" && segment.test(tag) ? tag : null;
  } catch {
    return null;
  }
}

function redirectTo(location: string, maxAge = 60) {
  return new Response(null, {
    status: 302,
    headers: { Location: location, "Cache-Control": `public, max-age=${maxAge}` }
  });
}

async function assetResponse(request: Request, bucket: ReleaseBucket, tag: string, asset: string) {
  const object = await bucket.get(`releases/${tag}/${asset}`);
  if (!object) {
    return null;
  }
  const headers = {
    "Cache-Control": "public, max-age=86400",
    "Content-Length": String(object.size),
    "Content-Type": contentType(asset),
    ...(asset.endsWith(".txt") || asset.endsWith(".json")
      ? {}
      : { "Content-Disposition": `attachment; filename="${asset}"` })
  };
  return new Response(request.method === "HEAD" ? null : object.body, { headers });
}

async function tagPage(request: Request, bucket: ReleaseBucket, tag: string) {
  const sums = await bucket.get(`releases/${tag}/checksums.txt`);
  if (!sums) {
    return null;
  }
  const assets = (await sums.text())
    .split("\n")
    .map((line) => line.trim().split(/\s+/))
    .filter((fields) => fields.length === 2 && segment.test(fields[1].replace(/^\*/, "")))
    .map(([sha, name]) => ({ name: name.replace(/^\*/, ""), sha }));
  const rows = assets
    .map((a) => `<li><a href="/releases/download/${tag}/${a.name}">${a.name}</a> <code>${a.sha}</code></li>`)
    .join("");
  const html = `<!doctype html><meta charset="utf-8"><title>gs ${tag}</title><h1>gs ${tag}</h1><ul>${rows}<li><a href="/releases/download/${tag}/checksums.txt">checksums.txt</a></li></ul><p>Install: <code>curl -fsSL https://gitslice.io/install.sh | sh</code> or <code>gs upgrade</code>.</p>`;
  return new Response(request.method === "HEAD" ? null : html, {
    headers: { "Cache-Control": "public, max-age=300", "Content-Type": "text/html; charset=utf-8" }
  });
}

// releaseResponse answers /releases URLs from R2 when the release is there,
// and redirects to the GitHub releases otherwise (older releases, or no
// bucket, as in tests and staging). The URLs:
//   /releases, /releases/latest          -> /releases/tag/<latest>
//   /releases/tag/<tag>                  -> a page listing the files
//   /releases/latest/download/<asset>    -> the latest release's file
//   /releases/download/<tag>/<asset>     -> that release's file
export async function releaseResponse(request: Request, bucket: ReleaseBucket | null | undefined): Promise<Response | null> {
  const fallback = releaseRedirectTarget(request);
  if (!fallback) {
    return null;
  }
  if (!bucket) {
    return redirectTo(fallback);
  }
  const parts = new URL(request.url).pathname.split("/").filter(Boolean).slice(1);
  try {
    if (parts.length === 0 || (parts.length === 1 && parts[0] === "latest")) {
      const tag = await latestTag(bucket);
      return tag ? redirectTo(`/releases/tag/${tag}`) : redirectTo(fallback);
    }
    if (parts.length === 2 && parts[0] === "tag") {
      return (await tagPage(request, bucket, parts[1])) ?? redirectTo(fallback);
    }
    if (parts.length === 3 && parts[0] === "latest" && parts[1] === "download") {
      const tag = await latestTag(bucket);
      // Through the tag's URL, so a cached "latest" never mixes two releases.
      return tag ? redirectTo(`/releases/download/${tag}/${parts[2]}`) : redirectTo(fallback);
    }
    if (parts.length === 3 && parts[0] === "download") {
      return (await assetResponse(request, bucket, parts[1], parts[2])) ?? redirectTo(fallback);
    }
  } catch {
    // The store is unavailable: the GitHub copy may still be there.
  }
  return redirectTo(fallback);
}
