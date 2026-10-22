import { useSyncExternalStore } from "react";

import { formatDate, formatDateTime, formatFullTime, formatRelativeTime, formatUTCDate, parseTime } from "../lib/time";

const subscribe = () => () => undefined;

// True once the page runs in the browser. A server render (in UTC) and the
// hydration that follows it see false, so their text matches; then the time
// switches to the viewer's own zone.
function useInBrowser() {
  return useSyncExternalStore(
    subscribe,
    () => true,
    () => false
  );
}

/**
 * A time in the viewer's time zone: "Oct 10, 10:13 AM", "Oct 10" with
 * dateOnly, or "3h ago" with relative. Hovering shows the full local time with
 * its zone.
 */
export function Timestamp({
  className,
  dateOnly = false,
  relative = false,
  value
}: {
  className?: string;
  dateOnly?: boolean;
  relative?: boolean;
  value?: string | null;
}) {
  const inBrowser = useInBrowser();
  const date = parseTime(value);
  if (!date) {
    return null;
  }
  const text = !inBrowser
    ? formatUTCDate(date)
    : relative
      ? formatRelativeTime(date)
      : dateOnly
        ? formatDate(date)
        : formatDateTime(date);
  return (
    <time className={className} dateTime={date.toISOString()} title={inBrowser ? formatFullTime(date) : undefined}>
      {text}
    </time>
  );
}
