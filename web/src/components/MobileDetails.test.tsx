import "@testing-library/jest-dom/vitest";

import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { useState } from "react";
import { afterEach, describe, expect, it } from "vitest";

import {
  MobileDetailsProvider,
  MobileDetailsToggle,
  useMobileDetailsClass
} from "./MobileDetails";
import { DataTable } from "./source/DataTable";
import { SourceCodeViewer } from "./source/SourceCodeViewer";

function Meta() {
  return <p className={useMobileDetailsClass()}>meta</p>;
}

function Page({ children }: { children: React.ReactNode }) {
  const [open, setOpen] = useState(false);
  return (
    <MobileDetailsProvider onToggle={() => setOpen((value) => !value)} open={open}>
      <MobileDetailsToggle />
      {children}
    </MobileDetailsProvider>
  );
}

describe("MobileDetails", () => {
  afterEach(() => {
    cleanup();
  });

  it("folds details on phones until the toggle opens them", () => {
    render(
      <Page>
        <Meta />
      </Page>
    );
    expect(screen.getByText("meta")).toHaveClass("max-lg:hidden");
    const toggle = screen.getByRole("button", { name: /Details/ });
    expect(toggle).toHaveAttribute("aria-expanded", "false");

    fireEvent.click(toggle);
    expect(screen.getByText("meta")).not.toHaveClass("max-lg:hidden");
    expect(screen.getByRole("button", { name: /Hide details/ })).toHaveAttribute("aria-expanded", "true");
  });

  it("shows everything outside a folding page", () => {
    render(<Meta />);
    expect(screen.getByText("meta").className).toBe("");
  });

  it("folds a viewer's path, counts and table summary but keeps Preview/Raw", () => {
    render(
      <Page>
        <SourceCodeViewer code={"a,b\n1,2\n"} path="/acme/data.csv" />
        <DataTable rows={[["a", "b"], ["1", "2"]]} summary="separated by commas" />
      </Page>
    );
    expect(screen.getByText("/acme/data.csv")).toHaveClass("max-lg:hidden");
    expect(screen.getByText("3 lines").parentElement).toHaveClass("max-lg:hidden");
    expect(screen.getByRole("button", { name: /raw/i })).toBeVisible();
    expect(screen.getAllByText(/separated by commas/)[0].closest("div")).toHaveClass("max-lg:hidden");
  });
});
