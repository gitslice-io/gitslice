import { Suspense, lazy, type ComponentType } from "react";
import { useParams } from "@tanstack/react-router";

import {
  type DocParams,
  type DocSection,
  docSections,
  normalizeSection
} from "./doc/docData";
import { SectionLink } from "./doc/DocParts";

const lazyDocSections: Record<DocSection, ComponentType> = {
  start: lazy(() => import("./doc/StartHereDoc").then((m) => ({ default: m.StartHereDoc }))),
  concepts: lazy(() => import("./doc/ConceptsDoc").then((m) => ({ default: m.ConceptsDoc }))),
  agents: lazy(() => import("./doc/AgentsDoc").then((m) => ({ default: m.AgentsDoc }))),
  lazy: lazy(() => import("./doc/LazyWorkspacesDoc").then((m) => ({ default: m.LazyWorkspacesDoc }))),
  checks: lazy(() => import("./doc/ChecksDoc").then((m) => ({ default: m.ChecksDoc }))),
  secrets: lazy(() => import("./doc/SecretsDoc").then((m) => ({ default: m.SecretsDoc }))),
  triggers: lazy(() => import("./doc/TriggersDoc").then((m) => ({ default: m.TriggersDoc }))),
  mirrors: lazy(() => import("./doc/MirrorsDoc").then((m) => ({ default: m.MirrorsDoc }))),
  "git-users": lazy(() => import("./doc/GitUsersDoc").then((m) => ({ default: m.GitUsersDoc }))),
  cli: lazy(() => import("./doc/CliReferenceDoc").then((m) => ({ default: m.CliReferenceDoc })))
};

export function DocPage() {
  const params = useParams({ strict: false }) as DocParams;
  const section = normalizeSection(params.section);
  const ActiveSection = lazyDocSections[section];

  return (
    <section className="mx-auto grid w-full max-w-[100rem] grid-cols-1 gap-8 lg:grid-cols-[17rem_minmax(0,1fr)]">
      <aside className="min-w-0 lg:sticky lg:top-6 lg:self-start">
        <nav aria-label="Documentation sections" className="flex flex-wrap gap-2 lg:grid">
          {docSections.map((item) => (
            <SectionLink
              active={section === item.id}
              key={item.id}
              section={item}
            />
          ))}
        </nav>
      </aside>

      <div className="min-w-0">
        <Suspense fallback={<div aria-hidden className="min-h-32" />}>
          <ActiveSection />
        </Suspense>
      </div>
    </section>
  );
}
