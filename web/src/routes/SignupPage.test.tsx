import "@testing-library/jest-dom/vitest";

import { cleanup, render, screen } from "@testing-library/react";
import type { ReactNode } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";

const auth = vi.hoisted(() => ({ current: { isLoaded: true, isSignedIn: false } }));
const signUpProps = vi.hoisted(() => ({ current: {} as Record<string, unknown> }));

vi.mock("@clerk/tanstack-react-start", () => ({
  useAuth: () => auth.current,
  SignUp: (props: Record<string, unknown>) => {
    signUpProps.current = props;
    return <div>clerk sign-up</div>;
  }
}));
vi.mock("@tanstack/react-router", () => ({
  Navigate: ({ to }: { to: string }) => <div>redirect to {to}</div>
}));
vi.mock("../components/AuthFrame", () => ({
  AuthFrame: ({ children, title }: { children: ReactNode; title: string }) => (
    <section>
      <h1>{title}</h1>
      {children}
    </section>
  )
}));

import { SignupPage } from "./SignupPage";

afterEach(cleanup);

describe("SignupPage", () => {
  it("shows Clerk's sign-up at /signup, linking back to sign-in", () => {
    auth.current = { isLoaded: true, isSignedIn: false };
    render(<SignupPage />);
    expect(screen.getByRole("heading", { name: "Sign up" })).toBeInTheDocument();
    expect(screen.getByText("clerk sign-up")).toBeInTheDocument();
    expect(signUpProps.current).toMatchObject({ path: "/signup", routing: "path", signInUrl: "/login" });
  });

  it("sends someone already signed in home", () => {
    auth.current = { isLoaded: true, isSignedIn: true };
    render(<SignupPage />);
    expect(screen.getByText("redirect to /")).toBeInTheDocument();
  });
});
