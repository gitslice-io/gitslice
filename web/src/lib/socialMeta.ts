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

export interface ChangesetSocial {
  // The id in the changeset's link: /cs/{handle}.
  handle: string;
  title: string;
  // Markdown; only its first paragraph is used.
  description: string;
  status: string;
  author: string;
  account: string;
  slice: string;
  // How many files the changeset changes; 0 when not known.
  fileCount: number;
  origin: string;
}

// What a shared changeset link shows: its title as the page title, and a
// description that says what it is (status, author, slice, size) followed by
// the start of its own description. The picture is the slice's card. A page
// is only rendered with these for a viewer who can read the changeset, so a
// private changeset's link shows none of it.
export function changesetHead(social: ChangesetSocial): {
  meta: HeadMeta[];
  links: Array<{ rel: string; href: string }>;
} {
  const sliceName = social.account && social.slice ? `${social.account}/${social.slice}` : "";
  const title = social.title.trim() || "Untitled changeset";
  const status = statusWord(social.status);
  const facts =
    `${status} changeset ${social.handle}` +
    (social.author ? ` by ${social.author}` : "") +
    (sliceName ? ` in ${sliceName}` : "") +
    (social.fileCount > 0 ? `, ${social.fileCount} ${social.fileCount === 1 ? "file" : "files"} changed` : "") +
    ".";
  const intro = firstParagraph(social.description);
  const description = clip(intro ? `${facts} ${intro}` : facts);
  const origin = social.origin.replace(/\/+$/, "");
  const url = `${origin}/cs/${encodeURIComponent(social.handle)}`;
  // The slice's card; the generic one when the changeset names no slice.
  const image = sliceName
    ? `${origin}/og/slices/${encodeURIComponent(social.account)}/${encodeURIComponent(social.slice)}.png`
    : `${origin}/og/default.png`;
  const pageTitle = changesetPageTitle(title, social.account, social.slice);

  return {
    meta: [
      { title: pageTitle },
      { name: "description", content: description },
      { property: "og:type", content: "article" },
      { property: "og:site_name", content: "Gitslice" },
      { property: "og:title", content: clip(title, 100) },
      { property: "og:description", content: description },
      { property: "og:url", content: url },
      { property: "og:image", content: image },
      { property: "og:image:width", content: "1200" },
      { property: "og:image:height", content: "630" },
      { property: "og:image:alt", content: `${sliceName || "Gitslice"} on Gitslice` },
      { name: "twitter:card", content: "summary_large_image" },
      { name: "twitter:title", content: clip(title, 100) },
      { name: "twitter:description", content: description },
      { name: "twitter:image", content: image }
    ],
    links: [{ rel: "canonical", href: url }]
  };
}

// The page title of a changeset, for the tab and history: the same as the
// head's. Used when the page learns of the changeset after it was routed.
export function changesetPageTitle(title: string, account: string, slice: string): string {
  const name = account && slice ? ` · ${account}/${slice}` : "";
  return `${clip(title.trim() || "Untitled changeset", 100)}${name} · Gitslice`;
}

function statusWord(status: string): string {
  switch (status.toLowerCase()) {
    case "submitted":
    case "merged":
      return "Landed";
    case "abandoned":
      return "Abandoned";
    case "open":
    case "published":
      return "Open";
    default:
      return "Draft";
  }
}

// The first paragraph of Markdown text as plain words: no headings, list
// markers, code fences or emphasis, which a preview shows as noise.
function firstParagraph(markdown: string): string {
  const paragraph = markdown
    .trim()
    .split(/\n\s*\n/)
    .map((block) => block.trim())
    .find((block) => block && !block.startsWith("```"));
  return (paragraph ?? "")
    .replace(/^#{1,6}\s+/gm, "")
    .replace(/^\s*(?:[-*+]|\d+\.)\s+/gm, "")
    .replace(/\[([^\]]+)\]\([^)]*\)/g, "$1")
    .replace(/[`*_~]/g, "")
    .replace(/\s+/g, " ")
    .trim();
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
