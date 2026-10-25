import { hasSessionCookie } from "./sessionCookie";

// The banner that tells someone arriving by a shared link what Gitslice is.
// Whether to show it is decided from cookies, which the server render and the
// browser both see, so a visitor who dismissed it does not see it flash in the
// page's HTML, and a signed-in person is never shown it.

export const VISITOR_BANNER_COOKIE = "gitslice_welcome";
const THIRTY_DAYS = 60 * 60 * 24 * 30;

function cookieValue(cookieHeader: string, name: string): string | undefined {
  for (const part of cookieHeader.split(";")) {
    const separator = part.indexOf("=");
    if (separator > 0 && part.slice(0, separator).trim() === name) {
      return part.slice(separator + 1).trim();
    }
  }
  return undefined;
}

// visitorBannerWanted: the visitor has no session and has not dismissed it.
export function visitorBannerWanted(cookieHeader: string): boolean {
  return !hasSessionCookie(cookieHeader) && cookieValue(cookieHeader, VISITOR_BANNER_COOKIE) !== "1";
}

// The Set-Cookie string that remembers a dismissal for thirty days.
export function dismissalCookie(secure: boolean): string {
  return `${VISITOR_BANNER_COOKIE}=1; Max-Age=${THIRTY_DAYS}; Path=/; SameSite=Lax${secure ? "; Secure" : ""}`;
}

// Pages about Gitslice itself do not need telling.
export function visitorBannerHiddenOn(pathname: string): boolean {
  return /^\/(?:doc|blogs?)(?:\/|$)/.test(pathname);
}
