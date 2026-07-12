import { cn } from "../../lib/cn";
import { languageFromPath } from "./sourceUtils";

interface IconSpec {
  classes: string;
  glyph?: "config" | "image" | "page";
  label?: string;
}

const toneClasses = {
  amber: "border-amber-300 dark:border-amber-800/70 bg-amber-50 dark:bg-amber-950/30 text-amber-800 dark:text-amber-200",
  blue: "border-blue-300 bg-blue-50 text-blue-700",
  cyan: "border-cyan-300 bg-cyan-50 text-cyan-700",
  emerald: "border-emerald-300 dark:border-emerald-800/70 bg-emerald-50 dark:bg-emerald-950/30 text-emerald-700 dark:text-emerald-300",
  indigo: "border-indigo-300 bg-indigo-50 text-indigo-700",
  orange: "border-orange-300 bg-orange-50 text-orange-700",
  pink: "border-pink-300 bg-pink-50 text-pink-700",
  rose: "border-rose-300 dark:border-rose-800/70 bg-rose-50 dark:bg-rose-950/30 text-rose-700 dark:text-rose-300",
  sky: "border-sky-300 dark:border-sky-800/70 bg-sky-50 dark:bg-sky-950/30 text-sky-700 dark:text-sky-300",
  slate: "border-slate-300 dark:border-zinc-700 bg-slate-50 dark:bg-zinc-950 text-slate-700 dark:text-zinc-300",
  teal: "border-teal-300 bg-teal-50 text-teal-700",
  violet: "border-violet-300 bg-violet-50 text-violet-700"
};

const configIcon: IconSpec = {
  classes: toneClasses.slate,
  glyph: "config"
};

const pageIcon: IconSpec = {
  classes: toneClasses.slate,
  glyph: "page"
};

const basenameIcons: Partial<Record<string, IconSpec>> = {
  "go.mod": { classes: toneClasses.cyan, label: "MOD" },
  "go.sum": { classes: toneClasses.cyan, label: "SUM" }
};

const extensionIcons: Partial<Record<string, IconSpec>> = {
  avif: { classes: toneClasses.emerald, glyph: "image" },
  gif: { classes: toneClasses.emerald, glyph: "image" },
  ico: { classes: toneClasses.emerald, glyph: "image" },
  jpeg: { classes: toneClasses.emerald, glyph: "image" },
  jpg: { classes: toneClasses.emerald, glyph: "image" },
  png: { classes: toneClasses.emerald, glyph: "image" },
  svg: { classes: toneClasses.emerald, glyph: "image" },
  webp: { classes: toneClasses.emerald, glyph: "image" }
};

const languageIcons: Partial<Record<string, IconSpec>> = {
  bash: { classes: toneClasses.emerald, label: ">_" },
  css: { classes: toneClasses.sky, label: "CSS" },
  go: { classes: toneClasses.cyan, label: "GO" },
  html: { classes: toneClasses.orange, label: "<>" },
  java: { classes: toneClasses.rose, label: "JV" },
  js: { classes: toneClasses.amber, label: "JS" },
  json: { classes: toneClasses.amber, label: "{}" },
  jsx: { classes: toneClasses.amber, label: "JSX" },
  kotlin: { classes: toneClasses.violet, label: "KT" },
  markdown: { classes: toneClasses.slate, label: "MD" },
  proto: { classes: toneClasses.indigo, label: "PB" },
  python: { classes: toneClasses.blue, label: "PY" },
  rust: { classes: toneClasses.orange, label: "RS" },
  scss: { classes: toneClasses.pink, label: "SCSS" },
  sql: { classes: toneClasses.teal, label: "SQL" },
  toml: { classes: toneClasses.violet, label: "TOML" },
  ts: { classes: toneClasses.blue, label: "TS" },
  tsx: { classes: toneClasses.blue, label: "TSX" },
  xml: { classes: toneClasses.orange, label: "XML" },
  yaml: { classes: toneClasses.violet, label: "YML" }
};

export function FileTypeIcon({
  name,
  className
}: {
  name: string;
  className?: string;
}): JSX.Element {
  const icon = categoryForName(name);
  const longLabel = Boolean(icon.label && icon.label.length > 3);

  return (
    <span
      aria-hidden="true"
      className={cn(
        "inline-flex h-4 w-4 shrink-0 items-center justify-center overflow-hidden rounded-[3px] border px-px font-mono font-bold leading-none tracking-normal",
        longLabel && "w-5",
        icon.classes,
        className
      )}
    >
      {icon.glyph === "image" ? <ImageMark /> : null}
      {icon.glyph === "config" ? <ConfigMark /> : null}
      {icon.glyph === "page" ? <PageMark /> : null}
      {icon.label ? (
        <span className={cn(labelClass(icon.label), "leading-none")}>
          {icon.label}
        </span>
      ) : null}
    </span>
  );
}

function categoryForName(name: string): IconSpec {
  const basename = basenameForPath(name);
  const lowerBasename = basename.toLowerCase();
  const basenameIcon = basenameIcons[lowerBasename];

  if (basenameIcon) {
    return basenameIcon;
  }

  if (isConfigName(lowerBasename)) {
    return configIcon;
  }

  const extension = extensionForBasename(lowerBasename);
  const extensionIcon = extensionIcons[extension];

  if (extensionIcon) {
    return extensionIcon;
  }

  const languageIcon = languageIcons[languageFromPath(basename)];

  if (languageIcon) {
    return languageIcon;
  }

  if (extension) {
    return {
      classes: toneClasses.slate,
      label: extension.slice(0, 3).toUpperCase()
    };
  }

  return pageIcon;
}

function basenameForPath(path: string) {
  const normalized = path.replace(/\\/g, "/").replace(/\/+$/g, "");
  const parts = normalized.split("/");
  return parts[parts.length - 1] || path;
}

function extensionForBasename(basename: string) {
  const lastDot = basename.lastIndexOf(".");

  if (lastDot <= 0 || lastDot === basename.length - 1) {
    return "";
  }

  return basename.slice(lastDot + 1);
}

function isConfigName(basename: string) {
  return (
    basename === ".dockerignore" ||
    basename === ".gitignore" ||
    basename === "dockerfile" ||
    basename === "package-lock.json" ||
    basename.endsWith(".lock") ||
    basename.startsWith(".env") ||
    basename.includes(".config.")
  );
}

function labelClass(label: string) {
  if (label.length > 3) {
    return "text-[6px]";
  }

  if (label === "{}" || label === "<>" || label === ">_") {
    return "text-[8px]";
  }

  return "text-[7px]";
}

function ImageMark() {
  return (
    <svg
      className="h-3 w-3"
      fill="none"
      focusable="false"
      stroke="currentColor"
      strokeLinecap="round"
      strokeLinejoin="round"
      strokeWidth="1.4"
      viewBox="0 0 16 16"
    >
      <path d="M3.5 4.5h9v7h-9z" />
      <path d="m4.5 10.5 2.5-3 2 2 1.5-1.5 1.5 2.5" />
      <circle cx="10.4" cy="6.3" fill="currentColor" r="0.8" stroke="none" />
    </svg>
  );
}

function ConfigMark() {
  return (
    <svg
      className="h-3 w-3"
      fill="none"
      focusable="false"
      stroke="currentColor"
      strokeLinecap="round"
      strokeLinejoin="round"
      strokeWidth="1.4"
      viewBox="0 0 16 16"
    >
      <path d="M8 2.5v2M8 11.5v2M2.5 8h2M11.5 8h2M4.1 4.1l1.4 1.4M10.5 10.5l1.4 1.4M11.9 4.1l-1.4 1.4M5.5 10.5l-1.4 1.4" />
      <circle cx="8" cy="8" r="2.4" />
    </svg>
  );
}

function PageMark() {
  return (
    <svg
      className="h-3 w-2.5"
      fill="none"
      focusable="false"
      stroke="currentColor"
      strokeLinecap="round"
      strokeLinejoin="round"
      strokeWidth="1.25"
      viewBox="0 0 12 14"
    >
      <path d="M2.5 1.5h4.25l2.75 2.75v8.25h-7z" />
      <path d="M6.75 1.5v2.75H9.5M4 7h4M4 9.5h3" />
    </svg>
  );
}
