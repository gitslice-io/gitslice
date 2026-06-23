import type { ChangesetStack } from "../../api/types";
import { StackStatusBadge } from "../stackPageUtils";
import { Metadata } from "./Metadata";
import { formatCommit, formatTimestamp } from "../stackPageUtils";
import { SliceNotice } from "../../components/slices/SlicePageParts";

export function StackMetadataPanel({ stack }: { stack: ChangesetStack }) {
  return (
    <div className="rounded-lg border border-slate-200 bg-white p-5 shadow-sm shadow-slate-200/50">
      <h2 className="text-sm font-semibold text-zinc-950">Dependency metadata</h2>
      <dl className="mt-4 grid grid-cols-1 gap-4">
        <Metadata label="Status" value={<StackStatusBadge status={stack.status} />} />
        <Metadata label="Dependency id" value={stack.id || "not returned"} />
        <Metadata label="Base" value={formatCommit(stack.baseCommitId)} />
        <Metadata label="Target" value={stack.targetRef || "not returned"} />
        <Metadata label="Created" value={formatTimestamp(stack.createdAt)} />
        <Metadata label="Updated" value={formatTimestamp(stack.updatedAt)} />
      </dl>
    </div>
  );
}