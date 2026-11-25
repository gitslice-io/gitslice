import handler, { createServerEntry } from "@tanstack/react-start/server-entry";

import { handleIngestProxy } from "./analytics/ingestProxy";
import { handleAgentAuthRequest } from "./server/agentAuthHandler";
import { gitProxyTarget } from "./server/gitProxy";
import { llmsAliasResponse } from "./server/llmsAlias";
import { handleMCPProxyRequest } from "./server/mcpProxy";
import { apiSliceLookup, goImportResponse, sliceGoImportResponse } from "./server/goImport";
import { servePage, type PageCacheStore } from "./server/pageCache";
import { releaseResponse, type ReleaseBucket } from "./server/releases";
import { socialImageResponse } from "./server/socialImage";
import { handleStatusRequest } from "./server/statusHandler";

export default createServerEntry({
  async fetch(request) {
    // Proxy first-party PostHog ingestion (`/ingest/*`) before the app handler.
    const ingestResponse = await handleIngestProxy(request);
    if (ingestResponse) {
      return ingestResponse;
    }

    // Hosted Streamable HTTP MCP endpoint (`/mcp`).
    const mcpResponse = await handleMCPProxyRequest(
      request,
      import.meta.env.VITE_API_BASE_URL ?? ""
    );
    if (mcpResponse) {
      return mcpResponse;
    }

    // Same-origin agent web session endpoints (`/auth/agent/login`, `/token`, `/logout`).
    const agentAuthResponse = await handleAgentAuthRequest(
      request,
      import.meta.env.VITE_API_BASE_URL ?? ""
    );
    if (agentAuthResponse) {
      return agentAuthResponse;
    }

    const llmsAlias = llmsAliasResponse(request);
    if (llmsAlias) {
      return llmsAlias;
    }

    // Same-origin live health probe (`/api/status`) for the Status page.
    const statusResponse = await handleStatusRequest(
      request,
      import.meta.env.VITE_API_BASE_URL ?? ""
    );
    if (statusResponse) {
      return statusResponse;
    }

    // `go get` discovery for gitslice.io import paths and stable release
    // download URLs. Both are answered here, never by the app.
    const goImport =
      goImportResponse(request) ??
      (await sliceGoImportResponse(request, apiSliceLookup(import.meta.env.VITE_API_BASE_URL ?? "")));
    if (goImport) {
      return goImport;
    }
    // Nitro's Cloudflare entry puts the Worker's bindings on globalThis.__env__.
    const bindings = (globalThis as { __env__?: { RELEASES?: ReleaseBucket } }).__env__;
    const release = await releaseResponse(request, bindings?.RELEASES);
    if (release) {
      return release;
    }

    // Link-preview images and account pictures, drawn by the API and cached
    // at the edge.
    const socialImage = await socialImageResponse(request, import.meta.env.VITE_API_BASE_URL ?? "");
    if (socialImage) {
      return socialImage;
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

    // Pages for visitors with no session are kept briefly at the edge
    // (lib/pageCache); everything else is rendered for the request.
    return servePage(request, {
      buildId: typeof __BUILD_ID__ === "string" ? __BUILD_ID__ : "dev",
      cache: edgePageCache(),
      render: async (toRender) => handler.fetch(toRender),
      waitUntil: backgroundWork(request)
    });
  }
});

// The Workers cache of this data center, by key; absent anywhere else.
function edgePageCache(): PageCacheStore | undefined {
  const cache = (globalThis as { caches?: { default?: Cache } }).caches?.default;
  if (!cache) {
    return undefined;
  }
  return {
    match: (key) => cache.match(key),
    put: (key, response) => cache.put(key, response)
  };
}

// Nitro's Cloudflare entry puts the Worker's waitUntil on the request.
function backgroundWork(request: Request): ((work: Promise<unknown>) => void) | undefined {
  const waitUntil = (request as { waitUntil?: (work: Promise<unknown>) => void }).waitUntil;
  return typeof waitUntil === "function" ? (work) => waitUntil(work) : undefined;
}
