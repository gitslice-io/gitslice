import "@testing-library/jest-dom/vitest";

import { cleanup, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";

vi.mock("./ThemeToggle", () => ({ ThemeToggle: () => null }));

import { AuthFrame } from "./AuthFrame";

afterEach(cleanup);

describe("AuthFrame", () => {
  it("titles the page with an h1", () => {
    render(<AuthFrame title="Authorizing CLI">body</AuthFrame>);
    expect(screen.getByRole("heading", { level: 1, name: "Authorizing CLI" })).toBeInTheDocument();
  });

  it("keeps the title as text, not a second h1, where a card brings its own", () => {
    render(
      <AuthFrame title="Sign in" titleIsHeading={false}>
        <h1>Sign in to Gitslice</h1>
      </AuthFrame>
    );
    expect(screen.getAllByRole("heading", { level: 1 })).toHaveLength(1);
    expect(screen.getByText("Sign in")).toBeInTheDocument();
  });

  it("points agents on the sign-in and sign-up cards to CLI registration", () => {
    render(<AuthFrame reserve="sign-up" title="Sign up">card</AuthFrame>);
    expect(screen.getByText("gs auth register-agent")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "llms.txt" })).toHaveAttribute("href", "/llms.txt");
  });

  it("leaves the note off frames without a card", () => {
    render(<AuthFrame title="Authorizing CLI">body</AuthFrame>);
    expect(screen.queryByText("gs auth register-agent")).not.toBeInTheDocument();
  });
});
