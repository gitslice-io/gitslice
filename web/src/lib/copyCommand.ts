let copyListenerRegistered = false;
let lastCopyMessage = "";
const copyTimers = new WeakMap<HTMLElement, ReturnType<typeof setTimeout>>();

function reportCopyResult(button: HTMLElement, ok: boolean) {
  const tip = button.nextElementSibling;
  const existingTimer = copyTimers.get(button);
  if (existingTimer) {
    clearTimeout(existingTimer);
  }
  button.removeAttribute("data-copied");
  button.removeAttribute("data-failed");

  let message = ok ? "Copied" : "Copy failed";
  if (message === lastCopyMessage) {
    // VoiceOver skips identical consecutive announcements even after clearing.
    message += "\u00A0";
  }
  lastCopyMessage = message;

  if (tip) {
    tip.textContent = "";
  }
  void button.offsetWidth;
  button.setAttribute(ok ? "data-copied" : "data-failed", "");

  const announce = () => {
    if (tip) {
      tip.textContent = message;
    }
  };
  if (typeof requestAnimationFrame === "function") {
    requestAnimationFrame(announce);
  } else {
    announce();
  }

  copyTimers.set(
    button,
    setTimeout(() => {
      button.removeAttribute("data-copied");
      button.removeAttribute("data-failed");
      if (tip) {
        tip.textContent = "";
      }
    }, 2000)
  );
}

async function copyElementText(button: HTMLElement, target: HTMLElement) {
  const raw = target.innerText ?? target.textContent ?? "";
  const text = raw.trim();
  if (!text) return;
  try {
    await navigator.clipboard.writeText(text);
    reportCopyResult(button, true);
  } catch {
    reportCopyResult(button, false);
  }
}

export function ensureCopyCommandListener() {
  if (copyListenerRegistered || typeof window === "undefined" || typeof document === "undefined") {
    return;
  }
  copyListenerRegistered = true;

  if ("CommandEvent" in window) {
    document.addEventListener(
      "command",
      (event) => {
        const commandEvent = event as Event & {
          command?: string;
          source?: Element | null;
        };
        if (
          commandEvent.command === "--copy" &&
          commandEvent.source instanceof HTMLElement &&
          commandEvent.target instanceof HTMLElement
        ) {
          void copyElementText(commandEvent.source, commandEvent.target);
        }
      },
      true
    );
  } else {
    document.addEventListener("click", (event) => {
      const origin = event.target;
      if (!(origin instanceof Element)) return;
      const button = origin.closest<HTMLButtonElement>('button[command="--copy"]');
      if (!button) return;
      const targetId = button.getAttribute("commandfor");
      if (!targetId) return;
      const target = document.getElementById(targetId);
      if (target) {
        void copyElementText(button, target);
      }
    });
  }
}
