import { useEffect, useState } from "react";

import { isDarkTheme, renderDot, renderMermaid, svgDataUrl } from "../../lib/diagram";
import { extensionOf } from "./sourceUtils";

type State = { status: "loading" } | { status: "error"; message: string } | { status: "ready"; url: string };

export function diagramKindFromPath(path: string): "mermaid" | "dot" {
  const extension = extensionOf(path);
  return extension === "dot" || extension === "gv" ? "dot" : "mermaid";
}

// A rendered Mermaid or Graphviz diagram.
export function DiagramViewer({ path, source }: { path: string; source: string }) {
  const kind = diagramKindFromPath(path);
  const [state, setState] = useState<State>({ status: "loading" });
  const [fit, setFit] = useState(true);
  // An SVG in an <img> with a relative width fills its container, so the image is
  // sized to its own dimensions once they are known.
  const [width, setWidth] = useState(0);

  useEffect(() => {
    let active = true;
    setState({ status: "loading" });
    const render = kind === "dot" ? renderDot(source) : renderMermaid(source, isDarkTheme());
    render.then(
      (svg) => active && setState({ status: "ready", url: svgDataUrl(svg) }),
      (error) =>
        active &&
        setState({ status: "error", message: error instanceof Error ? error.message.split("\n").slice(0, 3).join(" ") : "render error" })
    );
    return () => {
      active = false;
    };
  }, [kind, source]);

  if (state.status === "error") {
    return (
      <div className="bg-amber-50 px-4 py-3 text-sm text-amber-800 dark:bg-amber-950/30 dark:text-amber-200" role="alert">
        The diagram could not be rendered: {state.message}. Switch to Raw to see the source.
      </div>
    );
  }

  return (
    <div>
      <div className="flex items-center justify-between gap-3 border-b border-slate-200 px-4 py-2 text-xs text-slate-500 dark:border-zinc-800 dark:text-zinc-400">
        <span>{kind === "dot" ? "Graphviz" : "Mermaid"} diagram</span>
        <label className="inline-flex items-center gap-2">
          <input checked={fit} onChange={(event) => setFit(event.target.checked)} type="checkbox" />
          Fit to width
        </label>
      </div>
      <div className="overflow-auto bg-white p-4">
        {state.status === "loading" ? (
          <p className="text-sm text-slate-500" role="status">
            Rendering…
          </p>
        ) : (
          <img
            alt={`Diagram of ${path.split("/").pop()}`}
            className="mx-auto h-auto"
            onLoad={(event) => setWidth(event.currentTarget.naturalWidth)}
            src={state.url}
            style={{ maxWidth: fit ? "100%" : "none", width: width > 0 ? width : undefined }}
          />
        )}
      </div>
    </div>
  );
}
