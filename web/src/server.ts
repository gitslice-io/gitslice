import handler, { createServerEntry } from "@tanstack/react-start/server-entry";

import { handleIngestProxy } from "./analytics/ingestProxy";

export default createServerEntry({
  async fetch(request) {
    // Proxy first-party PostHog ingestion (`/ingest/*`) before the app handler.
    const ingestResponse = await handleIngestProxy(request);
    if (ingestResponse) {
      return ingestResponse;
    }
    return handler.fetch(request);
  }
});
