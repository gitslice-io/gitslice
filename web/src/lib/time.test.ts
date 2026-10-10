import { afterEach, describe, expect, it, vi } from "vitest";

import { formatDate, formatDateTime, formatRelativeTime, formatUTCDate, parseTime } from "./time";

describe("local times", () => {
  afterEach(() => {
    vi.unstubAllEnvs();
  });

  const now = new Date("2026-10-10T18:00:00Z");

  it("reads relative times both ways and falls back to a local date", () => {
    expect(formatRelativeTime("2026-10-10T17:59:30Z", now)).toBe("just now");
    expect(formatRelativeTime("2026-10-10T17:55:00Z", now)).toBe("5m ago");
    expect(formatRelativeTime("2026-10-10T15:00:00Z", now)).toBe("3h ago");
    expect(formatRelativeTime("2026-10-08T18:00:00Z", now)).toBe("2d ago");
    expect(formatRelativeTime("2026-10-10T18:05:00Z", now)).toBe("in 5m");
    // A week or more: the date, not an ISO string.
    expect(formatRelativeTime("2026-09-01T12:00:00Z", now)).toBe(formatDate("2026-09-01T12:00:00Z", now));
    expect(formatRelativeTime("2026-09-01T12:00:00Z", now)).not.toMatch(/^\d{4}-\d{2}-\d{2}/);
    expect(formatRelativeTime(undefined, now)).toBe("unknown");
  });

  it("formats in the runtime's own time zone, not UTC", () => {
    const date = new Date("2026-10-10T17:13:45Z");
    const expected = new Intl.DateTimeFormat(undefined, { month: "short", day: "numeric", hour: "numeric", minute: "2-digit" }).format(date);
    expect(formatDateTime(date, now)).toBe(expected);
    expect(formatDateTime("2025-01-02T03:04:05Z", now)).toMatch(/2025/);
    expect(formatUTCDate("2026-10-10T23:30:00Z")).toBe("Oct 10, 2026");
    expect(parseTime("not a time")).toBeNull();
  });
});
