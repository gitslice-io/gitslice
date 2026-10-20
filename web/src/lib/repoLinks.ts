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

// projectRoot is the folder a document's root-absolute paths ("/docs/x.png")
// are relative to, as on GitHub: the slice's included path that holds the
// document. It is null when no included path does.
export function projectRoot(includedPaths: readonly string[] | undefined, documentPath: string): string | null {
  const document = "/" + documentPath.split("/").filter(Boolean).join("/");
  let best: string | null = null;
  for (const included of includedPaths ?? []) {
    const root = "/" + included.split("/").filter(Boolean).join("/");
    if ((document === root || document.startsWith(root === "/" ? "/" : root + "/")) && (best === null || root.length > best.length)) {
      best = root;
    }
  }
  return best;
}

// resolveRepoImage is resolveRepoLink for an image: besides the paths relative
// to the document, it also resolves a root-absolute one ("/img/logo.png")
// against the project root, when there is one. A link to "/..." can be an app
// address, so links do not get this; no app address serves an image, so an
// image can. A path that climbs out of the project root gives null.
export function resolveRepoImage(directory: string, root: string | null | undefined, src: string | null | undefined): string | null {
  const link = (src ?? "").trim();
  if (link.startsWith("/") && !link.startsWith("//")) {
    if (!root) {
      return null;
    }
    const resolved = resolveRepoLink(root, link.replace(/^\/+/, ""));
    return resolved !== null && (resolved === root || resolved.startsWith(root === "/" ? "/" : root + "/")) ? resolved : null;
  }
  return resolveRepoLink(directory, src);
}

// markRepoImages takes the <img> elements of rendered markdown whose src is a
// path in the repository (logo.png, ../docs/shot.png, and with a project root
// /docs/shot.png) and, instead of leaving the browser to request them from the
// page's own address (where they do not exist), drops their src and records
// the absolute repository path in data-repo-image, for the viewer to load
// through the API. Other images are left as they are.
export function markRepoImages(html: string, directory: string, root?: string | null): string {
  if (typeof DOMParser === "undefined" || !html.includes("<img")) {
    return html;
  }
  const doc = new DOMParser().parseFromString(html, "text/html");
  let changed = false;
  for (const image of Array.from(doc.querySelectorAll("img"))) {
    const path = resolveRepoImage(directory, root, image.getAttribute("src"));
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

// resolvePictures turns each <picture> of rendered markdown into its <img>,
// showing the source the browser would pick, except that a
// prefers-color-scheme source follows the site's theme rather than the
// operating system's, as on GitHub: a README's dark-mode logo is for a dark
// page. The <img> then goes through markRepoImages like any other image, which
// a <source srcset> would not.
export function resolvePictures(html: string, dark: boolean): string {
  if (typeof DOMParser === "undefined" || !html.includes("<picture")) {
    return html;
  }
  const doc = new DOMParser().parseFromString(html, "text/html");
  for (const picture of Array.from(doc.querySelectorAll("picture"))) {
    const image = picture.querySelector("img");
    const sources = Array.from(picture.querySelectorAll("source"));
    const chosen = sources.find((source) => sourceMatches(source.getAttribute("media"), dark));
    const url = chosen ? firstSrcsetUrl(chosen.getAttribute("srcset")) : "";
    if (image && url) {
      image.setAttribute("src", url);
      image.removeAttribute("srcset");
    }
    for (const source of sources) {
      source.remove();
    }
    if (image) {
      picture.replaceWith(image);
    }
  }
  return doc.body.innerHTML;
}

function sourceMatches(media: string | null, dark: boolean): boolean {
  const query = (media ?? "").trim();
  if (!query) {
    return true;
  }
  const scheme = /prefers-color-scheme\s*:\s*(dark|light)/i.exec(query);
  if (scheme) {
    return (scheme[1].toLowerCase() === "dark") === dark;
  }
  return typeof window !== "undefined" && typeof window.matchMedia === "function" && window.matchMedia(query).matches;
}

// firstSrcsetUrl is the first candidate's URL of a srcset ("a.png 1x, b.png 2x").
function firstSrcsetUrl(srcset: string | null): string {
  return (srcset ?? "").split(",")[0]?.trim().split(/\s+/)[0] ?? "";
}
