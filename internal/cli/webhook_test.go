package cli

import "testing"

func TestDisplayWebhookURLHidesQueryValues(t *testing.T) {
	for raw, want := range map[string]string{
		"https://hooks.example.com/release": "https://hooks.example.com/release",
		"https://cloudbuild.googleapis.com/v1/projects/p/triggers/t:webhook?key=AIza123&secret=abc": "https://cloudbuild.googleapis.com/v1/projects/p/triggers/t:webhook?key=...&secret=...",
		"https://hooks.example.com/x?token": "https://hooks.example.com/x?token=...",
	} {
		if got := displayWebhookURL(raw); got != want {
			t.Errorf("displayWebhookURL(%q) = %q, want %q", raw, got, want)
		}
	}
}
