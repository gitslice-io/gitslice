// When the page has finished loading and the browser has a moment to spare.
// Analytics is not needed to show the page, and fetching its scripts (and
// session recorder) while the page's own are still arriving slows both on a
// slow connection. A visit shorter than this is not recorded.
export function afterPageLoad(idleTimeoutMs = 1500): Promise<void> {
  return new Promise((resolve) => {
    const idle = () => {
      if (typeof window.requestIdleCallback === "function") {
        window.requestIdleCallback(() => resolve(), { timeout: idleTimeoutMs });
      } else {
        window.setTimeout(resolve, Math.min(idleTimeoutMs, 1000));
      }
    };
    if (document.readyState === "complete") {
      idle();
    } else {
      window.addEventListener("load", idle, { once: true });
    }
  });
}
