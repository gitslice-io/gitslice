import {
  useCallback,
  useEffect,
  useRef,
  type PointerEvent as ReactPointerEvent,
  type SyntheticEvent
} from "react";

interface UseNativeDialogOptions {
  open: boolean;
  onClose(): void;
}

function isPointerOutsideElement(
  element: HTMLElement,
  clientX: number,
  clientY: number
): boolean {
  const rect = element.getBoundingClientRect();
  if (rect.width === 0 && rect.height === 0) {
    return false;
  }
  return (
    clientX < rect.left ||
    clientX > rect.right ||
    clientY < rect.top ||
    clientY > rect.bottom
  );
}

function supportsNativeClosedBy(): boolean {
  return (
    typeof HTMLDialogElement !== "undefined" &&
    "closedBy" in HTMLDialogElement.prototype
  );
}

/**
 * Manages a native `<dialog>` modal lifecycle (`showModal`, `closedby="any"`,
 * Safari pointer-rect light-dismiss fallback, `returnValue` reset on `close`,
 * and jsdom fallback when `showModal` is unavailable).
 */
export function useNativeDialog({ open, onClose }: UseNativeDialogOptions) {
  const dialogRef = useRef<HTMLDialogElement>(null);
  const pointerDownOutsideRef = useRef(false);
  const closingProgrammaticallyRef = useRef(false);
  const closedNotifiedRef = useRef(false);

  const notifyClose = useCallback(() => {
    if (closedNotifiedRef.current) {
      return;
    }
    closedNotifiedRef.current = true;
    onClose();
  }, [onClose]);

  useEffect(() => {
    const dialog = dialogRef.current;
    if (!dialog) {
      return;
    }

    if (!open) {
      if (dialog.open || dialog.hasAttribute("open")) {
        closingProgrammaticallyRef.current = true;
        if (typeof dialog.close === "function") {
          dialog.close();
        } else {
          dialog.removeAttribute("open");
        }
        closingProgrammaticallyRef.current = false;
      }
      return;
    }

    closedNotifiedRef.current = false;
    pointerDownOutsideRef.current = false;

    const supportsShowModal = typeof dialog.showModal === "function";
    if (!dialog.open) {
      if (supportsShowModal) {
        dialog.showModal();
      } else {
        dialog.setAttribute("open", "");
      }
    }

    if (supportsShowModal) {
      return () => {
        if (dialog.open && typeof dialog.close === "function") {
          closingProgrammaticallyRef.current = true;
          dialog.close();
          closingProgrammaticallyRef.current = false;
        }
      };
    }

    // jsdom fallback: real browsers handle Escape natively via top-layer <dialog>.
    function handleWindowKeyDown(event: KeyboardEvent) {
      if (event.key !== "Escape") {
        return;
      }
      const openDialogs = document.querySelectorAll("dialog[open]");
      if (openDialogs[openDialogs.length - 1] === dialog) {
        notifyClose();
      }
    }

    window.addEventListener("keydown", handleWindowKeyDown);
    return () => {
      window.removeEventListener("keydown", handleWindowKeyDown);
      if (dialog.hasAttribute("open")) {
        dialog.removeAttribute("open");
      }
    };
  }, [notifyClose, open]);

  const handleDialogClose = useCallback(
    (event: SyntheticEvent<HTMLDialogElement>) => {
      event.currentTarget.returnValue = "";
      if (!closingProgrammaticallyRef.current) {
        notifyClose();
      }
    },
    [notifyClose]
  );

  const handleDialogCancel = useCallback(
    (event: SyntheticEvent<HTMLDialogElement>) => {
      event.preventDefault();
      notifyClose();
    },
    [notifyClose]
  );

  const handlePointerDown = useCallback(
    (event: ReactPointerEvent<HTMLDialogElement>) => {
      if (supportsNativeClosedBy()) {
        pointerDownOutsideRef.current = false;
        return;
      }
      pointerDownOutsideRef.current = isPointerOutsideElement(
        event.currentTarget,
        event.clientX,
        event.clientY
      );
    },
    []
  );

  const handlePointerUp = useCallback(
    (event: ReactPointerEvent<HTMLDialogElement>) => {
      if (supportsNativeClosedBy()) {
        pointerDownOutsideRef.current = false;
        return;
      }
      const startedOutside = pointerDownOutsideRef.current;
      pointerDownOutsideRef.current = false;
      if (
        startedOutside &&
        isPointerOutsideElement(event.currentTarget, event.clientX, event.clientY)
      ) {
        notifyClose();
      }
    },
    [notifyClose]
  );

  return {
    dialogRef,
    dialogProps: {
      ...({ closedby: "any" } as Record<string, unknown>),
      onCancel: handleDialogCancel,
      onClose: handleDialogClose,
      onPointerDown: handlePointerDown,
      onPointerUp: handlePointerUp
    },
    requestClose: notifyClose
  };
}
