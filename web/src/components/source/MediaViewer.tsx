import { useObjectUrl } from "../../lib/bytes";
import { extensionOf, type BinaryKind } from "./sourceUtils";

// PDF, audio and video files, shown by the browser's own players from a blob:
// URL made from the file's bytes.

const MIME_TYPES: Record<string, string> = {
  aac: "audio/aac",
  flac: "audio/flac",
  m4a: "audio/mp4",
  m4v: "video/mp4",
  mov: "video/quicktime",
  mp3: "audio/mpeg",
  mp4: "video/mp4",
  oga: "audio/ogg",
  ogg: "audio/ogg",
  ogv: "video/ogg",
  opus: "audio/ogg",
  pdf: "application/pdf",
  wav: "audio/wav",
  webm: "video/webm"
};

export function MediaViewer({ data, kind, path }: { data: string; kind: Extract<BinaryKind, "audio" | "pdf" | "video">; path: string }) {
  const mime = MIME_TYPES[extensionOf(path)] ?? "application/octet-stream";
  const url = useObjectUrl(data, mime);
  const filename = path.split("/").pop() || "file";

  if (!data) {
    return <p className="p-4 text-sm text-slate-600 dark:text-zinc-400">This file is empty.</p>;
  }
  if (!url) {
    return <p className="p-4 text-sm text-slate-500 dark:text-zinc-400" role="status">Loading…</p>;
  }

  return (
    <div className="bg-slate-100 dark:bg-zinc-950">
      {kind === "pdf" ? (
        <iframe className="block h-[80dvh] w-full border-0 bg-white" src={url} title={`Preview of ${filename}`} />
      ) : kind === "video" ? (
        <div className="grid place-items-center p-4">
          <video aria-label={`Video ${filename}`} className="max-h-[72dvh] max-w-full" controls preload="metadata" src={url} />
        </div>
      ) : (
        <div className="grid place-items-center p-8">
          <audio aria-label={`Audio ${filename}`} className="w-full max-w-xl" controls preload="metadata" src={url} />
        </div>
      )}
      <div className="flex flex-wrap items-center justify-end gap-3 border-t border-slate-200 bg-white px-4 py-2 text-xs dark:border-zinc-800 dark:bg-zinc-900">
        <a className="font-medium text-sky-700 hover:underline dark:text-sky-300" href={url} rel="noopener noreferrer" target="_blank">
          Open in a new tab
        </a>
        <a className="font-medium text-sky-700 hover:underline dark:text-sky-300" download={filename} href={url}>
          Download
        </a>
      </div>
    </div>
  );
}
