import { useRouterState } from "@tanstack/react-router";
import { useEffect, useState } from "react";

import { cn } from "../lib/cn";

type ProgressPhase = "idle" | "loading" | "complete";

// A thin bar across the top while a client-side navigation waits for a route
// chunk or loader. It appears only after 150ms so fast navigations never flash
// it, and it renders the same idle markup on the server and during hydration.
export function NavigationProgress() {
  const isLoading = useRouterState({ select: (state) => state.isLoading });
  const [phase, setPhase] = useState<ProgressPhase>("idle");

  useEffect(() => {
    if (isLoading) {
      const timer = setTimeout(() => setPhase("loading"), 150);
      return () => clearTimeout(timer);
    }

    // Finish the bar only if it was showing, then fade it out.
    setPhase((current) => (current === "loading" ? "complete" : current));
    const timer = setTimeout(() => setPhase("idle"), 200);
    return () => clearTimeout(timer);
  }, [isLoading]);

  return (
    <div
      aria-hidden
      className="pointer-events-none fixed inset-x-0 top-0 z-40 h-0.5"
    >
      <div
        className={cn(
          "h-full origin-left bg-sky-500 transition-[transform,opacity] ease-out motion-reduce:transition-none dark:bg-sky-400",
          phase === "complete" ? "duration-200" : "duration-700",
          phase === "loading" && "scale-x-[0.85] opacity-100",
          phase === "complete" && "scale-x-100 opacity-0",
          phase === "idle" && "scale-x-0 opacity-0"
        )}
      />
    </div>
  );
}
