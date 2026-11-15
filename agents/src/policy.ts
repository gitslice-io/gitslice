// Pure security, path-filtering, bounded-state, and alarm-scheduling policies
// shared across the agents Worker and unit-tested directly under Node.

import { timingSafeEqual } from "node:crypto";
import type { Session, Signal } from "./types.ts";

const encoder = new TextEncoder();

export const MIN_API_KEY_LENGTH = 16;
export const MAX_HUB_SESSIONS = 500;
export const MAX_NATIVE_QUEUED = 500;
export const NATIVE_QUEUE_TTL_MS = 10 * 60_000;
export const MAX_RETIRED_BASELINES = 200;

export const FIX_AFTER_MS = 15_000;
export const STALL_MS = 90_000;
export const WATCHDOG_EVERY_MS = 30_000;
export const RETRY_LIMIT = 3;
export const REREVIEW_AFTER_MS = 20_000;
export const NATIVE_POLL_BUSY_MS = 2_000;
export const NATIVE_POLL_IDLE_MS = 20_000;
export const NATIVE_BUSY_WINDOW_MS = 90_000;
export const MAX_NATIVE_SESSION_WATCH_MS = 15 * 60_000;
export const MAX_HUMAN_POLL_WINDOW_MS = 15 * 60_000;

export const PROCESSING_STATUSES: ReadonlySet<string> = new Set([
  "pushed",
  "reviewing",
  "approved",
  "landing",
  "merging",
]);

export const TERMINAL_STATUSES: ReadonlySet<string> = new Set(["landed", "failed", "conflict"]);

async function sha256Bytes(value: string): Promise<Uint8Array> {
  return new Uint8Array(await crypto.subtle.digest("SHA-256", encoder.encode(value)));
}

function constantTimeEqual(a: Uint8Array, b: Uint8Array): boolean {
  if (a.byteLength !== b.byteLength) return false;
  const subtle = crypto.subtle as unknown as {
    timingSafeEqual?: (left: BufferSource, right: BufferSource) => boolean;
  };
  if (typeof subtle.timingSafeEqual === "function") {
    return subtle.timingSafeEqual(a, b);
  }
  return timingSafeEqual(a, b);
}

// authorized compares the Bearer token in constant time over SHA-256 digests
// against every configured key in AGENTS_API_KEY (>= 16 chars), without
// short-circuiting on length or prefix.
export async function authorized(
  request: Pick<Request, "headers">,
  env: { AGENTS_API_KEY?: string },
): Promise<boolean> {
  const header = request.headers.get("authorization") ?? "";
  if (!header.startsWith("Bearer ")) return false;
  const presented = header.slice("Bearer ".length).trim();
  const keys = (env.AGENTS_API_KEY || "")
    .split(",")
    .map((k) => k.trim())
    .filter((k) => k.length >= MIN_API_KEY_LENGTH);
  if (keys.length === 0) return false;

  const presentedValid = presented.length >= MIN_API_KEY_LENGTH;
  const presentedDigest = await sha256Bytes(presented);
  let matched = false;
  for (const key of keys) {
    const keyDigest = await sha256Bytes(key);
    const eq = constantTimeEqual(presentedDigest, keyDigest);
    if (eq && presentedValid) matched = true;
  }
  return matched;
}

export function readAuthRequired(env: { AGENTS_READ_AUTH?: string }): boolean {
  const raw = (env.AGENTS_READ_AUTH ?? "").trim().toLowerCase();
  return Boolean(raw) && raw !== "0" && raw !== "false" && raw !== "off";
}

export function corsHeaders(
  request: Pick<Request, "url" | "headers">,
  env: { CORS_ALLOW_ORIGIN?: string; GITSLICE_WEB?: string },
): Record<string, string> {
  const configured = (env.CORS_ALLOW_ORIGIN ?? "").trim();
  if (configured === "*") {
    return { "access-control-allow-origin": "*" };
  }
  const origin = request.headers.get("origin");
  if (!origin) return {};

  const allowed = new Set<string>();
  try {
    allowed.add(new URL(request.url).origin);
  } catch {
    // ignore malformed request URL
  }
  if (configured) {
    for (const entry of configured.split(",").map((s) => s.trim()).filter(Boolean)) {
      allowed.add(entry);
    }
  } else if (env.GITSLICE_WEB) {
    try {
      allowed.add(new URL(env.GITSLICE_WEB).origin);
    } catch {
      // ignore invalid GITSLICE_WEB
    }
  }
  if (allowed.has(origin)) {
    return { "access-control-allow-origin": origin, vary: "Origin" };
  }
  return { vary: "Origin" };
}

export function redactSignals(signals: readonly Signal[]): Signal[] {
  return signals.map((sig) => {
    if (sig.data && typeof sig.data === "object" && !Array.isArray(sig.data) && "hunks" in sig.data) {
      const rest = { ...(sig.data as Record<string, unknown>) };
      delete rest.hunks;
      return { ...sig, data: rest as Signal["data"] };
    }
    return sig;
  });
}

export function redactSession(session: Session): Session {
  return {
    ...session,
    signals: redactSignals(session.signals),
  };
}

function globToRegExp(pattern: string): RegExp {
  let out = "";
  for (let i = 0; i < pattern.length; i++) {
    const ch = pattern[i];
    if (ch === "*") {
      if (pattern[i + 1] === "*") {
        out += ".*";
        i++;
      } else {
        out += ".*";
      }
    } else if (".+?^${}()|[]\\".includes(ch)) {
      out += "\\" + ch;
    } else {
      out += ch;
    }
  }
  return new RegExp(out);
}

// HARDCODED_PROTECTED_PATTERNS ensures `.gitslice/` configuration, check
// definitions, and trigger scripts at root (`/.gitslice/**`) or any nested
// folder (`**/.gitslice/**`) always escalate to a human and are NEVER
// auto-approved by LLM review.
const HARDCODED_PROTECTED_PATTERNS: readonly RegExp[] = [
  /(?:^|\/)\.gitslice(?:\/|$)/,
  globToRegExp("/.gitslice/**"),
  globToRegExp("**/.gitslice/**"),
];

export function protectedPatterns(env: { PROTECTED_PATHS?: string }): RegExp[] {
  const custom = (env.PROTECTED_PATHS || "")
    .split(",")
    .map((p) => p.trim())
    .filter(Boolean)
    .map(globToRegExp);
  return [...HARDCODED_PROTECTED_PATTERNS, ...custom];
}

// normalizeRepoRelPath validates a repository-relative path (`account/slice/...`),
// rejecting traversal segments (`..`, `.`), backslashes, NUL bytes, `.git`, and
// optionally `.gitslice`.
export function normalizeSlicePath(
  rawPath: string,
  account: string,
  slice: string,
  opts: { allowDotGitslice?: boolean; leadingSlash?: boolean } = {},
): string | null {
  if (!rawPath || rawPath.includes("\0") || rawPath.includes("\\")) return null;
  const trimmed = rawPath.trim().replace(/^\/+/, "").replace(/\/+$/, "");
  if (!trimmed) return null;
  const parts = trimmed.split("/");
  for (const part of parts) {
    if (!part || part === "." || part === ".." || part === ".git") return null;
    if (opts.allowDotGitslice === false && part === ".gitslice") return null;
  }
  const prefix = `${account}/${slice}/`;
  if (!trimmed.startsWith(prefix) || trimmed.length <= prefix.length) return null;
  return opts.leadingSlash ? `/${trimmed}` : trimmed;
}

export function filterSlicePaths(
  paths: readonly string[],
  account: string,
  slice: string,
  opts: { allowDotGitslice?: boolean; leadingSlash?: boolean } = {},
): string[] {
  const out: string[] = [];
  const seen = new Set<string>();
  for (const raw of paths) {
    const normalized = normalizeSlicePath(raw, account, slice, opts);
    if (normalized && !seen.has(normalized)) {
      seen.add(normalized);
      out.push(normalized);
    }
  }
  return out;
}

export function isUnreviewedEscalation(s: Session): boolean {
  return s.status === "needs-human" && s.review?.model === "none";
}

export interface AlarmScheduleInput {
  now: number;
  sessions: Iterable<Session>;
  nativeEnabled: boolean;
  nativeActiveAt: number;
  openWebSockets: number;
}

// computeNextAlarmMs returns the next timestamp (ms) the Hub DO should wake at,
// or null when the Hub has no pending work and should stay asleep (scaling DO,
// Cloud Run, and Neon to zero).
export function computeNextAlarmMs(input: AlarmScheduleInput): number | null {
  const { now, sessions, nativeEnabled, nativeActiveAt, openWebSockets } = input;
  let next = Infinity;
  let hasActiveNative = false;

  for (const s of sessions) {
    if (s.supersededBy) continue;

    if (s.fixAt && !s.fixedBy && s.status === "changes-requested") {
      next = Math.min(next, s.fixAt);
    }

    if (PROCESSING_STATUSES.has(s.status) && s.lastCommit && (s.retries ?? 0) <= RETRY_LIMIT) {
      next = Math.min(next, now + WATCHDOG_EVERY_MS);
    }

    if (s.status === "needs-human") {
      if (isUnreviewedEscalation(s)) {
        if (s.lastCommit && (s.retries ?? 0) < RETRY_LIMIT) {
          next = Math.min(next, now + REREVIEW_AFTER_MS);
        }
      } else if (!s.native && s.changesetId && s.patchsetId) {
        const age = now - s.updatedAt;
        if (age < MAX_HUMAN_POLL_WINDOW_MS) {
          const delay = age < 120_000 ? 5_000 : 30_000;
          next = Math.min(next, now + delay);
        }
      }
    }

    if (
      s.native &&
      !TERMINAL_STATUSES.has(s.status) &&
      now - s.updatedAt < MAX_NATIVE_SESSION_WATCH_MS
    ) {
      hasActiveNative = true;
    }
  }

  if (nativeEnabled) {
    const inBusyWindow = nativeActiveAt > 0 && now - nativeActiveAt < NATIVE_BUSY_WINDOW_MS;
    if (inBusyWindow || hasActiveNative) {
      next = Math.min(next, now + NATIVE_POLL_BUSY_MS);
    } else if (openWebSockets > 0) {
      next = Math.min(next, now + NATIVE_POLL_IDLE_MS);
    }
    // When idle (!inBusyWindow && !hasActiveNative && openWebSockets === 0),
    // schedule NO native poll alarm so the DO and backend scale to zero.
  }

  if (next === Infinity) return null;
  return Math.max(next, now + 250);
}

// pruneSessionsMap bounds in-memory and per-key DO session count, evicting
// superseded and terminal sessions oldest-first and returning evicted sessions.
export function pruneSessionsMap(sessions: Map<string, Session>, maxSessions = MAX_HUB_SESSIONS): Session[] {
  if (sessions.size <= maxSessions) return [];
  const need = sessions.size - maxSessions;
  const candidates = [...sessions.values()].sort((a, b) => {
    const aInactive = a.supersededBy || TERMINAL_STATUSES.has(a.status) ? 0 : 1;
    const bInactive = b.supersededBy || TERMINAL_STATUSES.has(b.status) ? 0 : 1;
    if (aInactive !== bInactive) return aInactive - bInactive;
    return a.updatedAt - b.updatedAt;
  });
  const evicted = candidates.slice(0, need);
  for (const s of evicted) sessions.delete(s.id);
  return evicted;
}

export function pruneNativeQueued(
  nativeQueued: Map<string, number>,
  now: number,
  ttlMs = NATIVE_QUEUE_TTL_MS,
  maxEntries = MAX_NATIVE_QUEUED,
): void {
  for (const [key, queuedAt] of nativeQueued) {
    if (now - queuedAt >= ttlMs) nativeQueued.delete(key);
  }
  while (nativeQueued.size > maxEntries) {
    const oldest = nativeQueued.keys().next().value;
    if (oldest === undefined) break;
    nativeQueued.delete(oldest);
  }
}

export function capRetiredList(
  retired: string[],
  activeBaselineRepo: string | undefined,
  maxEntries = MAX_RETIRED_BASELINES,
): string[] {
  if (retired.length <= maxEntries) return [];
  const count = retired.length - maxEntries;
  const overflow = retired.splice(0, count);
  return overflow.filter((name) => name !== activeBaselineRepo);
}
