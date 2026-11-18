import { useRef, useState, type KeyboardEvent } from "react";

type TerminalTabId = "agent-cli" | "lazy-mount" | "mcp";

const terminalTabs: Array<{
  id: TerminalTabId;
  label: string;
  path: string;
}> = [
  { id: "agent-cli", label: "Agent CLI", path: "~/work/checkout" },
  { id: "lazy-mount", label: "Lazy Mount", path: "~/work/monorepo" },
  { id: "mcp", label: "MCP", path: "stdio · gs mcp" }
];

const parallelLanes = [
  { agent: "build-bot", scope: "/orbit/checkout", cs: "8b4ee46085", check: "orbit/unit" },
  { agent: "billing-bot", scope: "/orbit/billing", cs: "c19a2f04d1", check: "billing/test" },
  { agent: "web-bot", scope: "/orbit/web", cs: "4e7b91ca03", check: "web/build" }
];

export function LandingHeroTerminal() {
  const [activeTab, setActiveTab] = useState<TerminalTabId>("agent-cli");
  const tabRefs = useRef<Array<HTMLButtonElement | null>>([]);
  const activeMeta = terminalTabs.find((t) => t.id === activeTab) ?? terminalTabs[0];

  function handleTabKeyDown(event: KeyboardEvent<HTMLButtonElement>, index: number) {
    let nextIndex = -1;
    if (event.key === "ArrowRight") {
      nextIndex = (index + 1) % terminalTabs.length;
    } else if (event.key === "ArrowLeft") {
      nextIndex = (index - 1 + terminalTabs.length) % terminalTabs.length;
    } else if (event.key === "Home") {
      nextIndex = 0;
    } else if (event.key === "End") {
      nextIndex = terminalTabs.length - 1;
    }

    if (nextIndex !== -1) {
      event.preventDefault();
      const nextTab = terminalTabs[nextIndex];
      setActiveTab(nextTab.id);
      tabRefs.current[nextIndex]?.focus();
    }
  }

  return (
    <figure className="relative min-w-0 max-w-full">
      <div
        aria-hidden="true"
        className="pointer-events-none absolute -inset-4 -z-10 rounded-[2rem] bg-gradient-to-br from-sky-500/[0.10] via-transparent to-emerald-500/[0.08] blur-2xl dark:from-sky-400/[0.12] dark:to-emerald-400/[0.08]"
      />

      <div className="overflow-hidden rounded-2xl border border-zinc-800/90 bg-zinc-950 shadow-2xl shadow-zinc-950/35 ring-1 ring-white/[0.06] dark:border-white/10 dark:shadow-black/60">
        <div className="flex items-center justify-between gap-3 border-b border-white/10 bg-zinc-900/90 px-4">
          <div className="flex min-w-0 flex-1 items-center gap-3">
            <div aria-hidden="true" className="flex shrink-0 gap-1.5 py-3.5">
              <span className="h-2.5 w-2.5 rounded-full bg-zinc-700" />
              <span className="h-2.5 w-2.5 rounded-full bg-zinc-700" />
              <span className="h-2.5 w-2.5 rounded-full bg-zinc-700" />
            </div>

            <div
              aria-label="Gitslice workflow examples"
              className="flex items-center overflow-x-auto"
              role="tablist"
              {...({ focusgroup: "tablist nomemory" } as Record<string, string>)}
            >
              {terminalTabs.map((tab, index) => {
                const selected = activeTab === tab.id;
                return (
                  <button
                    aria-selected={selected}
                    className={[
                      "landing-tab shrink-0 px-3 py-3 font-mono text-xs outline-none transition-colors focus-visible:text-white focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-sky-400",
                      selected ? "text-white" : "text-zinc-400 hover:text-zinc-200"
                    ].join(" ")}
                    id={`hero-tab-${tab.id}`}
                    key={tab.id}
                    onClick={() => setActiveTab(tab.id)}
                    onKeyDown={(e) => handleTabKeyDown(e, index)}
                    ref={(el) => {
                      tabRefs.current[index] = el;
                    }}
                    role="tab"
                    tabIndex={selected ? 0 : -1}
                    type="button"
                    {...(selected ? ({ focusgroupstart: "" } as Record<string, string>) : {})}
                  >
                    {tab.label}
                  </button>
                );
              })}
            </div>
          </div>

          <span className="hidden truncate font-mono text-[11px] text-zinc-500 sm:inline">
            {activeMeta.path}
          </span>
        </div>

        <div className="grid grid-cols-1">
          <div
            aria-labelledby="hero-tab-agent-cli"
            className="col-start-1 row-start-1 min-w-0"
            hidden={activeTab !== "agent-cli"}
            role="tabpanel"
            tabIndex={activeTab === "agent-cli" ? 0 : undefined}
          >
            <div className="min-h-[21.5rem] overflow-x-auto p-5 font-mono text-[12px] leading-6 sm:p-6 sm:text-[13px]">
              <p>
                <span className="text-sky-400">$</span>{" "}
                <span className="text-zinc-100">
                  gs auth register-agent --username build-bot \
                </span>
              </p>
              <p className="pl-4 text-zinc-100">--email mira@orbit.dev</p>
              <p className="text-zinc-400">
                Registered build-bot; claim link sent for mira@orbit.dev
              </p>

              <p className="mt-4">
                <span className="text-sky-400">$</span>{" "}
                <span className="text-zinc-100">gs init --lazy orbit/checkout work</span>
              </p>
              <p className="text-zinc-400">
                mounted orbit:checkout at ~/work (318 files, fetched on first read; edits stay
                local until gs create)
              </p>

              <p className="mt-4">
                <span className="text-sky-400">$</span>{" "}
                <span className="text-zinc-100">gs create --all --submit \</span>
              </p>
              <p className="pl-4 text-zinc-100">
                --message &quot;Tighten authorization&quot;
              </p>
              <p className="text-zinc-400">created 8b4ee46085 patchset 1</p>
              <p className="text-zinc-400">view: https://gitslice.io/cs/8b4ee46085</p>
              <p className="text-zinc-400">
                waiting for required check &quot;orbit/unit&quot; to pass...
              </p>
              <p className="text-emerald-400">✓ submitted 8b4ee46085 → refs/global/main</p>
            </div>
          </div>

          <div
            aria-labelledby="hero-tab-lazy-mount"
            className="col-start-1 row-start-1 min-w-0"
            hidden={activeTab !== "lazy-mount"}
            role="tabpanel"
            tabIndex={activeTab === "lazy-mount" ? 0 : undefined}
          >
            <div className="min-h-[21.5rem] overflow-x-auto p-5 font-mono text-[12px] leading-6 sm:p-6 sm:text-[13px]">
              <p>
                <span className="text-sky-400">$</span>{" "}
                <span className="text-zinc-100">
                  gs init --lazy orbit/monorepo ~/work/monorepo
                </span>
              </p>
              <p className="text-zinc-400">
                mounted 4,508 files in 2.6 s (listing only; reads fetch on demand)
              </p>

              <p className="mt-4">
                <span className="text-sky-400">$</span>{" "}
                <span className="text-zinc-100">
                  gs checkpoint -m &quot;tests green, before refactor&quot;
                </span>
              </p>
              <p className="text-zinc-400">saved local checkpoint #3 (nothing uploaded)</p>

              <p className="mt-4">
                <span className="text-sky-400">$</span>{" "}
                <span className="text-zinc-100">gs fork 3 ~/work/monorepo-alt</span>
              </p>
              <p className="text-zinc-400">
                mounted checkpoint #3 at ~/work/monorepo-alt on base sha256:9f2c…
              </p>

              <p className="mt-4">
                <span className="text-sky-400">$</span>{" "}
                <span className="text-zinc-100">gs rewind 3</span>
              </p>
              <p className="text-emerald-400">
                ✓ restored local edits to checkpoint #3 (previous state saved as #4)
              </p>
            </div>
          </div>

          <div
            aria-labelledby="hero-tab-mcp"
            className="col-start-1 row-start-1 min-w-0"
            hidden={activeTab !== "mcp"}
            role="tabpanel"
            tabIndex={activeTab === "mcp" ? 0 : undefined}
          >
            <div className="min-h-[21.5rem] overflow-x-auto p-5 font-mono text-[12px] leading-6 sm:p-6 sm:text-[13px]">
              <p>
                <span className="text-sky-400">$</span>{" "}
                <span className="text-zinc-100">gs mcp --slice orbit/checkout</span>
              </p>
              <p className="text-zinc-400">
                serving MCP on stdio (no local checkout needed)
              </p>

              <p className="mt-4 text-zinc-400">
                <span className="text-sky-400">→</span> tool:{" "}
                <span className="text-zinc-100">
                  search(&#123; query: &quot;verifyToken&quot; &#125;)
                </span>
              </p>
              <p className="pl-4 text-zinc-400">
                orbit/checkout/auth.go:42 · 1 match
              </p>

              <p className="mt-3 text-zinc-400">
                <span className="text-sky-400">→</span> tool:{" "}
                <span className="text-zinc-100">
                  create_changeset(&#123; edits: [...], submit: true &#125;)
                </span>
              </p>
              <p className="pl-4 text-zinc-400">
                changeset: 8b4ee46085 · required checks: passed
              </p>
              <p className="pl-4 text-emerald-400">
                ✓ status: &quot;submitted&quot;, commit: &quot;sha256:3f8d…&quot;
              </p>
            </div>
          </div>
        </div>

        <div className="border-t border-white/10 bg-zinc-900/60 px-4 py-3 sm:px-5">
          <div className="flex items-center justify-between gap-2 text-[11px] font-mono">
            <span className="uppercase tracking-[0.14em] text-zinc-400">
              Parallel admission · refs/global/main
            </span>
            <span className="rounded-full border border-emerald-500/30 bg-emerald-500/10 px-2 py-0.5 text-emerald-300">
              0 retries
            </span>
          </div>
          <div className="mt-2.5 grid gap-1.5 font-mono text-[11px]">
            {parallelLanes.map((lane) => (
              <div
                className="flex items-center justify-between gap-2 rounded-md border border-white/[0.06] bg-zinc-950/80 px-2.5 py-1.5"
                key={lane.agent}
              >
                <div className="flex min-w-0 items-center gap-2">
                  <span className="h-1.5 w-1.5 shrink-0 rounded-full bg-emerald-400" />
                  <span className="text-zinc-200">{lane.agent}</span>
                  <span className="truncate text-zinc-500">{lane.scope}</span>
                </div>
                <div className="flex shrink-0 items-center gap-2">
                  <span className="hidden text-zinc-500 sm:inline">{lane.check}</span>
                  <span className="text-emerald-400">landed</span>
                </div>
              </div>
            ))}
          </div>
        </div>
      </div>

      <figcaption className="sr-only">
        Interactive Gitslice workflow examples showing an agent registering itself and submitting
        a changeset via the CLI, using lazy workspace checkpoints and forks, or submitting changes
        over MCP while multiple agents land in parallel with zero retries.
      </figcaption>
    </figure>
  );
}
