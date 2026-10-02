// The review agent: every changeset an agent pushes gets a structured review
// from a Workers AI model before it can land. Deterministic checks run first,
// protected paths always escalate to a human, and the model decides the rest.

import { unifiedDiff } from "./merge";

export type Verdict = "approve" | "request_changes" | "escalate";

export interface Review {
  verdict: Verdict;
  summary: string;
  concerns: string[];
  model: string;
  ms: number;
}

export interface ReviewFile {
  path: string;
  before: string | null; // null: added
  after: string | null; // null: deleted
}

export interface ReviewInput {
  agent: string;
  task: string;
  context: string;
  files: ReviewFile[];
  protectedPatterns: RegExp[];
}

const MODELS = ["@cf/openai/gpt-oss-120b", "@cf/meta/llama-3.3-70b-instruct-fp8-fast"] as const;
const MAX_DIFF_CHARS = 14000;

const INSTRUCTIONS = `You review changes that autonomous coding agents push to a shared TypeScript codebase.
Approve a change when it does what the task says, stays within that scope, and is syntactically valid.
Request changes when it breaks syntax, edits unrelated code, deletes functionality without reason, adds secrets, or contradicts the task.
Escalate to a human when the change touches payments, authentication, or anything security sensitive.
Reply with JSON only: {"verdict":"approve"|"request_changes"|"escalate","summary":"one sentence","concerns":["..."]}`;

export async function review(ai: Ai, input: ReviewInput): Promise<Review> {
  const started = Date.now();

  // Deterministic checks the model must not overrule.
  for (const file of input.files) {
    if (file.after !== null && file.path.endsWith(".json")) {
      try {
        JSON.parse(file.after);
      } catch (err) {
        return {
          verdict: "request_changes",
          summary: `${shortPath(file.path)} is not valid JSON.`,
          concerns: [String(err)],
          model: "policy",
          ms: Date.now() - started,
        };
      }
    }
  }
  const protectedFiles = input.files.filter((f) => input.protectedPatterns.some((p) => p.test(f.path)));

  const diff = renderDiff(input.files);
  const prompt = [
    `Agent: ${input.agent}`,
    `Task: ${input.task}`,
    input.context ? `Agent notes:\n${input.context}` : "",
    `Diff:\n${diff}`,
  ]
    .filter(Boolean)
    .join("\n\n");

  // A burst of pushes can hit capacity limits: retry, alternating models,
  // before giving up.
  let modelReview: Omit<Review, "ms"> | null = null;
  for (let round = 0; round < 3 && !modelReview; round++) {
    for (const model of MODELS) {
      try {
        modelReview = await askModel(ai, model, prompt);
        break;
      } catch (err) {
        console.warn(JSON.stringify({ msg: "review model failed", model, round, error: String(err) }));
        await new Promise((resolve) => setTimeout(resolve, 600 * (round + 1)));
      }
    }
  }
  if (!modelReview) {
    // Never approve without a review: a human takes over.
    modelReview = {
      verdict: "escalate",
      summary: "The review model was unavailable, so a human needs to review this change.",
      concerns: [],
      model: "none",
    };
  }
  if (protectedFiles.length > 0 && modelReview.verdict === "approve") {
    modelReview = {
      ...modelReview,
      verdict: "escalate",
      concerns: [`Protected path: ${protectedFiles.map((f) => shortPath(f.path)).join(", ")} needs a human approval.`, ...modelReview.concerns],
    };
  }
  return { ...modelReview, ms: Date.now() - started };
}

async function askModel(ai: Ai, model: string, prompt: string): Promise<Omit<Review, "ms">> {
  let raw: unknown;
  if (model.startsWith("@cf/openai/gpt-oss")) {
    raw = await ai.run(model as keyof AiModels, {
      instructions: INSTRUCTIONS,
      input: prompt,
      reasoning: { effort: "low" },
    } as never);
  } else {
    raw = await ai.run(model as keyof AiModels, {
      messages: [
        { role: "system", content: INSTRUCTIONS },
        { role: "user", content: prompt },
      ],
      max_tokens: 400,
    } as never);
  }
  const text = extractText(raw);
  const json = text.match(/\{[\s\S]*\}/);
  if (!json) throw new Error(`no JSON in model reply: ${text.slice(0, 200)}`);
  const parsed = JSON.parse(json[0]) as { verdict?: string; summary?: string; concerns?: unknown };
  const verdict: Verdict =
    parsed.verdict === "approve" || parsed.verdict === "request_changes" || parsed.verdict === "escalate" ? parsed.verdict : "escalate";
  return {
    verdict,
    summary: String(parsed.summary ?? "").slice(0, 400) || "No summary.",
    concerns: Array.isArray(parsed.concerns) ? parsed.concerns.map((c) => String(c).slice(0, 300)).slice(0, 5) : [],
    model: model.replace("@cf/", ""),
  };
}

// extractText reads the assistant text from either a Responses-style result
// (gpt-oss) or a chat-style result ({ response }).
export function extractText(raw: unknown): string {
  if (typeof raw === "string") return raw;
  const r = raw as Record<string, unknown>;
  if (typeof r.response === "string") return r.response;
  if (typeof r.output_text === "string") return r.output_text;
  const parts: string[] = [];
  for (const item of (r.output as Array<Record<string, unknown>> | undefined) ?? []) {
    if (item.type !== "message") continue;
    for (const c of (item.content as Array<Record<string, unknown>> | undefined) ?? []) {
      if (typeof c.text === "string") parts.push(c.text);
    }
  }
  return parts.join("\n");
}

function renderDiff(files: ReviewFile[]): string {
  let out = "";
  for (const file of files) {
    const chunk =
      file.before === null && file.after === null
        ? ""
        : unifiedDiff(shortPath(file.path), file.before ?? "", file.after ?? "");
    if (out.length + chunk.length > MAX_DIFF_CHARS) {
      out += `\n… diff truncated (${files.length} files)`;
      break;
    }
    out += chunk + "\n";
  }
  return out;
}

function shortPath(path: string): string {
  return path.split("/").slice(3).join("/") || path;
}
