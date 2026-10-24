package server

import (
	"context"
	"net/http"
	"strings"

	"gitslice.io/gitslice/internal/usernames"
)

// avatarSource is the part of the auth service the picture handler needs.
type avatarSource interface {
	AccountAvatar(ctx context.Context, account string) ([]byte, string)
}

const (
	// A ?v= that names the picture the account shows now is cached for good:
	// a new upload has a new version, so a new address.
	avatarImmutableMaxAge = "public, max-age=31536000, immutable"
	// Without one (or with an old one), a change shows within minutes.
	avatarMaxAge = "public, max-age=300"
	// avatarGeneratedVersion is what clients send as ?v= for an account they
	// know shows its generated picture.
	avatarGeneratedVersion = "none"
)

// newAvatarImageHandler serves account pictures:
//
//	GET /avatars/{account}.png[?v=version]
//
// The picture is the account's upload, or one generated from the name when
// there is none, also for names no account has (an invitation's, say), so a
// client can always show something. Like the /og/ cards it is anonymous,
// since pictures are public, and the web app's Worker caches it at the edge.
func newAvatarImageHandler(avatars avatarSource) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		account, ok := avatarAccount(r.URL.Path)
		if !ok {
			http.NotFound(w, r)
			return
		}
		data, version := avatars.AccountAvatar(r.Context(), account)
		current := version
		if current == "" {
			current = avatarGeneratedVersion
		}
		maxAge := avatarMaxAge
		if r.URL.Query().Get("v") == current {
			maxAge = avatarImmutableMaxAge
		}
		w.Header().Set("Content-Type", "image/png")
		w.Header().Set("Cache-Control", maxAge)
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Content-Security-Policy", "default-src 'none'")
		if r.Method == http.MethodHead {
			return
		}
		_, _ = w.Write(data)
	})
}

// avatarAccount reads the account from /avatars/{account}.png. Account names
// are lowercase letters, digits and dashes; anything else is not a picture.
func avatarAccount(p string) (string, bool) {
	rest, ok := strings.CutPrefix(p, "/avatars/")
	if !ok {
		return "", false
	}
	account, ok := strings.CutSuffix(rest, ".png")
	if !ok || account == "" || len(account) > usernames.MaxLength {
		return "", false
	}
	for _, c := range account {
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-') {
			return "", false
		}
	}
	return account, true
}
