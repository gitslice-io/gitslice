import { useParams } from "@tanstack/react-router";

import { type DocParams, normalizeSection, docSections } from "./doc/docData";
import { SectionLink } from "./doc/DocParts";
import { StartHereDoc } from "./doc/StartHereDoc";
import { ConceptsDoc } from "./doc/ConceptsDoc";
import { AgentsDoc } from "./doc/AgentsDoc";
import { ChecksDoc } from "./doc/ChecksDoc";
import { WebhooksDoc } from "./doc/WebhooksDoc";
import { GitUsersDoc } from "./doc/GitUsersDoc";
import { CliReferenceDoc } from "./doc/CliReferenceDoc";

export function DocPage() {
  const params = useParams({ strict: false }) as DocParams;
  const section = normalizeSection(params.section);

  return (
    <section className="mx-auto grid w-full max-w-[100rem] gap-8 lg:grid-cols-[17rem_minmax(0,1fr)]">
      <aside className="lg:sticky lg:top-6 lg:self-start">
        <div className="grid gap-2">
          {docSections.map((item) => (
            <SectionLink
              active={section === item.id}
              key={item.id}
              section={item}
            />
          ))}
        </div>
      </aside>

      <div className="min-w-0">
        {section === "start" ? <StartHereDoc /> : null}
        {section === "concepts" ? <ConceptsDoc /> : null}
        {section === "agents" ? <AgentsDoc /> : null}
        {section === "checks" ? <ChecksDoc /> : null}
        {section === "webhooks" ? <WebhooksDoc /> : null}
        {section === "git-users" ? <GitUsersDoc /> : null}
        {section === "cli" ? <CliReferenceDoc /> : null}
      </div>
    </section>
  );
}
