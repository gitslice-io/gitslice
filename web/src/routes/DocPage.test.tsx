import "@testing-library/jest-dom/vitest";

import { cleanup, render, screen } from "@testing-library/react";
import type { ReactNode } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";

import { DocPage } from "./DocPage";

const params = vi.hoisted(() => ({ current: {} as { section?: string } }));

vi.mock("@tanstack/react-router", () => ({
  Link: ({ children }: { children: ReactNode }) => <a href="#">{children}</a>,
  useParams: () => params.current
}));

describe("DocPage", () => {
  afterEach(() => {
    cleanup();
  });

  it("documents webhooks: events, signature and retries", () => {
    params.current = { section: "webhooks" };
    render(<DocPage />);
    expect(screen.getByRole("heading", { level: 1, name: "Webhooks" })).toBeInTheDocument();
    expect(screen.getByRole("cell", { name: "check_run.completed" })).toBeInTheDocument();
    expect(screen.getAllByText("X-Gitslice-Signature-256").length).toBeGreaterThan(0);
    expect(screen.getByText(/1 minute, 5 minutes, 30 minutes/)).toBeInTheDocument();
  });

  it("lists the webhook commands in the CLI reference", () => {
    params.current = { section: "cli" };
    render(<DocPage />);
    expect(screen.getByText("gs webhook ping <webhook-id>")).toBeInTheDocument();
  });
});
