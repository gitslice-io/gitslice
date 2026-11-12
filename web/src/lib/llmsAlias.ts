// Agents are sometimes told to read "llama.txt" (a slip for llms.txt), which
// otherwise 404s; send them to the real file.
const ALIASES = new Set(["/llama.txt", "/llama.md", "/llm.txt", "/llms.md"]);

export function llmsAliasResponse(request: Request): Response | undefined {
  if (request.method !== "GET" && request.method !== "HEAD") {
    return undefined;
  }
  const url = new URL(request.url);
  if (!ALIASES.has(url.pathname.toLowerCase())) {
    return undefined;
  }
  return Response.redirect(new URL("/llms.txt", url).toString(), 301);
}
