import handler, { createServerEntry } from "@tanstack/react-start/server-entry";

import { handleIngestProxy } from "./analytics/ingestProxy";
import { gitProxyTarget } from "./lib/gitProxy";
import { goImportResponse } from "./lib/goImport";
import { releaseRedirect } from "./lib/releases";

export default createServerEntry({
  async fetch(request) {
    // Proxy first-party PostHog ingestion (`/ingest/*`) before the app handler.
    const ingestResponse = await handleIngestProxy(request);
    if (ingestResponse) {
      return ingestResponse;
    }

    // `go get` discovery for gitslice.io import paths and stable release
    // download URLs. Both are answered here, never by the app.
    const goImport = goImportResponse(request);
    if (goImport) {
      return goImport;
    }
    const release = releaseRedirect(request);
    if (release) {
      return release;
    }

    const target = gitProxyTarget(request);
    if (target) {
      const init: RequestInit & { duplex?: "half" } = {
        method: request.method,
        headers: request.headers,
        redirect: "manual"
      };

      if (
        request.method !== "GET" &&
        request.method !== "HEAD" &&
        request.body !== null
      ) {
        init.body = request.body;
        init.duplex = "half";
      }

      return fetch(target, init);
    }

    return handler.fetch(request);
  }
});
