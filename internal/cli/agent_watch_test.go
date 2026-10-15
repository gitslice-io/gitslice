package cli

import (
	"testing"
	"time"
)

func TestInboundStale(t *testing.T) {
	base := time.Date(2026, 6, 29, 12, 0, 0, 0, time.UTC)
	timeout := 50 * time.Second
	tests := []struct {
		name    string
		last    time.Time
		now     time.Time
		timeout time.Duration
		want    bool
	}{
		{
			name:    "fresh",
			last:    base,
			now:     base.Add(49 * time.Second),
			timeout: timeout,
			want:    false,
		},
		{
			name:    "exact timeout",
			last:    base,
			now:     base.Add(timeout),
			timeout: timeout,
			want:    false,
		},
		{
			name:    "over timeout",
			last:    base,
			now:     base.Add(timeout + time.Nanosecond),
			timeout: timeout,
			want:    true,
		},
		{
			name:    "zero last seen",
			last:    time.Time{},
			now:     base.Add(timeout + time.Second),
			timeout: timeout,
			want:    false,
		},
		{
			name:    "clock moved backwards",
			last:    base,
			now:     base.Add(-time.Second),
			timeout: timeout,
			want:    false,
		},
		{
			name:    "nonpositive timeout",
			last:    base,
			now:     base.Add(time.Second),
			timeout: 0,
			want:    false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := inboundStale(tc.last, tc.now, tc.timeout)
			if got != tc.want {
				t.Fatalf("inboundStale() = %t, want %t", got, tc.want)
			}
		})
	}
}

func TestAgentInboundStateMarksReceivedMessage(t *testing.T) {
	base := time.Date(2026, 6, 29, 12, 0, 0, 0, time.UTC)
	inbound := newAgentInboundState(base)
	receivedAt := base.Add(30 * time.Second)

	inbound.mark(receivedAt)

	if got := inbound.lastSeen(); !got.Equal(receivedAt) {
		t.Fatalf("last inbound = %s, want %s", got, receivedAt)
	}
	if inboundStale(inbound.lastSeen(), receivedAt.Add(49*time.Second), agentInboundStaleTimeout) {
		t.Fatalf("recently marked inbound message should keep connection fresh")
	}
}

func TestAgentDaemonIsIdle(t *testing.T) {
	base := time.Date(2026, 8, 2, 12, 0, 0, 0, time.UTC)
	timeout := time.Hour
	tests := []struct {
		name         string
		lastActivity time.Time
		activeWork   int
		now          time.Time
		timeout      time.Duration
		want         bool
	}{
		{
			name:         "active work blocks idle",
			lastActivity: base,
			activeWork:   1,
			now:          base.Add(2 * timeout),
			timeout:      timeout,
			want:         false,
		},
		{
			name:         "disabled timeout",
			lastActivity: base,
			now:          base.Add(2 * timeout),
			timeout:      0,
			want:         false,
		},
		{
			name:    "zero activity time",
			now:     base.Add(2 * timeout),
			timeout: timeout,
			want:    false,
		},
		{
			name:         "exact timeout",
			lastActivity: base,
			now:          base.Add(timeout),
			timeout:      timeout,
			want:         false,
		},
		{
			name:         "inactive past timeout",
			lastActivity: base,
			now:          base.Add(timeout + time.Nanosecond),
			timeout:      timeout,
			want:         true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			d := &agentDaemon{
				lastActivity: tc.lastActivity,
				activeWork:   tc.activeWork,
			}
			if got := d.isIdle(tc.now, tc.timeout); got != tc.want {
				t.Fatalf("isIdle() = %t, want %t", got, tc.want)
			}
		})
	}
}

func TestAgentDaemonWorkActivityBalance(t *testing.T) {
	timeout := time.Hour
	d := &agentDaemon{}

	d.beginWork()
	if d.isIdle(time.Now().Add(10*timeout), timeout) {
		t.Fatalf("outstanding work should prevent idle shutdown")
	}

	d.endWork()
	if !d.isIdle(time.Now().Add(timeout+time.Second), timeout) {
		t.Fatalf("completed work should become idle after the timeout")
	}

	d.beginWork()
	if d.isIdle(time.Now().Add(10*timeout), timeout) {
		t.Fatalf("new outstanding work should prevent idle shutdown")
	}
	d.endWork()
	d.endWork()
	d.idleMu.Lock()
	activeWork := d.activeWork
	d.idleMu.Unlock()
	if activeWork != 0 {
		t.Fatalf("active work = %d after defensive extra end, want 0", activeWork)
	}
}
