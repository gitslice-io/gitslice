import "@testing-library/jest-dom/vitest";

import { act, cleanup, fireEvent, render, renderHook, screen } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";

import { useCopyToClipboard } from "../../lib/useCopyToClipboard";
import { Button } from "./Button";
import { CopyButton } from "./CopyButton";
import { Drawer } from "./Drawer";
import { Input } from "./Input";
import { ModalDialog } from "./ModalDialog";
import { Select } from "./Select";
import { useCrudPanelState } from "./useCrudPanelState";

describe("UI primitives", () => {
  afterEach(() => {
    vi.useRealTimers();
    vi.restoreAllMocks();
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

describe("ModalDialog and Drawer", () => {
  afterEach(() => {
    vi.restoreAllMocks();
    cleanup();
  });

  it("uses open attribute fallback in jsdom, exposes closedby=any, and closes on close button or Escape", () => {
    const onClose = vi.fn();
    const { rerender } = render(
      <ModalDialog onClose={onClose} open={false} title="Hidden dialog">
        <p>Body</p>
      </ModalDialog>
    );
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();

    rerender(
      <ModalDialog onClose={onClose} open title="Confirm run">
        <p>Run details</p>
      </ModalDialog>
    );

    const dialog = screen.getByRole("dialog", { name: "Confirm run" });
    expect(dialog).toHaveAttribute("open");
    expect(dialog).toHaveAttribute("closedby", "any");
    expect(dialog).not.toHaveAttribute("aria-modal");
    expect(dialog).not.toHaveAttribute("role");

    fireEvent.keyDown(window, { key: "Escape" });
    expect(onClose).toHaveBeenCalledTimes(1);

    rerender(
      <ModalDialog onClose={onClose} open={false} title="Confirm run">
        <p>Run details</p>
      </ModalDialog>
    );
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
  });

  it("calls showModal when supported, resets returnValue on close, and closes only topmost dialog on Escape", () => {
    const showModalSpy = vi.fn(function (this: HTMLDialogElement) {
      this.setAttribute("open", "");
    });
    const closeSpy = vi.fn(function (this: HTMLDialogElement) {
      this.removeAttribute("open");
    });
    Object.defineProperty(HTMLDialogElement.prototype, "showModal", {
      configurable: true,
      value: showModalSpy
    });
    Object.defineProperty(HTMLDialogElement.prototype, "close", {
      configurable: true,
      value: closeSpy
    });

    try {
      const onClose = vi.fn();
      render(
        <Drawer closeLabel="Close tags" onClose={onClose} open title="Tags">
          <div>Tag list</div>
        </Drawer>
      );

      const dialog = screen.getByRole("dialog", { name: "Tags" }) as HTMLDialogElement;
      expect(showModalSpy).toHaveBeenCalledTimes(1);

      dialog.returnValue = "confirm";
      fireEvent(dialog, new Event("close"));
      expect(dialog.returnValue).toBe("");
      expect(onClose).toHaveBeenCalledTimes(1);
    } finally {
      Reflect.deleteProperty(HTMLDialogElement.prototype, "showModal");
      Reflect.deleteProperty(HTMLDialogElement.prototype, "close");
    }
  });

  it("supports Safari light-dismiss pointerdown/pointerup outside dialog bounds without dismissing on drags from inside", () => {
    const onClose = vi.fn();
    render(
      <Drawer closeLabel="Close policy" onClose={onClose} open size="xl" title="Folder policy">
        <div>Content</div>
      </Drawer>
    );

    const dialog = screen.getByRole("dialog", { name: "Folder policy" });
    vi.spyOn(dialog, "getBoundingClientRect").mockReturnValue({
      x: 400,
      y: 0,
      left: 400,
      top: 0,
      right: 900,
      bottom: 800,
      width: 500,
      height: 800,
      toJSON: () => ({})
    });

    // Click inside dialog bounds: must stay open.
    fireEvent.pointerDown(dialog, { clientX: 500, clientY: 100 });
    fireEvent.pointerUp(dialog, { clientX: 500, clientY: 100 });
    expect(onClose).not.toHaveBeenCalled();

    // Text-selection drag starting inside and ending on backdrop: must stay open.
    fireEvent.pointerDown(dialog, { clientX: 500, clientY: 100 });
    fireEvent.pointerUp(dialog, { clientX: 100, clientY: 100 });
    expect(onClose).not.toHaveBeenCalled();

    // Press and release outside bounds on backdrop: dismisses once.
    fireEvent.pointerDown(dialog, { clientX: 100, clientY: 100 });
    fireEvent.pointerUp(dialog, { clientX: 100, clientY: 100 });
    expect(onClose).toHaveBeenCalledTimes(1);
  });
});

describe("CopyButton and useCopyToClipboard", () => {
  afterEach(() => {
    vi.useRealTimers();
    vi.restoreAllMocks();
    cleanup();
  });

  it("shows Copied feedback without disabling the button, updates off-button role=status, and resets", async () => {
    vi.useFakeTimers();
    const writeText = vi.fn().mockResolvedValue(undefined);
    Object.defineProperty(navigator, "clipboard", {
      configurable: true,
      value: { writeText }
    });

    render(<CopyButton ariaLabel="Copy command" value="gs init ." />);

    const button = screen.getByRole("button", { name: "Copy command" });
    const statusRegion = screen.getByRole("status");
    expect(statusRegion).not.toBe(button);
    expect(button.contains(statusRegion)).toBe(false);

    await act(async () => {
      fireEvent.click(button);
      await Promise.resolve();
    });

    expect(writeText).toHaveBeenCalledWith("gs init .");
    expect(button).toHaveTextContent("Copied");
    expect(button).toBeEnabled();
    expect(statusRegion).toHaveTextContent("Copied");

    act(() => {
      vi.advanceTimersByTime(1800);
    });
    expect(button).toHaveTextContent("Copy");
    expect(statusRegion).toHaveTextContent("");
  });

  it("shows Copy failed when clipboard write rejects and deduplicates repeated announcements", async () => {
    vi.useFakeTimers();
    const writeText = vi
      .fn()
      .mockRejectedValueOnce(new Error("Permission denied"))
      .mockResolvedValueOnce(undefined)
      .mockResolvedValueOnce(undefined);
    Object.defineProperty(navigator, "clipboard", {
      configurable: true,
      value: { writeText }
    });

    const { result } = renderHook(() => useCopyToClipboard({ resetMs: 1500 }));

    let ok = true;
    await act(async () => {
      ok = await result.current.copy("secret");
    });
    expect(ok).toBe(false);
    expect(result.current.status).toBe("failed");
    expect(result.current.announcement).toBe("Copy failed");

    await act(async () => {
      ok = await result.current.copy("secret");
    });
    expect(ok).toBe(true);
    expect(result.current.status).toBe("copied");
    expect(result.current.announcement).toBe("Copied\u00A0");

    await act(async () => {
      await result.current.copy("secret");
    });
    expect(result.current.announcement).toBe("Copied");
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
