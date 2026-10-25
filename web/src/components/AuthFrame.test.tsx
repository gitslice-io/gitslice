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
});
