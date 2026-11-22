// goImportResponse answers the discovery request the go command makes for a
// vanity import path (https://go.dev/ref/mod#vcs-find): it fetches
// `https://gitslice.io/<path>?go-get=1` and reads the `go-import` meta tag to
// learn where the module's repository lives. Keeping the import path on
// gitslice.io lets the repository move without renaming the module
// (design/21_self_hosting.md).

export interface GoModule {
  /** Module path, e.g. "gitslice.io/gitslice". Its first segment is the host. */
  importPrefix: string;
  vcs: "git";
  /** Repository the go command clones. */
  repoURL: string;
  /**
   * Module root inside the repository; omit for the repository root. A
   * subdirectory needs Go 1.25+ when the go command reads this tag directly
   * (GOPROXY=direct), and version tags must then carry it as a prefix.
   */
  subdir?: string;
  /** Source browsing URL templates for the go-source meta tag. */
  source?: { home: string; directory: string; file: string };
}

export const goModules: GoModule[] = [
  {
    importPrefix: "gitslice.io/gitslice",
    vcs: "git",
    // Stage 2 of design/21_self_hosting.md: Gitslice serves its own module.
    // The projected repository keeps the account-rooted layout, so the module
    // root is the gitslice/gitslice subdirectory, and the projection publishes
    // gitslice/gitslice/vX.Y.Z tags for it.
    repoURL: "https://gitslice.io/git/gitslice/gitslice.git",
    subdir: "gitslice/gitslice",
    source: {
      home: "https://gitslice.io/slices/gitslice/gitslice",
      directory: "https://gitslice.io/slices/gitslice/gitslice?path=/gitslice/gitslice{/dir}",
      file: "https://gitslice.io/slices/gitslice/gitslice?path=/gitslice/gitslice{/dir}/{file}"
    }
  }
];

export function goImportResponse(
  request: Request,
  modules: GoModule[] = goModules
): Response | null {
  if (request.method !== "GET" && request.method !== "HEAD") {
    return null;
  }
  const url = new URL(request.url);
  if (url.searchParams.get("go-get") !== "1") {
    return null;
  }
  const module = modules.find((candidate) =>
    pathWithinModule(url.pathname, candidate.importPrefix)
  );
  if (!module) {
    return null;
  }

  return moduleResponse(request, module, `go install ${module.importPrefix}/cmd/gs@latest`);
}

function moduleResponse(request: Request, module: GoModule, hint: string): Response {
  const importTag = [module.importPrefix, module.vcs, module.repoURL, module.subdir]
    .filter((part): part is string => Boolean(part))
    .join(" ");
  const meta = [`<meta name="go-import" content="${escapeAttribute(importTag)}">`];
  if (module.source) {
    const sourceTag = [
      module.importPrefix,
      module.source.home,
      module.source.directory,
      module.source.file
    ].join(" ");
    meta.push(`<meta name="go-source" content="${escapeAttribute(sourceTag)}">`);
  }
  const body = `<!DOCTYPE html>
<html>
<head>
<meta charset="utf-8">
${meta.join("\n")}
</head>
<body>
<p>${escapeText(hint)}</p>
</body>
</html>
`;
  return new Response(request.method === "HEAD" ? null : body, {
    status: 200,
    headers: {
      "Content-Type": "text/html; charset=utf-8",
      "Cache-Control": "public, max-age=300"
    }
  });
}

/** What the API says about a slice, or null when it does not exist or is not visible. */
export type SliceLookup = (
  account: string,
  slice: string,
  authorization: string | null
) => Promise<{ includedPaths: string[] } | null>;

const SLUG = /^[A-Za-z0-9][A-Za-z0-9._-]*$/;

/**
 * Answers `go get` discovery for any slice: gitslice.io/<account>/<slice>
 * (and package paths below it) is a module whose repository is the slice's
 * Git projection and whose root is the slice's included path, the directory
 * its version tags are prefixed with (design/21_self_hosting.md, "Go
 * modules"). Fixed modules (goModules) are answered first by goImportResponse.
 */
export async function sliceGoImportResponse(
  request: Request,
  lookup: SliceLookup
): Promise<Response | null> {
  if (request.method !== "GET" && request.method !== "HEAD") {
    return null;
  }
  const url = new URL(request.url);
  if (url.searchParams.get("go-get") !== "1") {
    return null;
  }
  const [account, slice] = url.pathname.split("/").filter(Boolean);
  // /git/... is the Git endpoint itself, not a module path.
  if (!account || !slice || account === "git" || !SLUG.test(account) || !SLUG.test(slice)) {
    return null;
  }
  const host = url.host;
  const importPrefix = `${host}/${account}/${slice}`;
  const found = await lookup(account, slice, bearerFromRequest(request));
  if (!found) {
    return goGetError(
      request,
      `no Go module at ${importPrefix}: the slice ${account}/${slice} does not exist or is private. ` +
        `For a private slice, put a Gitslice token in ~/.netrc for ${host} (login: your username, password: the token) and set GOPRIVATE=${host}/${account}/*.`
    );
  }
  const roots = found.includedPaths.map((path) => path.replace(/^\/+|\/+$/g, "")).filter(Boolean);
  if (roots.length !== 1) {
    return goGetError(
      request,
      `no Go module at ${importPrefix}: the slice includes ${roots.length === 0 ? "the whole account" : `${roots.length} paths`}, ` +
        "and a module needs exactly one directory as its root."
    );
  }
  const subdir = roots[0];
  const sliceURL = `https://${host}/slices/${encodeURIComponent(account)}/${encodeURIComponent(slice)}`;
  return moduleResponse(
    request,
    {
      importPrefix,
      vcs: "git",
      repoURL: `https://${host}/git/${account}/${slice}.git`,
      subdir,
      source: {
        home: sliceURL,
        directory: `${sliceURL}?path=/${subdir}{/dir}`,
        file: `${sliceURL}?path=/${subdir}{/dir}/{file}`
      }
    },
    `go get ${importPrefix}@latest`
  );
}

// The go command sends ~/.netrc credentials as Basic auth; the API takes the
// token (the password) as a Bearer token.
function bearerFromRequest(request: Request): string | null {
  const header = request.headers.get("Authorization") ?? "";
  const [scheme, value] = header.split(" ", 2);
  if (!value) {
    return null;
  }
  if (scheme.toLowerCase() === "bearer") {
    return value;
  }
  if (scheme.toLowerCase() === "basic") {
    try {
      const decoded = atob(value);
      const password = decoded.slice(decoded.indexOf(":") + 1);
      return password || null;
    } catch {
      return null;
    }
  }
  return null;
}

function goGetError(request: Request, message: string): Response {
  return new Response(request.method === "HEAD" ? null : `${message}\n`, {
    status: 404,
    headers: { "Content-Type": "text/plain; charset=utf-8", "Cache-Control": "no-store" }
  });
}

/** Looks a slice up through the API's ResolveSlice. */
export function apiSliceLookup(apiBase: string, fetchFn: typeof fetch = fetch): SliceLookup {
  const base = apiBase.replace(/\/+$/, "");
  return async (account, slice, token) => {
    if (!base) {
      return null;
    }
    const headers: Record<string, string> = { "Content-Type": "application/json" };
    if (token) {
      headers.Authorization = `Bearer ${token}`;
    }
    const response = await fetchFn(`${base}/gitslice.core.v1.SliceService/ResolveSlice`, {
      method: "POST",
      headers,
      body: JSON.stringify({ ref: { account, slice } })
    });
    if (!response.ok) {
      return null;
    }
    const resolved = (await response.json()) as { definition?: { includedPaths?: string[] } };
    return { includedPaths: resolved.definition?.includedPaths ?? [] };
  };
}

function pathWithinModule(pathname: string, importPrefix: string): boolean {
  const slash = importPrefix.indexOf("/");
  if (slash < 0) {
    return false;
  }
  const modulePath = importPrefix.slice(slash);
  const trimmed = pathname.replace(/\/+$/, "");
  return trimmed === modulePath || trimmed.startsWith(`${modulePath}/`);
}

function escapeAttribute(value: string): string {
  return escapeText(value).replace(/"/g, "&quot;");
}

function escapeText(value: string): string {
  return value.replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;");
}
