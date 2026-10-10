// Times are shown in the viewer's own time zone, in their locale's format.
// The API sends RFC 3339 times in UTC.

export function parseTime(value?: string | Date | null): Date | null {
  if (!value) {
    return null;
  }
  const date = value instanceof Date ? value : new Date(value);
  return Number.isNaN(date.getTime()) ? null : date;
}

const sameYearDateTime = new Intl.DateTimeFormat(undefined, {
  month: "short",
  day: "numeric",
  hour: "numeric",
  minute: "2-digit"
});
const otherYearDateTime = new Intl.DateTimeFormat(undefined, {
  year: "numeric",
  month: "short",
  day: "numeric",
  hour: "numeric",
  minute: "2-digit"
});
const sameYearDate = new Intl.DateTimeFormat(undefined, { month: "short", day: "numeric" });
const otherYearDate = new Intl.DateTimeFormat(undefined, { year: "numeric", month: "short", day: "numeric" });
const fullDateTime = new Intl.DateTimeFormat(undefined, {
  dateStyle: "full",
  timeStyle: "long"
});

/** "Oct 10, 10:13 AM" in the viewer's zone; the year shows when it is not this year's. */
export function formatDateTime(value?: string | Date | null, now = new Date()) {
  const date = parseTime(value);
  if (!date) {
    return "";
  }
  return (date.getFullYear() === now.getFullYear() ? sameYearDateTime : otherYearDateTime).format(date);
}

/** "Oct 10" (or "Oct 10, 2025") in the viewer's zone. */
export function formatDate(value?: string | Date | null, now = new Date()) {
  const date = parseTime(value);
  if (!date) {
    return "";
  }
  return (date.getFullYear() === now.getFullYear() ? sameYearDate : otherYearDate).format(date);
}

/** The whole local time with its zone, for hover titles. */
export function formatFullTime(value?: string | Date | null) {
  const date = parseTime(value);
  return date ? fullDateTime.format(date) : "";
}

/**
 * "just now", "5m ago", "3h ago", "2d ago", or the local date after a week.
 * Future times read "in 5m".
 */
export function formatRelativeTime(value?: string | Date | null, now = new Date()) {
  const date = parseTime(value);
  if (!date) {
    return "unknown";
  }
  const seconds = Math.round((now.getTime() - date.getTime()) / 1000);
  const future = seconds < 0;
  const abs = Math.abs(seconds);
  const say = (amount: string) => (future ? `in ${amount}` : `${amount} ago`);
  if (abs < 45) return future ? "in a moment" : "just now";
  const minutes = Math.round(abs / 60);
  if (minutes < 60) return say(`${minutes}m`);
  const hours = Math.round(minutes / 60);
  if (hours < 24) return say(`${hours}h`);
  const days = Math.round(hours / 24);
  if (days < 7) return say(`${days}d`);
  return formatDate(date, now);
}

const utcDate = new Intl.DateTimeFormat("en-US", {
  timeZone: "UTC",
  year: "numeric",
  month: "short",
  day: "numeric"
});

/** What a server render shows before the browser knows the viewer's zone. */
export function formatUTCDate(value?: string | Date | null) {
  const date = parseTime(value);
  return date ? utcDate.format(date) : "";
}
