import type { CreateApiClientOptions } from "./rpcError";
import type { ApiClient } from "./useApi";

type ClientModule = typeof import("./client");

let clientModule: Promise<ClientModule> | undefined;

// loadApiClientModule fetches the client's code once. A failed fetch (the
// network dropped) is not remembered, so the next call tries again.
export function loadApiClientModule(): Promise<ClientModule> {
  clientModule ??= import("./client").catch((error: unknown) => {
    clientModule = undefined;
    throw error;
  });
  return clientModule;
}

// createLazyApiClient is createApiClient without its code: the client is
// about a quarter of the script every page would otherwise load before it
// can respond to input, and the landing page, the docs, the blog and the
// sign-in pages never make a call. Each method fetches the client on first
// use and then calls through to it, so callers see the same promises and
// streams, one request later the first time.
export function createLazyApiClient(options: CreateApiClientOptions): ApiClient {
  let client: Promise<ApiClient> | undefined;
  const load = () => (client ??= loadApiClientModule().then((module) => module.createApiClient(options)));
  const methods = new Map<string, unknown>();

  return new Proxy({} as ApiClient, {
    get(_target, name) {
      // Not a thenable, and no well-known symbols: only API methods.
      if (typeof name !== "string" || name === "then") {
        return undefined;
      }
      let method = methods.get(name);
      if (!method) {
        const call = async (args: unknown[]) => {
          const target = (await load()) as unknown as Record<string, (...args: unknown[]) => unknown>;
          return target[name](...args);
        };
        // The stream* methods are async generators; the rest return promises.
        method = name.startsWith("stream")
          ? async function* (...args: unknown[]) {
              yield* (await call(args)) as AsyncGenerator<unknown>;
            }
          : (...args: unknown[]) => call(args);
        methods.set(name, method);
      }
      return method;
    }
  });
}
