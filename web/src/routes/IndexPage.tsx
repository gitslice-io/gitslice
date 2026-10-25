import { useAuth } from "@clerk/tanstack-react-start";

import { RequireAuth } from "../auth/RequireAuth";
import { AppShell } from "../components/AppShell";
import { SelectionProvider } from "../state/selection";
import { HomePage } from "./HomePage";
import { LandingPage } from "./LandingPage";
import { UsernameGate } from "./UsernameGate";

// The front page: Home for someone signed in, the landing page for anyone else.
export function IndexPage() {
  const { isLoaded, isSignedIn } = useAuth();

  if (isLoaded && isSignedIn) {
    return (
      <RequireAuth>
        <SelectionProvider>
          <UsernameGate>
            <AppShell>
              <HomePage />
            </AppShell>
          </UsernameGate>
        </SelectionProvider>
      </RequireAuth>
    );
  }

  return <LandingPage />;
}
