import "@testing-library/jest-dom/vitest";

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, render, screen } from "@testing-library/react";
import type { ReactNode } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";

const auth = vi.hoisted(() => ({ current: { isLoaded: true, isSignedIn: true } }));
const api = vi.hoisted(() => ({ listPendingClaims: vi.fn() }));

vi.mock("@clerk/tanstack-react-start", () => ({ useAuth: () => auth.current }));
vi.mock("@tanstack/react-router", () => ({
  Link: ({ children, to, ...rest }: { children: ReactNode; to: string }) => (
    <a href={to} {...rest}>
      {children}
    </a>
  )
}));
vi.mock("../api/useApi", () => ({ useApi: () => api }));

import { ClaimsNotice } from "./ClaimsNotice";

function renderNotice() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={client}>
      <ClaimsNotice />
    </QueryClientProvider>
  );
}

afterEach(() => {
  cleanup();
  vi.clearAllMocks();
});

describe("ClaimsNotice", () => {
  it("links to the claims page while agents wait to be claimed", async () => {
    auth.current = { isLoaded: true, isSignedIn: true };
    api.listPendingClaims.mockResolvedValue({ claims: [{ agentSubjectId: "a" }, { agentSubjectId: "b" }] });
    renderNotice();
    const link = await screen.findByRole("link", { name: "2 agents to claim: review" });
    expect(link).toHaveAttribute("href", "/claims");
  });

  it("shows nothing with nothing to claim, or signed out", async () => {
    api.listPendingClaims.mockResolvedValue({ claims: [] });
    renderNotice();
    await new Promise((resolve) => setTimeout(resolve, 0));
    expect(screen.queryByRole("link")).not.toBeInTheDocument();

    cleanup();
    auth.current = { isLoaded: true, isSignedIn: false };
    renderNotice();
    expect(api.listPendingClaims).toHaveBeenCalledTimes(1);
    expect(screen.queryByRole("link")).not.toBeInTheDocument();
  });
});
