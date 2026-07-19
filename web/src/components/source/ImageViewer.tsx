import { useEffect, useState } from "react";

const IMAGE_MIME_TYPES: Record<string, string> = {
  avif: "image/avif",
  bmp: "image/bmp",
  gif: "image/gif",
  ico: "image/x-icon",
  jpeg: "image/jpeg",
  jpg: "image/jpeg",
  png: "image/png",
  svg: "image/svg+xml",
  webp: "image/webp"
};

type ImageStatus = "empty" | "error" | "loading" | "ready";

interface ImageViewerProps {
  data: string;
  path: string;
}

export function imageMimeTypeFromPath(path: string) {
  const basename = path.replace(/\\/g, "/").split("/").pop() ?? "";
  const extension = basename.includes(".")
    ? basename.slice(basename.lastIndexOf(".") + 1).toLowerCase()
    : "";

  return IMAGE_MIME_TYPES[extension];
}

export function ImageViewer({ data, path }: ImageViewerProps) {
  const mimeType = imageMimeTypeFromPath(path);
  const [status, setStatus] = useState<ImageStatus>(data ? "loading" : "empty");
  const filename = path.replace(/\\/g, "/").split("/").pop() || "image";

  useEffect(() => {
    setStatus(data ? "loading" : "empty");
  }, [data, path]);

  if (!mimeType) {
    return null;
  }

  return (
    <div className="overflow-hidden rounded-lg border border-slate-200 bg-white dark:border-zinc-800 dark:bg-zinc-900">
      <div className="flex flex-wrap items-center justify-between gap-3 border-b border-slate-200 bg-slate-50 px-4 py-3 text-xs text-slate-500 dark:border-zinc-800 dark:bg-zinc-950 dark:text-zinc-400">
        <div className="min-w-0 truncate font-mono text-slate-600 dark:text-zinc-400">
          {path}
        </div>
        <span>{mimeType}</span>
      </div>
      <div className="relative grid min-h-64 place-items-center overflow-auto bg-slate-100 p-6 dark:bg-zinc-950 sm:p-10">
        {data && status !== "error" ? (
          <img
            alt={`Preview of ${filename}`}
            className={`max-h-[72dvh] max-w-full object-contain transition-opacity duration-200 ${
              status === "ready" ? "opacity-100" : "opacity-0"
            }`}
            onError={() => setStatus("error")}
            onLoad={() => setStatus("ready")}
            src={`data:${mimeType};base64,${data}`}
          />
        ) : null}
        {status === "loading" ? (
          <div
            aria-label="Loading image preview"
            className="absolute inset-6 animate-pulse rounded-md bg-slate-200 dark:bg-zinc-800 sm:inset-10"
            role="status"
          />
        ) : null}
        {status === "empty" ? (
          <p className="max-w-sm text-center text-sm leading-6 text-slate-600 dark:text-zinc-400">
            This image file is empty.
          </p>
        ) : null}
        {status === "error" ? (
          <p className="max-w-sm text-center text-sm leading-6 text-rose-700 dark:text-rose-300">
            The image could not be rendered. Its contents may be invalid or use
            an unsupported encoding.
          </p>
        ) : null}
      </div>
    </div>
  );
}
