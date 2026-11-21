export const AGENT_SESSION_COOKIE = "gs_agent_session";

// hasClerkSessionCookie says whether a Cookie header carries any sign of a
// signed-in Clerk session: a session token, or a client cookie that is not the
// "0" of a signed-out browser. Clerk names them with and without a per-instance
// suffix.
export function hasClerkSessionCookie(cookieHeader: string): boolean {
  for (const part of cookieHeader.split(";")) {
    const separator = part.indexOf("=");
    const name = (separator < 0 ? part : part.slice(0, separator)).trim();
    const value = separator < 0 ? "" : part.slice(separator + 1).trim();
    if (/^__session(?:_|$)/.test(name) && value !== "") {
      return true;
    }
    if (/^__client_uat(?:_|$)/.test(name) && value !== "" && value !== "0") {
      return true;
    }
  }
  return false;
}

// hasAgentSessionCookie says whether a Cookie header carries a Gitslice agent
// web session cookie.
export function hasAgentSessionCookie(cookieHeader: string): boolean {
  for (const part of cookieHeader.split(";")) {
    const separator = part.indexOf("=");
    const name = (separator < 0 ? part : part.slice(0, separator)).trim();
    const value = separator < 0 ? "" : part.slice(separator + 1).trim();
    if (name === AGENT_SESSION_COOKIE && value !== "") {
      return true;
    }
  }
  return false;
}

// hasSessionCookie says whether a Cookie header carries any sign of a signed-in
// session (Clerk or agent web session).
export function hasSessionCookie(cookieHeader: string): boolean {
  return hasClerkSessionCookie(cookieHeader) || hasAgentSessionCookie(cookieHeader);
}
