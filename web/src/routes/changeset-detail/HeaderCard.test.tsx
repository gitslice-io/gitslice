import "@testing-library/jest-dom/vitest";

import {
  RouterContextProvider,
  createMemoryHistory,
  createRootRoute,
  createRouter
} from "@tanstack/react-router";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";

import { HeaderCard } from "./HeaderCard";

function renderCard(props: Partial<Parameters<typeof HeaderCard>[0]> = {}) {
  const router = createRouter({
    history: createMemoryHistory(),
    routeTree: createRootRoute()
  });
  const base = {
    abandonPending: false,
    abandonReason: "",
    actionBusy: false,
    actionError: "",
    canUseReviewActions: true,
    changeset: {
      description: "Why it **changed**.",
      id: "cs_1",
      status: "draft",
      title: "Tighten authorization"
    },
    dependentChangesets: [],
    mergePending: false,
    onAbandon: vi.fn(),
    onAbandonReasonChange: vi.fn(),
    onMerge: vi.fn(),
    terminal: false
  };
  return render(
    <RouterContextProvider router={router}>
      <HeaderCard {...base} {...props} />
    </RouterContextProvider>
  );
}

describe("HeaderCard title and description", () => {
  afterEach(cleanup);

  it("shows the title and renders the description as Markdown", async () => {
    renderCard();
    expect(screen.getByRole("heading", { name: "Tighten authorization" })).toBeInTheDocument();
    expect(await screen.findByText("changed")).toBeInstanceOf(HTMLElement);
    expect((await screen.findByText("changed")).tagName).toBe("STRONG");
  });

  it("offers no edit without permission or once the changeset is no longer a draft", () => {
    renderCard();
    expect(screen.queryByRole("button", { name: "Edit" })).toBeNull();
    cleanup();
    renderCard({
      changeset: { id: "cs_1", status: "published", title: "t" },
      onSaveInfo: vi.fn()
    });
    expect(screen.queryByRole("button", { name: "Edit" })).toBeNull();
  });

  it("saves an edited title and description", async () => {
    const onSaveInfo = vi.fn().mockResolvedValue(undefined);
    renderCard({ onSaveInfo });
    fireEvent.click(screen.getByRole("button", { name: "Edit" }));
    fireEvent.change(screen.getByLabelText(/^Title/), {
      target: { value: "  Tighten auth  " }
    });
    fireEvent.change(screen.getByLabelText("Description"), {
      target: { value: "New body" }
    });
    fireEvent.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() => expect(onSaveInfo).toHaveBeenCalledWith("Tighten auth", "New body"));
    await waitFor(() => expect(screen.queryByLabelText("Description")).toBeNull());
  });

  it("keeps the editor open and shows the error when saving fails", async () => {
    const onSaveInfo = vi.fn().mockRejectedValue(new Error("only while it is open"));
    renderCard({ onSaveInfo });
    fireEvent.click(screen.getByRole("button", { name: "Edit" }));
    fireEvent.change(screen.getByLabelText(/^Title/), { target: { value: "x" } });
    fireEvent.click(screen.getByRole("button", { name: "Save" }));
    expect(await screen.findByText(/only while it is open/)).toBeInTheDocument();
    expect(screen.getByLabelText("Description")).toBeInTheDocument();
  });
});
