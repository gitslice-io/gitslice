package server

import (
	"context"
	"log/slog"
	"net/http"
	"strings"

	"gitslice.io/gitslice/internal/socialcard"
	corev1 "gitslice.io/gitslice/proto/core/v1"
)

// sliceResolver is the part of the slice service the card needs.
type sliceResolver interface {
	ResolveSlice(ctx context.Context, req *corev1.ResolveSliceRequest) (*corev1.Slice, error)
}

const (
	socialImageMaxAge   = "public, max-age=86400"
	socialGenericMaxAge = "public, max-age=3600"
)

// newSocialImageHandler serves the preview images shared links show:
//
//	GET /og/slices/{account}/{slice}.png  the slice's card
//	GET /og/default.png                   the generic Gitslice card
//
// It is mounted without the auth middleware, so it always asks as an
// anonymous reader: only a public slice gets its own card, and a private,
// archived or missing one gets the generic card, which tells nothing about it.
// The web app's Worker proxies these and caches them at the edge.
func newSocialImageHandler(slices sliceResolver) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		card, maxAge := socialcard.Card{}, socialGenericMaxAge
		if account, slice, ok := socialImageSlice(r.URL.Path); ok {
			resolved, err := slices.ResolveSlice(r.Context(), &corev1.ResolveSliceRequest{
				Ref: &corev1.SliceRef{Account: account, Slice: slice},
			})
			if err == nil && resolved.GetDefinition().GetVisibility() == "public" && !resolved.GetArchived() {
				ref := resolved.GetRef()
				card = socialcard.Card{
					Account:     ref.GetAccount(),
					Slice:       ref.GetSlice(),
					Description: resolved.GetDescription(),
					Facts:       []string{"Public slice", "gitslice.io/slices/" + ref.GetAccount() + "/" + ref.GetSlice()},
				}
				maxAge = socialImageMaxAge
			}
		} else if r.URL.Path != "/og/default.png" {
			http.NotFound(w, r)
			return
		}
		data, err := socialcard.Render(card)
		if err != nil {
			slog.Error("render social card", "card", card.Account+"/"+card.Slice, "error", err)
			http.Error(w, "could not draw the card", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "image/png")
		w.Header().Set("Cache-Control", maxAge)
		w.Header().Set("X-Content-Type-Options", "nosniff")
		if r.Method == http.MethodHead {
			return
		}
		_, _ = w.Write(data)
	})
}

// socialImageSlice reads the account and slice from /og/slices/{account}/{slice}.png.
func socialImageSlice(p string) (string, string, bool) {
	rest, ok := strings.CutPrefix(p, "/og/slices/")
	if !ok {
		return "", "", false
	}
	account, file, ok := strings.Cut(rest, "/")
	slice, isPNG := strings.CutSuffix(file, ".png")
	if !ok || !isPNG || account == "" || slice == "" || strings.Contains(slice, "/") {
		return "", "", false
	}
	return account, slice, true
}
