// Release downloads are addressed through gitslice.io, so install.sh, gs
// upgrade and the docs never name the store that holds the bytes.
//
// Every release is built off GitHub (ops/release, started by the
// gitslice/release trigger) and stored in R2 under releases/<tag>/, with
// releases/latest.json naming the newest; the worker serves them from its
// RELEASES binding. Releases made before that were copied into R2 on
// 2026-11-17, so nothing is fetched from GitHub.

// Tags and asset names: letters, digits, '.', '_', '+' and '-', starting with
// a letter or digit. This keeps every key inside releases/ (no '/' or '..').
const segment = /^[A-Za-z0-9][A-Za-z0-9._+-]*$/;

// releasePath reports whether a request is for the releases tree, and its
// path segments after /releases when they name something it can serve:
// [], ["latest"], ["tag", tag], ["latest", "download", asset] or
// ["download", tag, asset]. Anything else under /releases is not a release.
export function releasePath(request: Request): string[] | null {
  if (request.method !== "GET" && request.method !== "HEAD") {
    return null;
  }
  const { pathname } = new URL(request.url);
  if (pathname !== "/releases" && !pathname.startsWith("/releases/")) {
    return null;
  }
  const parts = pathname.split("/").filter(Boolean).slice(1);
  if (parts.length === 0) {
    return parts;
  }
  if (parts.length === 1 && parts[0] === "latest") {
    return parts;
  }
  if (parts.length === 3 && parts[0] === "latest" && parts[1] === "download" && segment.test(parts[2])) {
    return parts;
  }
  if (parts.length === 2 && parts[0] === "tag" && segment.test(parts[1])) {
    return parts;
  }
  if (parts.length === 3 && parts[0] === "download" && segment.test(parts[1]) && segment.test(parts[2])) {
    return parts;
  }
  return null;
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
  // Shown as text, so the install script can be read before it is run.
  ".sh": "text/plain; charset=utf-8",
  ".json": "application/json",
  // The agents demo release (agents-demo-2026-10-02) holds videos.
  ".mp4": "video/mp4",
  ".srt": "text/plain; charset=utf-8"
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

function plain(status: number, message: string, maxAge = 60) {
  return new Response(message + "\n", {
    status,
    headers: { "Cache-Control": `public, max-age=${maxAge}`, "Content-Type": "text/plain; charset=utf-8" }
  });
}

const notFound = () => plain(404, "No such gs release or file. See https://gitslice.io/releases/latest.");
const unavailable = () => plain(503, "gs releases are unavailable right now; try again in a minute.", 0);

// releaseResponse answers /releases URLs from R2:
//   /releases, /releases/latest          -> /releases/tag/<latest>
//   /releases/tag/<tag>                  -> a page listing the files
//   /releases/latest/download/<asset>    -> the latest release's file
//   /releases/download/<tag>/<asset>     -> that release's file
// A release or file that isn't there is 404; no bucket, or a failing one, is
// 503.
export async function releaseResponse(request: Request, bucket: ReleaseBucket | null | undefined): Promise<Response | null> {
  const parts = releasePath(request);
  if (!parts) {
    return null;
  }
  if (!bucket) {
    return unavailable();
  }
  try {
    if (parts.length === 0 || (parts.length === 1 && parts[0] === "latest")) {
      const tag = await latestTag(bucket);
      return tag ? redirectTo(`/releases/tag/${tag}`) : notFound();
    }
    if (parts.length === 2 && parts[0] === "tag") {
      return (await tagPage(request, bucket, parts[1])) ?? notFound();
    }
    if (parts.length === 3 && parts[0] === "latest" && parts[1] === "download") {
      const tag = await latestTag(bucket);
      // Through the tag's URL, so a cached "latest" never mixes two releases.
      return tag ? redirectTo(`/releases/download/${tag}/${parts[2]}`) : notFound();
    }
    return (await assetResponse(request, bucket, parts[1], parts[2])) ?? notFound();
  } catch {
    return unavailable();
  }
}
