// resolveRepoLink turns a relative link found in a document (a README's
// [guide](docs/guide.md), ../LICENSE, ./img.png) into the absolute repository
// path it points at, given the directory the document is in. It returns null
// for anything that is not a link to a path in the repository: a link with a
// scheme or host, a page-local #anchor, an app route that starts with "/", or a
// path that climbs out of the repository root.
export function resolveRepoLink(directory: string, href: string | null | undefined): string | null {
  const link = (href ?? "").trim();
  if (!link || link.startsWith("#") || link.startsWith("/") || /^[a-z][a-z0-9+.-]*:/i.test(link)) {
    return null;
  }
  let relative = link.split(/[?#]/, 1)[0];
  try {
    relative = decodeURIComponent(relative);
  } catch {
    // Keep the link as written when it is not valid percent-encoding.
  }
  if (!relative) {
    return null;
  }
  const parts = directory.split("/").filter(Boolean);
  for (const part of relative.replace(/\\/g, "/").split("/")) {
    if (!part || part === ".") {
      continue;
    }
    if (part === "..") {
      if (parts.length === 0) {
        return null;
      }
      parts.pop();
      continue;
    }
    parts.push(part);
  }
  return `/${parts.join("/")}`;
}
