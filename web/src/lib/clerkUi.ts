import { hasSessionCookie } from "./sessionCookie";

// Clerk's UI (the sign-in and sign-up cards, the account button) is a second
// script of about 300 KB compressed, and Clerk does not report the session as
// loaded until it has arrived. A visitor with no session, reading a page that
// shows none of those, does not need it: the server render decides, per
// document, whether the page asks Clerk for it.

const CLERK_UI_PATHS = /^\/(?:login|signup|cli-login)(?:\/|$)/;

// clerkUiWanted: the request may be signed in (the account button), or is for
// a page that shows one of Clerk's cards.
export function clerkUiWanted(pathname: string, cookieHeader: string): boolean {
  return CLERK_UI_PATHS.test(pathname) || hasSessionCookie(cookieHeader);
}

// The decision is written on <html> so the browser reads back what the server
// decided, and keeps to it for the life of the document: Clerk is configured
// once.
export const CLERK_UI_ATTRIBUTE = "data-clerk-ui";

// clerkUiInDocument says whether this document asked Clerk for its UI. True
// on the server and wherever the attribute is missing (tests, older HTML).
export function clerkUiInDocument(): boolean {
  if (typeof document === "undefined") {
    return true;
  }
  return document.documentElement.getAttribute(CLERK_UI_ATTRIBUTE) !== "0";
}

const RELOAD_KEY = "gitslice-clerk-ui-reload";

// reloadForClerkUi loads the page again from the server, which then includes
// the UI (the address, or the session that has since appeared, asks for it).
// It gives up after one try for an address, so a page can never loop.
export function reloadForClerkUi(): boolean {
  if (typeof window === "undefined" || clerkUiInDocument()) {
    return false;
  }
  const address = window.location.pathname + window.location.search;
  try {
    if (window.sessionStorage.getItem(RELOAD_KEY) === address) {
      return false;
    }
    window.sessionStorage.setItem(RELOAD_KEY, address);
  } catch {
    // No storage: reload anyway; the server's answer for this address ends it.
  }
  window.location.reload();
  return true;
}
