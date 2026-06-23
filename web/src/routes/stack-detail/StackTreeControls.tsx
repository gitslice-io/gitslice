import type { ChangesetStack, ChangesetStackEntry } from "../../api/types";
import { StackMetadataPanel } from "./StackMetadataPanel";
import { AddPatchsetPanel } from "./AddPatchsetPanel";
import { AddEntryPanel } from "./AddEntryPanel";
import { MoveEntryPanel } from "./MoveEntryPanel";

export function StackTreeControls({
  entries,
  onError,
  onMessage,
  onRefresh,
  onSelect,
  selectedEntry,
  stack
}: {
  entries: ChangesetStackEntry[];
  onError(message: string): void;
  onMessage(message: string): void;
  onRefresh(): Promise<void>;
  onSelect(entryId: string): void;
  selectedEntry: ChangesetStackEntry | null;
  stack: ChangesetStack;
}) {
  return (
    <aside className="grid content-start gap-4">
      <StackMetadataPanel stack={stack} />
      <AddPatchsetPanel
        entries={entries}
        onError={onError}
        onMessage={onMessage}
        onRefresh={onRefresh}
        selectedEntry={selectedEntry}
        stack={stack}
      />
      <AddEntryPanel
        entries={entries}
        onError={onError}
        onMessage={onMessage}
        onRefresh={onRefresh}
        onSelect={onSelect}
        selectedEntry={selectedEntry}
        stackId={stack.id || ""}
      />
      <MoveEntryPanel
        entries={entries}
        onError={onError}
        onMessage={onMessage}
        onRefresh={onRefresh}
        onSelect={onSelect}
        selectedEntry={selectedEntry}
        stackId={stack.id || ""}
      />
    </aside>
  );
}