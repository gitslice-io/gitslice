import { StartClient } from "@tanstack/react-start/client";
import { StrictMode, startTransition } from "react";
import { hydrateRoot } from "react-dom/client";

// After a deploy, Vite's content-hashed chunk names change, so a tab that
// loaded the old index can 404 when it lazily imports a chunk ("Failed to fetch
// dynamically imported module"). Reload once to pick up the new build. The
// session-scoped guard prevents a reload loop if the failure is not deploy-related.
window.addEventListener("vite:preloadError", () => {
  const KEY = "vite-preload-reloaded";
  if (sessionStorage.getItem(KEY)) return;
  sessionStorage.setItem(KEY, "1");
  window.location.reload();
});

startTransition(() => {
  hydrateRoot(
    document,
    <StrictMode>
      <StartClient />
    </StrictMode>
  );
});
