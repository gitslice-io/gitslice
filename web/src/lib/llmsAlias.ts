// Agents are sometimes told to read "llama.txt" (a slip for llms.txt) or
// lowercase "/skill.md" (for "/SKILL.md"), which otherwise 404; send them to
// the canonical file.
const LLMS_ALIASES = new Set(["/llama.txt", "/llama.md", "/llm.txt", "/llms.md"]);
const SKILL_ALIASES = new Set(["/skill.md", "/skills.md"]);

export function llmsAliasResponse(request: Request): Response | undefined {
  if (request.method !== "GET" && request.method !== "HEAD") {
    return undefined;
  }
  const url = new URL(request.url);
  const lower = url.pathname.toLowerCase();
  if (LLMS_ALIASES.has(lower)) {
    return Response.redirect(new URL("/llms.txt", url).toString(), 301);
  }
  if (url.pathname !== "/SKILL.md" && SKILL_ALIASES.has(lower)) {
    return Response.redirect(new URL("/SKILL.md", url).toString(), 301);
  }
  return undefined;
}
