import "@testing-library/jest-dom/vitest";

import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import type { ReactNode } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";

import { DocPage } from "./DocPage";

const params = vi.hoisted(() => ({ current: {} as { section?: string } }));

vi.mock("@tanstack/react-router", () => ({
  Link: ({ children, to }: { children: ReactNode; to?: string }) => (
    <a href={to ?? "#"}>{children}</a>
  ),
  useParams: () => params.current
}));

describe("DocPage", () => {
  afterEach(() => {
    cleanup();
    vi.restoreAllMocks();
  });

  it("renders Start Here by default and copies commands via the accessible copy button", async () => {
    const writeText = vi.fn().mockResolvedValue(undefined);
    Object.defineProperty(navigator, "clipboard", {
      configurable: true,
      value: { writeText }
    });

    params.current = {};
    render(<DocPage />);
    expect(await screen.findByRole("heading", { level: 1, name: "Start Here" })).toBeInTheDocument();
    expect(screen.getByText(/gs init <you>\/home ~\/work\/home/)).toBeInTheDocument();

    const copyBtn = screen.getByRole("button", { name: "Copy install commands" });
    fireEvent.click(copyBtn);

    await waitFor(() => {
      expect(writeText).toHaveBeenCalledWith(
        "curl -fsSL https://gitslice.io/install.sh | sh\ngs version"
      );
    });
    await waitFor(() => {
      expect(copyBtn).toHaveAttribute("data-copied", "");
    });
  });

  it("documents core concepts, slice roles, and advisory path intents", async () => {
    params.current = { section: "concepts" };
    render(<DocPage />);
    expect(await screen.findByRole("heading", { level: 1, name: "Concepts" })).toBeInTheDocument();
    expect(screen.getByRole("heading", { level: 2, name: "Slice roles and approver groups" })).toBeInTheDocument();
    expect(screen.getByRole("cell", { name: "contributor" })).toBeInTheDocument();
    expect(screen.getByText(/gs intent/)).toBeInTheDocument();
  });

  it("documents Agents: self-signup, delegated creation, contributor guardrails, and MCP", async () => {
    params.current = { section: "agents" };
    render(<DocPage />);
    expect(await screen.findByRole("heading", { level: 1, name: "Agents" })).toBeInTheDocument();
    expect(screen.getByText(/gs auth register-agent --username release-bot/)).toBeInTheDocument();
    expect(screen.getByText(/gs agent create triage-bot --slice acme\/payments --role contributor/)).toBeInTheDocument();
    expect(screen.getByText(/gs mcp --slice acme\/payments/)).toBeInTheDocument();
    expect(screen.getByText(/gs check allow <changeset>/)).toBeInTheDocument();
  });

  it("documents Git mirrors: setup, token, retries and recovery", async () => {
    params.current = { section: "mirrors" };
    render(<DocPage />);
    expect(await screen.findByRole("heading", { level: 1, name: "Git Mirrors" })).toBeInTheDocument();
    expect(screen.getByText(/gs slice mirror set acme\/app --url https:\/\/github.com\/acme\/app.git/)).toBeInTheDocument();
    expect(screen.getByRole("heading", { level: 2, name: "It never force-pushes" })).toBeInTheDocument();
    expect(screen.getByText(/after 1 minute,\s+5 minutes, 30 minutes/)).toBeInTheDocument();
  });

  it("documents secrets: scope, masking, access including contributors, and local env files", async () => {
    params.current = { section: "secrets" };
    render(<DocPage />);
    expect(await screen.findByRole("heading", { level: 1, name: "Secrets" })).toBeInTheDocument();
    expect(screen.getByRole("cell", { name: "Account owners and admins" })).toBeInTheDocument();
    expect(screen.getByRole("cell", { name: "Slice contributors" })).toBeInTheDocument();
    expect(screen.getByText(/gs secret env env.prod.template/)).toBeInTheDocument();
    expect(screen.getByText(/gitslice-secret:CLOUDFLARE_API_TOKEN/)).toBeInTheDocument();
  });

  it("links the checks doc to the secrets doc and covers gs check commands", async () => {
    params.current = { section: "checks" };
    render(<DocPage />);
    expect(await screen.findByRole("heading", { level: 2, name: "Pass secrets to checks" })).toBeInTheDocument();
    expect(screen.getAllByText("Secrets").length).toBeGreaterThan(0);
    expect(screen.getByText(/gs check allow \[changeset\]/)).toBeInTheDocument();
  });

  it("documents triggers: http, worker, and check actions plus reveal and signatures", async () => {
    params.current = { section: "triggers" };
    render(<DocPage />);
    expect(await screen.findByRole("heading", { level: 1, name: "Triggers" })).toBeInTheDocument();
    expect(screen.getByRole("cell", { name: "reveal" })).toBeInTheDocument();
    expect(screen.getByRole("cell", { name: "gs.revealSecret(name)" })).toBeInTheDocument();
    expect(screen.getAllByText(/sha256=<hex>/).length).toBeGreaterThan(0);
  });

  it("documents Git migration, sync, and Go modules for Git users", async () => {
    params.current = { section: "git-users" };
    render(<DocPage />);
    expect(await screen.findByRole("heading", { level: 1, name: "For Git Users" })).toBeInTheDocument();
    expect(screen.getByRole("heading", { level: 2, name: "How do I import an existing Git repository?" })).toBeInTheDocument();
    expect(screen.getByRole("heading", { level: 2, name: "Can I use a slice as a Go module?" })).toBeInTheDocument();
  });

  it("lists setup, sync, MCP, intent, and create --submit in the CLI reference", async () => {
    params.current = { section: "cli" };
    render(<DocPage />);
    expect(await screen.findByText("gs doctor [account/slice]")).toBeInTheDocument();
    expect(screen.queryByText("brew install gitslice-io/tap/gitslice")).not.toBeInTheDocument();
    expect(screen.getByText('gs feedback "<message>" --category bug|idea|docs')).toBeInTheDocument();
    expect(screen.getByText("gs completion bash|zsh|fish")).toBeInTheDocument();
    expect(screen.getByText("gs create --all --message <title> --submit")).toBeInTheDocument();
    expect(screen.getByText("gs sync")).toBeInTheDocument();
    expect(screen.getByText("gs mcp [--slice <account>/<slice>]")).toBeInTheDocument();
    expect(screen.getByText("gs intent <glob> --for 2h -m <note>")).toBeInTheDocument();
  });

  it("documents lazy workspaces: fetching, edits, checkpoints and requirements", async () => {
    params.current = { section: "lazy" };
    render(<DocPage />);
    expect(await screen.findByRole("heading", { level: 1, name: "Lazy Workspaces" })).toBeInTheDocument();
    for (const name of ["How files are fetched", "Where your edits live", "Checkpoints, rewind and fork", "Requirements"]) {
      expect(screen.getByRole("heading", { level: 2, name })).toBeInTheDocument();
    }
    expect(screen.getByText("GS_READAHEAD=off")).toBeInTheDocument();
    expect(screen.getByText(/gs fork 3 ~\/work\/payments-b/)).toBeInTheDocument();
  });

  it("lists the checkpoint commands in the CLI reference", async () => {
    params.current = { section: "cli" };
    render(<DocPage />);
    expect(await screen.findByText("gs checkpoint [-m <message>]")).toBeInTheDocument();
    expect(screen.getByText("gs rewind [checkpoint]")).toBeInTheDocument();
    expect(screen.getByText("gs fork <checkpoint> <dir>")).toBeInTheDocument();
  });
});
