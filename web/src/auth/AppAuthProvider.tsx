import { useAuth as useClerkAuth } from "@clerk/tanstack-react-start";
import { useQueryClient } from "@tanstack/react-query";
import {
  createContext,
  useCallback,
  useContext,
  useMemo,
  useRef,
  useState,
  type ReactNode
} from "react";

import {
  AGENT_AUTH_HEADER,
  isAgentSessionExpired,
  type AgentAuthResponseBody,
  type AgentSessionPublic
} from "../lib/agentSession";

export type AuthKind = "clerk" | "agent" | null;

export interface AppAuthState {
  agentAccount: string | null;
  agentExpiresAt: string | null;
  agentKeyId: string | null;
  authKind: AuthKind;
  getToken: () => Promise<string | null>;
  isLoaded: boolean;
  isSignedIn: boolean;
  signInWithAgentKey: (apiKey: string) => Promise<AgentSessionPublic>;
  signOut: () => Promise<void>;
  userId: string | null;
}

export const AppAuthContext = createContext<AppAuthState | null>(null);

interface AppAuthProviderProps {
  children: ReactNode;
  initialAgentSession?: AgentSessionPublic | null;
}

async function parseErrorMessage(response: Response, fallback: string): Promise<string> {
  try {
    const body = (await response.json()) as { error?: unknown };
    if (typeof body.error === "string" && body.error.trim()) {
      return body.error;
    }
  } catch {
    // Ignore parse errors and fall back to status text.
  }
  return fallback;
}

export function AppAuthProvider({
  children,
  initialAgentSession = null
}: AppAuthProviderProps) {
  const clerk = useClerkAuth();
  const queryClient = useQueryClient();
  const [agentSession, setAgentSession] = useState<AgentSessionPublic | null>(
    () =>
      initialAgentSession && !isAgentSessionExpired(initialAgentSession.expiresAt)
        ? initialAgentSession
        : null
  );
  // Kept strictly in memory for the lifetime of this tab; the HttpOnly
  // `gs_agent_session` cookie restores it across reloads via POST /auth/agent/token.
  const agentTokenRef = useRef<string | null>(null);
  const pendingTokenFetchRef = useRef<Promise<string | null> | null>(null);

  const fetchAgentToken = useCallback(async (): Promise<string | null> => {
    if (!agentSession) {
      return null;
    }
    if (isAgentSessionExpired(agentSession.expiresAt)) {
      agentTokenRef.current = null;
      setAgentSession(null);
      return null;
    }
    if (agentTokenRef.current) {
      return agentTokenRef.current;
    }
    if (pendingTokenFetchRef.current) {
      return pendingTokenFetchRef.current;
    }
    const request = (async () => {
      try {
        const response = await fetch("/auth/agent/token", {
          method: "POST",
          credentials: "same-origin",
          headers: {
            [AGENT_AUTH_HEADER]: "1"
          }
        });
        if (!response.ok) {
          if (response.status === 401) {
            agentTokenRef.current = null;
            setAgentSession(null);
          }
          return null;
        }
        const body = (await response.json()) as AgentAuthResponseBody;
        agentTokenRef.current = body.token;
        if (body.session) {
          setAgentSession(body.session);
        }
        return body.token;
      } catch {
        return null;
      } finally {
        pendingTokenFetchRef.current = null;
      }
    })();
    pendingTokenFetchRef.current = request;
    return request;
  }, [agentSession]);

  const signInWithAgentKey = useCallback(
    async (apiKey: string): Promise<AgentSessionPublic> => {
      const response = await fetch("/auth/agent/login", {
        method: "POST",
        credentials: "same-origin",
        headers: {
          "Content-Type": "application/json",
          [AGENT_AUTH_HEADER]: "1"
        },
        body: JSON.stringify({ apiKey })
      });
      if (!response.ok) {
        throw new Error(await parseErrorMessage(response, "Could not sign in with that API key."));
      }
      const body = (await response.json()) as AgentAuthResponseBody;
      agentTokenRef.current = body.token;
      setAgentSession(body.session);
      queryClient.clear();
      return body.session;
    },
    [queryClient]
  );

  const signOutAgent = useCallback(async (): Promise<void> => {
    try {
      await fetch("/auth/agent/logout", {
        method: "POST",
        credentials: "same-origin",
        headers: {
          [AGENT_AUTH_HEADER]: "1"
        }
      });
    } catch {
      // Clear local state even if the network request fails.
    }
    agentTokenRef.current = null;
    pendingTokenFetchRef.current = null;
    setAgentSession(null);
    queryClient.clear();
  }, [queryClient]);

  const clerkSignedIn = Boolean(clerk.isLoaded && clerk.isSignedIn);
  const agentActive = !clerkSignedIn && Boolean(agentSession);

  const getToken = useCallback(async (): Promise<string | null> => {
    if (clerkSignedIn) {
      return (await clerk.getToken()) ?? null;
    }
    if (agentActive) {
      return fetchAgentToken();
    }
    return null;
  }, [agentActive, clerk, clerkSignedIn, fetchAgentToken]);

  const signOut = useCallback(async (): Promise<void> => {
    if (clerkSignedIn) {
      await clerk.signOut();
      return;
    }
    if (agentActive) {
      await signOutAgent();
    }
  }, [agentActive, clerk, clerkSignedIn, signOutAgent]);

  const value = useMemo<AppAuthState>(() => {
    if (clerkSignedIn) {
      return {
        isLoaded: true,
        isSignedIn: true,
        authKind: "clerk",
        userId: clerk.userId ?? null,
        agentAccount: null,
        agentExpiresAt: null,
        agentKeyId: null,
        getToken,
        signInWithAgentKey,
        signOut
      };
    }
    if (agentActive && agentSession) {
      return {
        isLoaded: true,
        isSignedIn: true,
        authKind: "agent",
        userId: agentSession.subjectId,
        agentAccount: agentSession.account || null,
        agentExpiresAt: agentSession.expiresAt ?? null,
        agentKeyId: agentSession.keyId ?? null,
        getToken,
        signInWithAgentKey,
        signOut
      };
    }
    return {
      isLoaded: clerk.isLoaded,
      isSignedIn: false,
      authKind: null,
      userId: null,
      agentAccount: null,
      agentExpiresAt: null,
      agentKeyId: null,
      getToken,
      signInWithAgentKey,
      signOut
    };
  }, [agentActive, agentSession, clerk.isLoaded, clerk.userId, clerkSignedIn, getToken, signInWithAgentKey, signOut]);

  return <AppAuthContext.Provider value={value}>{children}</AppAuthContext.Provider>;
}

// useAppAuth returns the unified session state across both human Clerk sessions
// and agent API-key web sessions. When rendered outside AppAuthProvider (for
// example in isolated component unit tests that only mock Clerk), it falls back
// cleanly to Clerk's useAuth().
export function useAppAuth(): AppAuthState {
  const ctx = useContext(AppAuthContext);
  if (ctx) {
    return ctx;
  }
  const clerk = useClerkAuth();
  const isSignedIn = Boolean(clerk.isSignedIn);
  return {
    isLoaded: Boolean(clerk.isLoaded),
    isSignedIn,
    authKind: isSignedIn ? "clerk" : null,
    userId: clerk.userId ?? null,
    agentAccount: null,
    agentExpiresAt: null,
    agentKeyId: null,
    getToken: async () => (isSignedIn && typeof clerk.getToken === "function" ? (await clerk.getToken()) ?? null : null),
    signInWithAgentKey: async () => {
      throw new Error("AppAuthProvider is required to sign in as an agent.");
    },
    signOut: async () => {
      if (typeof clerk.signOut === "function") {
        await clerk.signOut();
      }
    }
  };
}
