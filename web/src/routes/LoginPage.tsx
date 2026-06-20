import { SignIn, useAuth } from "@clerk/clerk-react";
import { Navigate } from "@tanstack/react-router";
import { useEffect } from "react";

import { AuthFrame } from "../components/AuthFrame";
import { CLI_LOGIN_SEARCH_STORAGE_KEY } from "../auth/cliLogin";

const alexandriaClerkAppearance = {
  variables: {
    borderRadius: "0.375rem",
    colorBackground: "#fbfaf6",
    colorDanger: "#b42318",
    colorInputBackground: "#ffffff",
    colorInputText: "#211d17",
    colorPrimary: "#094cb2",
    colorText: "#211d17",
    colorTextSecondary: "#625b51",
    fontFamily: "Inter, ui-sans-serif, system-ui, sans-serif",
    fontFamilyButtons: "Public Sans, Inter, ui-sans-serif, system-ui, sans-serif"
  },
  elements: {
    card: "bg-transparent p-0 shadow-none",
    cardBox: "w-full bg-transparent shadow-none",
    dividerLine: "bg-outline-variant/15",
    dividerText: "font-label text-on-surface-muted",
    footerActionLink: "font-label font-semibold text-primary hover:text-primary",
    footerActionText: "text-on-surface-variant",
    formButtonPrimary:
      "h-10 rounded-sm bg-gradient-to-r from-primary to-primary-container font-label text-sm font-semibold text-white shadow-none transition hover:to-primary active:translate-y-px",
    formFieldAction: "font-label font-semibold text-primary",
    formFieldInput:
      "rounded-sm bg-white text-on-surface shadow-none ring-1 ring-outline-variant/15 focus:ring-2 focus:ring-primary",
    formFieldLabel: "font-label text-xs font-semibold text-on-surface",
    header: "items-start text-left",
    headerSubtitle: "text-sm leading-6 text-on-surface-variant",
    headerTitle:
      "font-serif text-2xl font-semibold leading-tight text-on-surface",
    identityPreview: "rounded-sm bg-surface-container-low",
    main: "gap-5",
    rootBox: "w-full",
    socialButtonsBlockButton:
      "rounded-sm bg-surface-container-high font-label font-semibold text-primary shadow-none transition hover:bg-surface-container-highest",
    socialButtonsBlockButtonText: "font-label"
  }
};

export function LoginPage() {
  const { isLoaded, isSignedIn } = useAuth();
  const pendingCliLoginSearch = sessionStorage.getItem(
    CLI_LOGIN_SEARCH_STORAGE_KEY
  );

  useEffect(() => {
    if (!isLoaded || !isSignedIn || !pendingCliLoginSearch) {
      return;
    }

    sessionStorage.removeItem(CLI_LOGIN_SEARCH_STORAGE_KEY);
    window.location.replace(`/cli-login${pendingCliLoginSearch}`);
  }, [isLoaded, isSignedIn, pendingCliLoginSearch]);

  if (!isLoaded) {
    return (
      <AuthFrame title="Sign in">
        <p>Loading session...</p>
      </AuthFrame>
    );
  }

  if (isSignedIn && pendingCliLoginSearch) {
    return (
      <AuthFrame title="Authorizing CLI">
        <p>Returning to CLI login...</p>
      </AuthFrame>
    );
  }

  if (isSignedIn) {
    return <Navigate replace to="/" />;
  }

  return (
    <AuthFrame title="Sign in">
      <SignIn
        appearance={alexandriaClerkAppearance}
        path="/login"
        routing="path"
      />
    </AuthFrame>
  );
}
