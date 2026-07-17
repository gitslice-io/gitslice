import handler, { createServerEntry } from "@tanstack/react-start/server-entry";

import { gitProxyTarget } from "./lib/gitProxy";

export default createServerEntry({
  async fetch(request) {
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
