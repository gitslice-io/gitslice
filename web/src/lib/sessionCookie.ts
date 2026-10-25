// hasSessionCookie says whether a Cookie header carries any sign of a signed-in
// Clerk session: a session token, or a client cookie that is not the "0" of a
// signed-out browser. Clerk names them with and without a per-instance suffix.
export function hasSessionCookie(cookieHeader: string): boolean {
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
