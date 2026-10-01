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
    // Stage 1: resolve through the GitHub mirror. Stage 2 points this at
    // https://gitslice.io/git/gitslice/gitslice.git with subdir
    // "gitslice/gitslice" once that endpoint serves stable history and tags.
    repoURL: "https://github.com/gitslice-io/gitslice",
    source: {
      home: "https://github.com/gitslice-io/gitslice",
      directory: "https://github.com/gitslice-io/gitslice/tree/main{/dir}",
      file: "https://github.com/gitslice-io/gitslice/blob/main{/dir}/{file}#L{line}"
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
<p>go install ${escapeText(module.importPrefix)}/cmd/gs@latest</p>
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
