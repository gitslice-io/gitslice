// @vitest-environment jsdom
import "@testing-library/jest-dom/vitest";

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";

const clerkAuth = vi.hoisted(() => ({
  current: {
    getToken: vi.fn<() => Promise<string | null>>().mockResolvedValue(null),
    isLoaded: true,
    isSignedIn: false,
    signOut: vi.fn<() => Promise<void>>().mockResolvedValue(undefined),
    userId: null as string | null
  }
}));

vi.mock("@clerk/tanstack-react-start", () => ({
  useAuth: () => clerkAuth.current
}));

import { AppAuthProvider, useAppAuth } from "./AppAuthProvider";

function AuthProbe() {
  const auth = useAppAuth();
  return (
    <div>
      <span data-testid="kind">{auth.authKind ?? "none"}</span>
      <span data-testid="signed-in">{String(auth.isSignedIn)}</span>
      <span data-testid="account">{auth.agentAccount ?? ""}</span>
      <button onClick={() => void auth.signInWithAgentKey("gsk_root_key")} type="button">
        Sign in agent
      </button>
      <button onClick={() => void auth.signOut()} type="button">
        Sign out
      </button>
      <button
        onClick={() => {
          void auth.getToken().then((token) => {
            const el = document.getElementById("token-out");
            if (el) el.textContent = token ?? "null";
          });
        }}
        type="button"
      >
        Fetch token
      </button>
      <span id="token-out" />
    </div>
  );
}

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
});

describe("AppAuthProvider", () => {
  it("hydrates an SSR agent session and fetches the token from /auth/agent/token on demand", async () => {
    const fetchMock = vi.spyOn(globalThis, "fetch").mockResolvedValueOnce(
      new Response(
        JSON.stringify({
          session: {
            subjectId: "agent_01",
            account: "release-bot",
            expiresAt: "2099-01-01T00:00:00Z",
            keyId: "key_web_1"
          },
          token: "gsk_web_child_1"
        }),
        { status: 200 }
      )
    );

    const qc = new QueryClient();
    render(
      <QueryClientProvider client={qc}>
        <AppAuthProvider
          initialAgentSession={{
            subjectId: "agent_01",
            account: "release-bot",
            expiresAt: "2099-01-01T00:00:00Z",
            keyId: "key_web_1"
          }}
        >
          <AuthProbe />
        </AppAuthProvider>
      </QueryClientProvider>
    );

    expect(screen.getByTestId("kind")).toHaveTextContent("agent");
    expect(screen.getByTestId("signed-in")).toHaveTextContent("true");
    expect(screen.getByTestId("account")).toHaveTextContent("release-bot");

    fireEvent.click(screen.getByRole("button", { name: "Fetch token" }));
    await waitFor(() => {
      expect(document.getElementById("token-out")).toHaveTextContent("gsk_web_child_1");
    });
    expect(fetchMock).toHaveBeenCalledWith(
      "/auth/agent/token",
      expect.objectContaining({ method: "POST" })
    );
  });

  it("signs in with an API key and signs out cleanly", async () => {
    const fetchMock = vi
      .spyOn(globalThis, "fetch")
      .mockResolvedValueOnce(
        new Response(
          JSON.stringify({
            session: {
              subjectId: "agent_02",
              account: "triage-bot",
              expiresAt: "2099-01-01T00:00:00Z"
            },
            token: "gsk_web_child_2"
          }),
          { status: 200 }
        )
      )
      .mockResolvedValueOnce(new Response(JSON.stringify({ ok: true }), { status: 200 }));

    const qc = new QueryClient();
    render(
      <QueryClientProvider client={qc}>
        <AppAuthProvider>
          <AuthProbe />
        </AppAuthProvider>
      </QueryClientProvider>
    );

    expect(screen.getByTestId("kind")).toHaveTextContent("none");
    fireEvent.click(screen.getByRole("button", { name: "Sign in agent" }));

    await waitFor(() => {
      expect(screen.getByTestId("kind")).toHaveTextContent("agent");
      expect(screen.getByTestId("account")).toHaveTextContent("triage-bot");
    });

    fireEvent.click(screen.getByRole("button", { name: "Sign out" }));
    await waitFor(() => {
      expect(screen.getByTestId("kind")).toHaveTextContent("none");
    });
    expect(fetchMock).toHaveBeenLastCalledWith(
      "/auth/agent/logout",
      expect.objectContaining({ method: "POST" })
    );
  });
});
