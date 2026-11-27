import "@testing-library/jest-dom/vitest";

import {
  RouterContextProvider,
  createMemoryHistory,
  createRootRoute,
  createRouter
} from "@tanstack/react-router";
import { cleanup, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it } from "vitest";

import type { Changeset } from "../../api/types";
import { ChangesetMetaLine, formatActorAttribution } from "./ChangesetMetaLine";

function renderMetaLine(changeset: Changeset) {
  const router = createRouter({
    history: createMemoryHistory(),
    routeTree: createRootRoute()
  });
  return render(
    <RouterContextProvider router={router}>
      <ChangesetMetaLine changeset={changeset} />
    </RouterContextProvider>
  );
}

describe("ChangesetMetaLine and formatActorAttribution", () => {
  afterEach(cleanup);

  it("formats full agent attribution with controller, daemon, and conversation", () => {
    expect(
      formatActorAttribution(
        {
          actingSubjectId: "agent_1",
          actingUsername: "web-agent",
          controllerSubjectId: "user_nic",
          controllerUsername: "nic",
          daemonId: "agent_daemon_1",
          daemonName: "my-ci",
          conversationId: "cv_99"
        },
        "web-agent"
      )
    ).toBe("web-agent (for nic) via my-ci, conversation cv_99");

    const { container } = renderMetaLine({
      id: "cs_1",
      author: "web-agent",
      attribution: {
        actingSubjectId: "agent_1",
        actingUsername: "web-agent",
        controllerSubjectId: "user_nic",
        controllerUsername: "nic",
        daemonId: "agent_daemon_1",
        daemonName: "my-ci",
        conversationId: "cv_99"
      }
    });

    expect(container.textContent?.replace(/\s+/g, " ").trim()).toBe(
      "by web-agent (for nic) via my-ci, conversation cv_99"
    );
    expect(screen.getByRole("link", { name: "web-agent" })).toHaveAttribute(
      "href",
      "/accounts/web-agent"
    );
    expect(screen.getByRole("link", { name: "nic" })).toHaveAttribute("href", "/accounts/nic");
  });

  it("omits controller when same as acting user and falls back to daemonId when daemonName is empty", () => {
    expect(
      formatActorAttribution(
        {
          actingSubjectId: "user_nic",
          actingUsername: "nic",
          controllerSubjectId: "user_nic",
          controllerUsername: "nic",
          daemonId: "agent_raw_daemon"
        },
        "nic"
      )
    ).toBe("nic via agent_raw_daemon");

    const { container } = renderMetaLine({
      id: "cs_2",
      author: "sunny",
      attribution: {
        actingSubjectId: "user_sunny",
        actingUsername: "sunny",
        controllerSubjectId: "user_sunny",
        controllerUsername: "sunny"
      }
    });
    expect(container.textContent).toMatch(/^by\s*sunny$/);
  });

  it("merges daemon and conversation from the current patchset when authored by the same actor", () => {
    const { container } = renderMetaLine({
      id: "cs_3",
      author: "web-agent",
      currentPatchsetId: "ps_1",
      attribution: {
        actingSubjectId: "agent_1",
        actingUsername: "web-agent",
        controllerSubjectId: "user_nic",
        controllerUsername: "nic"
      },
      patchsets: [
        {
          id: "ps_1",
          attribution: {
            actingSubjectId: "agent_1",
            actingUsername: "web-agent",
            daemonName: "runner-west",
            conversationId: "cv_42"
          }
        }
      ]
    });
    expect(container.textContent?.replace(/\s+/g, " ").trim()).toBe(
      "by web-agent (for nic) via runner-west, conversation cv_42"
    );
  });
});
