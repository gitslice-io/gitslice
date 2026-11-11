import { clerkJSScriptUrl } from "@clerk/shared/loadClerkJsScript";

// Clerk's script is added to the page by Clerk's own code, so the browser
// only learns of it after the app's script has been fetched and run. A
// preload in the HTML starts the download with the page instead; Clerk then
// finds it already there. The address and the crossorigin value must be the
// ones Clerk uses for its script tag, or the browser fetches it twice, so the
// address comes from Clerk's own function.
export function clerkScriptPreload(publishableKey: string | undefined) {
  if (!publishableKey) {
    return undefined;
  }
  try {
    return {
      rel: "preload",
      as: "script",
      href: clerkJSScriptUrl({ publishableKey }),
      crossOrigin: "anonymous"
    } as const;
  } catch {
    return undefined;
  }
}
