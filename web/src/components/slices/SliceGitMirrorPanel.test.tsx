import "@testing-library/jest-dom/vitest";

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { RpcError } from "../../api/rpcError";
import { SliceGitMirrorPanel } from "./SliceGitMirrorPanel";

const api = vi.hoisted(() => ({
  getSliceGitMirror: vi.fn(),
  setSliceGitMirror: vi.fn(),
  pushSliceGitMirror: vi.fn(),
  deleteSliceGitMirror: vi.fn(),
  listSliceSecrets: vi.fn()
}));
vi.mock("../../api/useApi", () => ({ useApi: () => api }));

const slice = { account: "acme", slice: "app" };

function renderPanel() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={client}>
      <SliceGitMirrorPanel slice={slice} />
    </QueryClientProvider>
  );
}

beforeEach(() => {
  vi.resetAllMocks();
  api.listSliceSecrets.mockResolvedValue({ secrets: [{ name: "GITHUB_MIRROR_TOKEN" }, { name: "DEPLOY_KEY" }] });
});
afterEach(cleanup);

describe("SliceGitMirrorPanel", () => {
  it("sets up a mirror with a token secret", async () => {
    api.getSliceGitMirror.mockRejectedValue(new RpcError(404, { code: "not_found", message: "this slice has no Git mirror" }));
    const saved = {
      slice,
      url: "https://github.com/acme/app.git",
      branch: "main",
      tokenSecret: "GITHUB_MIRROR_TOKEN",
      root: "/acme/app",
      pending: true
    };
    api.setSliceGitMirror.mockResolvedValue(saved);
    renderPanel();

    const url = await screen.findByLabelText("Repository URL");
    fireEvent.change(url, { target: { value: "https://github.com/acme/app.git" } });
    await screen.findByRole("option", { name: "GITHUB_MIRROR_TOKEN" });
    fireEvent.change(screen.getByLabelText("Token secret"), { target: { value: "GITHUB_MIRROR_TOKEN" } });
    api.getSliceGitMirror.mockResolvedValue(saved);
    fireEvent.click(screen.getByRole("button", { name: "Start mirroring" }));

    expect(await screen.findByText("Saved. The first push starts in a moment.")).toBeInTheDocument();
    expect(api.setSliceGitMirror).toHaveBeenCalledWith({
      slice,
      url: "https://github.com/acme/app.git",
      branch: "",
      username: "",
      tokenSecret: "GITHUB_MIRROR_TOKEN"
    });
    expect(await screen.findByText("Push pending")).toBeInTheDocument();
    expect(screen.getByText("/acme/app")).toBeInTheDocument();
  });

  it("shows a failing push and pushes again on request", async () => {
    api.getSliceGitMirror.mockResolvedValue({
      slice,
      url: "https://github.com/acme/app.git",
      branch: "main",
      tokenSecret: "GITHUB_MIRROR_TOKEN",
      pending: true,
      failures: 2,
      lastError: "main has 1 commit(s) after abc that did not come from Gitslice",
      lastPushedCommit: "0123456789abcdef",
      lastPushedAt: "2026-11-13T12:00:00Z"
    });
    api.pushSliceGitMirror.mockResolvedValue({ slice, url: "https://github.com/acme/app.git", pending: true });
    renderPanel();

    expect(await screen.findByText("Push failing")).toBeInTheDocument();
    expect(screen.getByText(/did not come from Gitslice/)).toBeInTheDocument();
    expect(screen.getByText("0123456789ab")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Save changes" })).toBeDisabled();

    fireEvent.click(screen.getByRole("button", { name: "Push now" }));
    expect(await screen.findByText("Push queued.")).toBeInTheDocument();
    expect(api.pushSliceGitMirror).toHaveBeenCalledWith({ slice });
  });

  it("shows why saving failed", async () => {
    api.getSliceGitMirror.mockRejectedValue(new RpcError(404, { code: "not_found", message: "none" }));
    api.setSliceGitMirror.mockRejectedValue(new Error("url: the URL must start with https://"));
    renderPanel();
    fireEvent.change(await screen.findByLabelText("Repository URL"), { target: { value: "https://x.example/a.git" } });
    fireEvent.click(screen.getByRole("button", { name: "Start mirroring" }));
    expect(await screen.findByText("url: the URL must start with https://")).toBeInTheDocument();
  });
});
