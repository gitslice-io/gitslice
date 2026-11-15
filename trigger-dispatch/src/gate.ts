// What a trigger script may reach (design/34_triggers.md): https, to the hosts
// its trigger lists in network, by name.

// hostAllowed matches a host against network entries: an exact name, or
// "*.example.com" for any name under example.com (not example.com itself).
export function hostAllowed(host: string, network: readonly string[]): boolean {
  host = host.toLowerCase().replace(/\.$/, "");
  if (isAddress(host)) return false;
  for (const entry of network) {
    const e = entry.toLowerCase();
    if (e.startsWith("*.")) {
      if (host.endsWith(e.slice(1)) && host.length > e.length - 1) return true;
    } else if (host === e) {
      return true;
    }
  }
  return false;
}

// isAddress reports an IP literal, which network never allows.
export function isAddress(host: string): boolean {
  return /^\d{1,3}(\.\d{1,3}){3}$/.test(host) || host.startsWith("[") || host.includes(":");
}

// textual says whether a body is worth searching for placeholders.
export function textual(contentType: string | null): boolean {
  if (!contentType) return true;
  const t = contentType.toLowerCase();
  return t.startsWith("text/") || t.includes("json") || t.includes("x-www-form-urlencoded") || t.includes("xml");
}
