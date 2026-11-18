import "@testing-library/jest-dom/vitest";

import { act, cleanup, render, renderHook, screen } from "@testing-library/react";
import { afterEach, describe, expect, it } from "vitest";

import { Button } from "./Button";
import { Input } from "./Input";
import { Select } from "./Select";
import { useCrudPanelState } from "./useCrudPanelState";

describe("UI primitives", () => {
  afterEach(() => {
    cleanup();
  });

  it("renders Button variants with type=button default and supports overrides", () => {
    render(
      <>
        <Button>Secondary</Button>
        <Button size="md" type="submit" variant="primary">
          Primary
        </Button>
        <Button variant="danger">Danger</Button>
        <Button size="xs" variant="ghost">
          Ghost
        </Button>
      </>
    );

    const secondary = screen.getByRole("button", { name: "Secondary" });
    expect(secondary).toHaveAttribute("type", "button");
    expect(secondary.className).toContain("border-slate-200");

    const primary = screen.getByRole("button", { name: "Primary" });
    expect(primary).toHaveAttribute("type", "submit");
    expect(primary.className).toContain("bg-zinc-950");

    const danger = screen.getByRole("button", { name: "Danger" });
    expect(danger.className).toContain("text-rose-700");

    const ghost = screen.getByRole("button", { name: "Ghost" });
    expect(ghost.className).toContain("bg-transparent");
  });

  it("renders Input and Select primitives with size and monospace options", () => {
    render(
      <>
        <Input aria-label="Mono input" monospace size="sm" />
        <Select aria-label="Role select" size="sm">
          <option value="writer">writer</option>
        </Select>
      </>
    );

    const input = screen.getByLabelText("Mono input");
    expect(input.className).toContain("font-mono");
    expect(input.className).toContain("h-7");

    const select = screen.getByLabelText("Role select");
    expect(select.className).toContain("h-8");
  });
});

describe("useCrudPanelState", () => {
  it("manages create, edit, delete, and error transitions", () => {
    const { result } = renderHook(() => useCrudPanelState<{ id: string; name: string }>());

    expect(result.current.editing).toBeNull();
    expect(result.current.deleting).toBeNull();
    expect(result.current.error).toBe("");

    act(() => result.current.handleError(new Error("boom")));
    expect(result.current.error).toBe("boom");

    act(() => result.current.startNew());
    expect(result.current.editing).toBe("new");
    expect(result.current.error).toBe("");

    act(() => result.current.startEditing("sec_1"));
    expect(result.current.editing).toBe("sec_1");

    act(() => result.current.requestDelete({ id: "sec_1", name: "TOKEN" }));
    expect(result.current.deleting).toEqual({ id: "sec_1", name: "TOKEN" });

    act(() => result.current.cancelDelete());
    expect(result.current.deleting).toBeNull();

    act(() => result.current.requestDelete({ id: "sec_1", name: "TOKEN" }));
    act(() => result.current.completeAction());
    expect(result.current.editing).toBeNull();
    expect(result.current.deleting).toBeNull();
    expect(result.current.error).toBe("");
  });
});
