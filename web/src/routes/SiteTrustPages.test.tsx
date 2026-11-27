import "@testing-library/jest-dom/vitest";

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, render, screen, waitFor } from "@testing-library/react";
import type { ReactNode } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";

vi.mock("@tanstack/react-router", () => ({
  Link: ({
    children,
    className,
    params,
    to
  }: {
    children: ReactNode;
    className?: string;
    params?: Record<string, string>;
    to: string;
  }) => {
    let href = to;
    for (const [key, value] of Object.entries(params ?? {})) {
      href = href.replace(`$${key}`, value);
    }
    return (
      <a className={className} href={href}>
        {children}
      </a>
    );
  }
}));

import { AboutPage } from "./AboutPage";
import { ChangelogPage } from "./ChangelogPage";
import { PricingPage } from "./PricingPage";
import { PrivacyPage } from "./PrivacyPage";
import { SecurityPage } from "./SecurityPage";
import { StatusPage } from "./StatusPage";
import { TermsPage } from "./TermsPage";

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
});

describe("Institutional trust & legal pages", () => {
  it("renders SecurityPage with controls, verified install, and disclosure email", () => {
    render(<SecurityPage />);
    expect(screen.getByRole("heading", { level: 1, name: "Security at Gitslice" })).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "security@gitslice.io" })).toHaveAttribute(
      "href",
      "mailto:security@gitslice.io"
    );
    expect(screen.getByRole("link", { name: "/.well-known/security.txt" })).toHaveAttribute(
      "href",
      "/.well-known/security.txt"
    );
  });

  it("renders PricingPage with three tiers and accessible FAQ disclosures", () => {
    render(<PricingPage />);
    expect(screen.getByRole("heading", { level: 1, name: "Simple, transparent plans" })).toBeInTheDocument();
    expect(screen.getByRole("heading", { level: 2, name: "Personal & Open Source" })).toBeInTheDocument();
    expect(screen.getByRole("heading", { level: 2, name: "Team" })).toBeInTheDocument();
    expect(screen.getByRole("heading", { level: 2, name: "Self-Hosted & Enterprise" })).toBeInTheDocument();
    expect(screen.getByText(/Will free accounts lose access/)).toBeInTheDocument();
  });

  it("renders ChangelogPage with dated milestones and native Gitslice releases link", () => {
    render(<ChangelogPage />);
    expect(screen.getByRole("heading", { level: 1, name: "What we've shipped" })).toBeInTheDocument();
    expect(screen.getByText("Reusable automation templates & multi-step pipelines")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: /Browse Gitslice CLI releases/i })).toHaveAttribute(
      "href",
      "/releases"
    );
  });

  it("renders StatusPage and probes same-origin /api/status", async () => {
    const fetchMock = vi.fn().mockResolvedValue({
      ok: true,
      status: 200,
      json: async () => ({
        overall: "operational",
        checkedAt: "2026-11-25T03:30:00.000Z",
        api: { status: "operational", latencyMs: 42, httpStatus: 200 }
      })
    });
    vi.stubGlobal("fetch", fetchMock);
    const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    render(
      <QueryClientProvider client={queryClient}>
        <StatusPage />
      </QueryClientProvider>
    );
    expect(screen.getByRole("heading", { level: 1, name: "Gitslice Status" })).toBeInTheDocument();
    await waitFor(() => {
      expect(screen.getByText("All core systems operational")).toBeInTheDocument();
    });
    expect(fetchMock).toHaveBeenCalledWith(
      "/api/status",
      expect.objectContaining({ method: "GET", cache: "no-store" })
    );
    expect(screen.getByText(/42 ms/)).toBeInTheDocument();
  });

  it("renders AboutPage, PrivacyPage, and TermsPage with clear headings", () => {
    render(<AboutPage />);
    expect(screen.getByRole("heading", { level: 1, name: "Source infrastructure built for parallel work" })).toBeInTheDocument();
    cleanup();

    render(<PrivacyPage />);
    expect(screen.getByRole("heading", { level: 1, name: "Privacy Policy" })).toBeInTheDocument();
    expect(screen.getByText(/do not sell your personal information/i)).toBeInTheDocument();
    cleanup();

    render(<TermsPage />);
    expect(screen.getByRole("heading", { level: 1, name: "Terms of Service" })).toBeInTheDocument();
    expect(screen.getByText(/retain full ownership/i)).toBeInTheDocument();
  });
});
