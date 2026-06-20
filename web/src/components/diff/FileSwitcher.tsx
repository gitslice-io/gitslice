import { useEffect, useState } from "react";

import { cn } from "../../lib/cn";
import { Button, Surface, buttonClassName } from "../ui";
import { ChangedFilesTree } from "./ChangedFilesTree";
import type { DiffFile } from "./parseDiff";

interface MobileFileSwitcherProps {
  activeId?: string;
  files: DiffFile[];
  onOpenPicker(): void;
  onSelectFile(id: string): void;
}

interface FilePickerSheetProps {
  activeId?: string;
  files: DiffFile[];
  onClose(): void;
  onSelectFile(id: string): void;
  totalAdditions: number;
  totalDeletions: number;
}

export function MobileFileSwitcher({
  activeId,
  files,
  onOpenPicker,
  onSelectFile
}: MobileFileSwitcherProps) {
  const active = activeFileDetails(files, activeId);

  if (!active) {
    return null;
  }

  const previousFile = active.index > 0 ? files[active.index - 1] : undefined;
  const nextFile =
    active.index < files.length - 1 ? files[active.index + 1] : undefined;

  return (
    <div className="flex min-w-0 flex-1 items-center gap-1.5 lg:hidden">
      <button
        aria-label="Previous file"
        className={fileNavButtonClass(!previousFile)}
        disabled={!previousFile}
        onClick={() => {
          if (previousFile) {
            onSelectFile(previousFile.id);
          }
        }}
        title="Previous file"
        type="button"
      >
        <span aria-hidden="true">{"<"}</span>
      </button>
      <Button
        aria-label={`Open file picker, ${active.basename}, ${
          active.index + 1
        } of ${files.length}`}
        className="grid h-9 min-w-0 flex-1 grid-cols-[minmax(0,1fr)_auto] items-center gap-2 px-2.5 text-left active:scale-[0.98]"
        onClick={onOpenPicker}
        size="sm"
        type="button"
        variant="secondary"
      >
        <span className="min-w-0 truncate font-mono text-xs font-semibold text-on-surface">
          {active.basename}
        </span>
        <span className="shrink-0 rounded-sm bg-surface-container-lowest px-1.5 py-0.5 font-mono text-[11px] text-on-surface-variant">
          {active.index + 1} / {files.length}
        </span>
      </Button>
      <button
        aria-label="Next file"
        className={fileNavButtonClass(!nextFile)}
        disabled={!nextFile}
        onClick={() => {
          if (nextFile) {
            onSelectFile(nextFile.id);
          }
        }}
        title="Next file"
        type="button"
      >
        <span aria-hidden="true">{">"}</span>
      </button>
    </div>
  );
}

export function FilePickerSheet({
  activeId,
  files,
  onClose,
  onSelectFile,
  totalAdditions,
  totalDeletions
}: FilePickerSheetProps) {
  const [entered, setEntered] = useState(false);

  useEffect(() => {
    const previousOverflow = document.body.style.overflow;
    document.body.style.overflow = "hidden";

    const handleKeyDown = (event: KeyboardEvent) => {
      if (event.key === "Escape") {
        onClose();
      }
    };

    document.addEventListener("keydown", handleKeyDown);

    return () => {
      document.body.style.overflow = previousOverflow;
      document.removeEventListener("keydown", handleKeyDown);
    };
  }, [onClose]);

  useEffect(() => {
    const frame = window.requestAnimationFrame(() => setEntered(true));
    return () => window.cancelAnimationFrame(frame);
  }, []);

  useEffect(() => {
    const desktopQuery = window.matchMedia("(min-width: 1024px)");
    const closeOnDesktop = () => {
      if (desktopQuery.matches) {
        onClose();
      }
    };

    closeOnDesktop();
    desktopQuery.addEventListener("change", closeOnDesktop);

    return () => {
      desktopQuery.removeEventListener("change", closeOnDesktop);
    };
  }, [onClose]);

  const handleSelectFile = (id: string) => {
    onSelectFile(id);
    onClose();
  };

  return (
    <div className="fixed inset-0 z-40 lg:hidden">
      <button
        aria-label="Close file picker"
        className={cn(
          "absolute inset-0 bg-on-surface/30 transition-opacity duration-200",
          entered ? "opacity-100" : "opacity-0"
        )}
        onClick={onClose}
        type="button"
      />
      <Surface
        aria-labelledby="diff-file-picker-title"
        aria-modal="true"
        className={cn(
          "absolute inset-x-0 bottom-0 z-10 flex max-h-[80dvh] min-h-0 transform flex-col overflow-hidden rounded-b-none rounded-t-sm shadow-[0_24px_60px_rgba(33,29,23,0.06)] transition-transform duration-200",
          entered ? "translate-y-0" : "translate-y-full"
        )}
        level="lowest"
        role="dialog"
      >
        <Surface
          className="flex items-center justify-between gap-3 rounded-none px-4 py-3"
          level="low"
        >
          <div className="flex min-w-0 items-baseline gap-2">
            <h2
              className="truncate text-sm font-semibold text-on-surface"
              id="diff-file-picker-title"
            >
              Files changed
            </h2>
            <div className="flex shrink-0 gap-1.5 font-mono text-xs">
              <span className="text-emerald-700">+{totalAdditions}</span>
              <span className="text-on-surface-muted">/</span>
              <span className="text-rose-700">-{totalDeletions}</span>
            </div>
          </div>
          <Button
            aria-label="Close file picker"
            className="h-8 w-8 shrink-0 p-0 text-sm active:scale-[0.98]"
            onClick={onClose}
            size="sm"
            type="button"
            variant="secondary"
          >
            x
          </Button>
        </Surface>
        <div className="min-h-0 flex-1 overflow-y-auto px-3 py-3">
          <ChangedFilesTree
            activeId={activeId}
            files={files}
            onSelect={handleSelectFile}
            scrollClassName="max-h-none overflow-visible"
          />
        </div>
      </Surface>
    </div>
  );
}

function fileNavButtonClass(disabled: boolean) {
  return buttonClassName({
    className: cn(
      "h-9 w-9 shrink-0 p-0 text-xs active:scale-[0.98]",
      disabled ? "cursor-not-allowed opacity-40" : "hover:text-primary"
    ),
    size: "sm",
    variant: "secondary"
  });
}

function activeFileDetails(files: DiffFile[], activeId?: string) {
  if (files.length === 0) {
    return undefined;
  }

  const activeIndex = files.findIndex((file) => file.id === activeId);
  const index = activeIndex >= 0 ? activeIndex : 0;
  const file = files[index];

  return {
    basename: basename(file.path),
    index
  };
}

function basename(path: string) {
  const parts = path.split("/").filter(Boolean);
  return parts[parts.length - 1] || path || "unknown";
}
