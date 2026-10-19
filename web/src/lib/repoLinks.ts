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

// markRepoImages takes the <img> elements of rendered markdown whose src is a
// path in the repository (logo.png, ../docs/shot.png) and, instead of leaving
// the browser to request them from the page's own address (where they do not
// exist), drops their src and records the repository path in data-repo-image,
// for the viewer to load through the API. Other images are left as they are.
export function markRepoImages(html: string, directory: string): string {
  if (typeof DOMParser === "undefined" || !html.includes("<img")) {
    return html;
  }
  const doc = new DOMParser().parseFromString(html, "text/html");
  let changed = false;
  for (const image of Array.from(doc.querySelectorAll("img"))) {
    const path = resolveRepoLink(directory, image.getAttribute("src"));
    if (path === null) {
      continue;
    }
    image.removeAttribute("src");
    image.removeAttribute("srcset");
    image.setAttribute("data-repo-image", path);
    changed = true;
  }
  return changed ? doc.body.innerHTML : html;
}
