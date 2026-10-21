// What a shared slice link shows: the page title and the Open Graph and
// Twitter card tags link previews read (Slack, X, Discord, LinkedIn...).
// Crawlers do not run scripts, so these are in the server-rendered <head>.

export interface SliceSocial {
  account: string;
  slice: string;
  description: string;
  // The repository path open on the page, or "" for the slice itself.
  path: string;
  // The site's origin, such as https://gitslice.io; tags need absolute URLs.
  origin: string;
}

type HeadMeta = { title: string } | { name: string; content: string } | { property: string; content: string };

const DESCRIPTION_LIMIT = 300;

export const SITE_DESCRIPTION =
  "Gitslice: slices of one repository, for people and agents.";

export function sliceHead(social: SliceSocial): {
  meta: HeadMeta[];
  links: Array<{ rel: string; href: string }>;
} {
  const name = `${social.account}/${social.slice}`;
  const description = clip(
    social.description.trim() || `${name} on Gitslice: browse its files, history and changesets.`
  );
  const relativePath = social.path.replace(/^\/+/, "");
  const title = relativePath
    ? `${relativePath} · ${name} · Gitslice`
    : social.description.trim()
      ? `${name}: ${clip(social.description.trim(), 120)} · Gitslice`
      : `${name} · Gitslice`;
  const origin = social.origin.replace(/\/+$/, "");
  const pagePath = `/slices/${encodeURIComponent(social.account)}/${encodeURIComponent(social.slice)}`;
  const url = `${origin}${pagePath}${relativePath ? `?path=${encodeURIComponent(social.path)}` : ""}`;
  // The version changes with what the card shows, so a site that cached the
  // old image fetches the new one after the description changes.
  const image = `${origin}/og/slices/${encodeURIComponent(social.account)}/${encodeURIComponent(social.slice)}.png?v=${shortHash(
    `${name}\n${social.description}`
  )}`;
  const ogTitle = relativePath ? `${relativePath} · ${name}` : name;

  return {
    meta: [
      { title },
      { name: "description", content: description },
      { property: "og:type", content: "website" },
      { property: "og:site_name", content: "Gitslice" },
      { property: "og:title", content: ogTitle },
      { property: "og:description", content: description },
      { property: "og:url", content: url },
      { property: "og:image", content: image },
      { property: "og:image:width", content: "1200" },
      { property: "og:image:height", content: "630" },
      { property: "og:image:alt", content: `${name} on Gitslice` },
      { name: "twitter:card", content: "summary_large_image" },
      { name: "twitter:title", content: ogTitle },
      { name: "twitter:description", content: description },
      { name: "twitter:image", content: image }
    ],
    links: [{ rel: "canonical", href: url }]
  };
}

function clip(text: string, limit = DESCRIPTION_LIMIT): string {
  const flat = text.replace(/\s+/g, " ").trim();
  if (flat.length <= limit) {
    return flat;
  }
  return `${flat.slice(0, limit - 1).trimEnd()}…`;
}

// shortHash is a small stable hash (FNV-1a) for cache-busting URLs.
export function shortHash(text: string): string {
  let hash = 0x811c9dc5;
  for (let i = 0; i < text.length; i++) {
    hash ^= text.charCodeAt(i);
    hash = Math.imul(hash, 0x01000193);
  }
  return (hash >>> 0).toString(36);
}
