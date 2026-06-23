import type { ChangesetStack } from "../../api/types";
import { normalizeRepositoryPath } from "../../components/source/sourceUtils";
import {
  isTerminalChangesetStatus
} from "../stackPageUtils";

const inputClass =
  "h-11 rounded-md border border-slate-300 bg-white px-3 text-sm text-zinc-950 outline-none transition placeholder:text-slate-400 focus:border-zinc-500 focus:ring-2 focus:ring-zinc-200";

export { inputClass };

export function hasInProgressEntry(stack?: ChangesetStack) {
  return Boolean(
    stack?.entries?.some((entry) => {
      const status = entry.changeset?.status;
      return status === "pending_publish" || !isTerminalChangesetStatus(status);
    })
  );
}

export function utf8ToBase64(value: string) {
  const bytes = new TextEncoder().encode(value);
  let binary = "";
  for (let index = 0; index < bytes.length; index += 1) {
    binary += String.fromCharCode(bytes[index]);
  }
  return window.btoa(binary);
}

export function base64ToUtf8(value: string) {
  const binary = window.atob(value);
  const bytes = new Uint8Array(binary.length);
  for (let index = 0; index < binary.length; index += 1) {
    bytes[index] = binary.charCodeAt(index);
  }
  return new TextDecoder().decode(bytes);
}

export function parentRepositoryPath(value: string) {
  const normalized = normalizeRepositoryPath(value);
  if (normalized === "/") {
    return "/";
  }
  const parts = normalized.replace(/^\/+/, "").split("/");
  parts.pop();
  return parts.length ? `/${parts.join("/")}` : "/";
}