import "@testing-library/jest-dom/vitest";

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, render, screen } from "@testing-library/react";
import type { ReactNode } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";

const api = vi.hoisted(() => ({ listSharedSlices: vi.fn() }));
vi.mock("../../api/useApi", () => ({ useApi: () => api }));
vi.mock("@clerk/tanstack-react-start", () => ({ useAuth: () => ({ isLoaded: true, isSignedIn: true }) }));
vi.mock("@tanstack/react-router", () => ({
  Link: ({ children }: { children: ReactNode }) => <a href="#">{children}</a>
}));

import { SharedSlices } from "./SharedSlices";

function renderShared() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={client}>
      <SharedSlices />
    </QueryClientProvider>
  );
}

afterEach(() => {
  cleanup();
  vi.clearAllMocks();
});

describe("SharedSlices", () => {
  it("lists slices shared with you and your role", async () => {
    api.listSharedSlices.mockResolvedValue({
      slices: [
        {
          role: "writer",
          slice: { definition: { includedPaths: ["/alice-agent/project"], visibility: "private" }, ref: { account: "alice-agent", slice: "project" } }
        }
      ]
    });
    renderShared();
    expect(await screen.findByRole("heading", { name: "Shared with you" })).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "alice-agent/project" })).toBeInTheDocument();
    expect(screen.getByText("writer")).toBeInTheDocument();
  });

  it("shows nothing when nothing is shared", async () => {
    api.listSharedSlices.mockResolvedValue({ slices: [] });
    const { container } = renderShared();
    await new Promise((resolve) => setTimeout(resolve, 0));
    expect(container).toBeEmptyDOMElement();
  });
});
