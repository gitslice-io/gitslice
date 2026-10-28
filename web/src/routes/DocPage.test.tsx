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

  it("documents secrets: scope, masking, access and local env files", () => {
    params.current = { section: "secrets" };
    render(<DocPage />);
    expect(screen.getByRole("heading", { level: 1, name: "Secrets" })).toBeInTheDocument();
    expect(screen.getByRole("cell", { name: "Account owners and admins" })).toBeInTheDocument();
    expect(screen.getByText(/gs secret env env.prod.template/)).toBeInTheDocument();
    expect(screen.getByText(/gitslice-secret:CLOUDFLARE_API_TOKEN/)).toBeInTheDocument();
  });

  it("links the checks doc to the secrets doc", () => {
    params.current = { section: "checks" };
    render(<DocPage />);
    expect(screen.getByRole("heading", { level: 2, name: "Pass secrets to checks" })).toBeInTheDocument();
    expect(screen.getAllByText("Secrets").length).toBeGreaterThan(0);
  });

  it("lists the webhook commands in the CLI reference", () => {
    params.current = { section: "cli" };
    render(<DocPage />);
    expect(screen.getByText("gs webhook ping <webhook-id>")).toBeInTheDocument();
  });

  it("lists the setup commands and create --submit in the CLI reference", () => {
    params.current = { section: "cli" };
    render(<DocPage />);
    expect(screen.getByText("gs doctor [account/slice]")).toBeInTheDocument();
    expect(screen.getByText("brew install gitslice-io/tap/gitslice")).toBeInTheDocument();
    expect(screen.getByText('gs feedback "<message>" --category bug|idea|docs')).toBeInTheDocument();
    expect(screen.getByText("gs completion bash|zsh|fish")).toBeInTheDocument();
    expect(screen.getByText("gs create --all --message <title> --submit")).toBeInTheDocument();
  });

  it("documents lazy workspaces: fetching, edits, checkpoints and requirements", () => {
    params.current = { section: "lazy" };
    render(<DocPage />);
    expect(screen.getByRole("heading", { level: 1, name: "Lazy Workspaces" })).toBeInTheDocument();
    for (const name of ["How files are fetched", "Where your edits live", "Checkpoints, rewind and fork", "Requirements"]) {
      expect(screen.getByRole("heading", { level: 2, name })).toBeInTheDocument();
    }
    expect(screen.getByText("GS_READAHEAD=off")).toBeInTheDocument();
    expect(screen.getByText(/gs fork 3 ~\/work\/payments-b/)).toBeInTheDocument();
  });

  it("lists the checkpoint commands in the CLI reference", () => {
    params.current = { section: "cli" };
    render(<DocPage />);
    expect(screen.getByText("gs checkpoint [-m <message>]")).toBeInTheDocument();
    expect(screen.getByText("gs rewind [checkpoint]")).toBeInTheDocument();
    expect(screen.getByText("gs fork <checkpoint> <dir>")).toBeInTheDocument();
  });
});
