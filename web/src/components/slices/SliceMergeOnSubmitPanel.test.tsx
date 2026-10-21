import "@testing-library/jest-dom/vitest";

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";

import { SliceMergeOnSubmitPanel } from "./SliceMergeOnSubmitPanel";

const api = vi.hoisted(() => ({ setSliceMergeOnSubmit: vi.fn() }));
vi.mock("../../api/useApi", () => ({ useApi: () => api }));

afterEach(cleanup);

describe("SliceMergeOnSubmitPanel", () => {
  it("shows the setting and turns it on", async () => {
    const ref = { account: "acme", slice: "app" };
    api.setSliceMergeOnSubmit.mockResolvedValue({ ref, mergeOnSubmit: true });
    render(
      <QueryClientProvider client={new QueryClient()}>
        <SliceMergeOnSubmitPanel slice={{ ref, mergeOnSubmit: false }} />
      </QueryClientProvider>
    );
    const toggle = screen.getByRole("checkbox", { name: "Merge stale changesets on submit" });
    expect(toggle).not.toBeChecked();

    fireEvent.click(toggle);
    expect(await screen.findByText("Merge on submit is on.")).toBeInTheDocument();
    expect(api.setSliceMergeOnSubmit).toHaveBeenCalledWith({ enabled: true, slice: ref });
  });

  it("says why it could not change", async () => {
    api.setSliceMergeOnSubmit.mockRejectedValue(new Error("only owners and admins may change it"));
    render(
      <QueryClientProvider client={new QueryClient()}>
        <SliceMergeOnSubmitPanel slice={{ ref: { account: "acme", slice: "app" }, mergeOnSubmit: true }} />
      </QueryClientProvider>
    );
    fireEvent.click(screen.getByRole("checkbox", { name: "Merge stale changesets on submit" }));
    expect(await screen.findByText("only owners and admins may change it")).toBeInTheDocument();
  });
});
