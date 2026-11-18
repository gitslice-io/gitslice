package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gitslice.io/gitslice/internal/intents"
	"gitslice.io/gitslice/internal/paths"
	corev1 "gitslice.io/gitslice/proto/core/v1"
)

func (s *Session) resolveIntentTarget(sliceFlag, rawTarget string) (*corev1.SliceRef, string, error) {
	raw := strings.TrimSpace(rawTarget)
	if raw == "" {
		return nil, "", userError("intent_pattern_required", "path or glob pattern is required", "Usage: gs intent '<account>/<slice>/<glob>'")
	}
	if strings.Contains(raw, `\`) {
		return nil, "", userError("invalid_intent_pattern", "intent pattern cannot contain backslashes", "Use forward slashes '/' in intent patterns.")
	}
	if strings.HasPrefix(raw, "!") {
		return nil, "", userError("invalid_intent_pattern", "intent pattern negation ('!') is not supported", "Pass a positive path or glob pattern.")
	}

	// 1. Explicit --slice flag.
	if strings.TrimSpace(sliceFlag) != "" {
		sliceRef, err := s.ResolveSliceRef(sliceFlag, false)
		if err != nil {
			return nil, "", err
		}
		pat, err := s.expandPatternAgainstSlice(sliceRef, nil, raw)
		return sliceRef, pat, err
	}

	ws, wsErr := s.r.readWorkspaceConfig()
	hasWS := wsErr == nil && strings.TrimSpace(ws.Account) != "" && strings.TrimSpace(ws.Slice) != ""

	// 2. Canonical path starting with '/'.
	if strings.HasPrefix(raw, "/") {
		if hasWS {
			if _, vErr := intents.ValidateSlicePattern(raw, ws.IncludedPaths); vErr == nil {
				return &corev1.SliceRef{Account: ws.Account, Slice: ws.Slice}, raw, nil
			}
		}
		segs := splitNonEmptySlash(raw)
		if len(segs) < 2 {
			return nil, "", userError("invalid_intent_pattern", "canonical intent pattern must start with /<account>/<slice>", "Example: gs intent '/acme/payment/src/**'")
		}
		sliceRef, err := parseSliceRef(segs[0] + ":" + segs[1])
		if err != nil {
			return nil, "", err
		}
		return sliceRef, raw, nil
	}

	// 3. Explicit account:slice/... syntax with colon.
	firstSeg, rest, hasSlash := strings.Cut(raw, "/")
	if strings.Contains(firstSeg, ":") {
		sliceRef, err := parseSliceRef(firstSeg)
		if err != nil {
			return nil, "", err
		}
		if !hasSlash || strings.TrimSpace(rest) == "" {
			pat, err := s.expandPatternAgainstSlice(sliceRef, nil, "**")
			return sliceRef, pat, err
		}
		pat, err := s.expandPatternAgainstSlice(sliceRef, nil, rest)
		return sliceRef, pat, err
	}

	// 4. Inside a workspace checkout.
	if hasWS {
		wsRef := &corev1.SliceRef{Account: ws.Account, Slice: ws.Slice}
		if _, vErr := intents.ValidateSlicePattern("/"+raw, ws.IncludedPaths); vErr == nil {
			return wsRef, "/" + raw, nil
		}
		if raw == ws.Account+"/"+ws.Slice {
			return wsRef, "/" + raw + "/**", nil
		}

		segs := splitNonEmptySlash(raw)
		if len(segs) >= 3 && !strings.ContainsAny(segs[0], "*?[{.") && !strings.ContainsAny(segs[1], "*?[{.") {
			if !s.r.localSegmentExistsInWorkspace(ws, segs[0]) {
				if otherRef, pErr := parseSliceRef(segs[0] + ":" + segs[1]); pErr == nil {
					if otherSlice, sErr := s.Slice().ResolveSlice(s.Ctx(), &corev1.ResolveSliceRequest{Ref: otherRef}); sErr == nil && otherSlice != nil {
						var inc []string
						if otherSlice.GetDefinition() != nil {
							inc = otherSlice.GetDefinition().GetIncludedPaths()
						}
						if _, vErr := intents.ValidateSlicePattern("/"+raw, inc); vErr == nil {
							return otherRef, "/" + raw, nil
						}
					}
				}
			}
		}

		pat, err := s.expandPatternAgainstSlice(wsRef, ws.IncludedPaths, raw)
		return wsRef, pat, err
	}

	// 5. Outside any workspace: expect '<account>/<slice>/<glob>' (or '<account>/<slice>').
	segs := splitNonEmptySlash(raw)
	if len(segs) < 2 {
		return nil, "", userError(
			"slice_required",
			"outside a workspace, pattern must start with <account>/<slice>/",
			"Example: gs intent 'acme/payment/src/billing/**' --for 2h -m \"refactor\"",
		)
	}
	sliceRef, err := parseSliceRef(segs[0] + ":" + segs[1])
	if err != nil {
		return nil, "", err
	}
	if len(segs) == 2 && !strings.HasSuffix(raw, "/") {
		return sliceRef, "/" + segs[0] + "/" + segs[1] + "/**", nil
	}
	return sliceRef, "/" + raw, nil
}

func (s *Session) expandPatternAgainstSlice(sliceRef *corev1.SliceRef, knownIncluded []string, raw string) (string, error) {
	if strings.HasPrefix(raw, "/") {
		return raw, nil
	}
	trailingSlash := strings.HasSuffix(raw, "/")
	cleanRel := strings.TrimPrefix(raw, "./")
	cleanRel = strings.TrimLeft(cleanRel, "/")

	acctSlicePrefix := sliceRef.GetAccount() + "/" + sliceRef.GetSlice()
	if cleanRel == acctSlicePrefix {
		return "/" + acctSlicePrefix + "/**", nil
	}
	if strings.HasPrefix(cleanRel, acctSlicePrefix+"/") {
		return "/" + cleanRel, nil
	}
	colonPrefix := sliceRef.GetAccount() + ":" + sliceRef.GetSlice() + "/"
	if strings.HasPrefix(cleanRel, colonPrefix) {
		return "/" + acctSlicePrefix + "/" + strings.TrimPrefix(cleanRel, colonPrefix), nil
	}

	included := knownIncluded
	if len(included) == 0 && s.conn != nil {
		if resolved, err := s.Slice().ResolveSlice(s.Ctx(), &corev1.ResolveSliceRequest{Ref: sliceRef}); err == nil && resolved.GetDefinition() != nil {
			included = resolved.GetDefinition().GetIncludedPaths()
		}
	}

	if len(included) > 0 {
		if _, err := intents.ValidateSlicePattern("/"+cleanRel, included); err == nil {
			return "/" + cleanRel, nil
		}
		if len(included) > 1 {
			return "", userError(
				"ambiguous_intent_pattern",
				fmt.Sprintf("slice %s includes multiple path roots; pass a full path under one of %s", sliceRefLabel(sliceRef), strings.Join(included, ", ")),
				"Example: gs intent '"+strings.TrimRight(included[0], "/")+"/src/**'",
			)
		}
		root, err := paths.CanonicalPrefix(included[0])
		if err == nil {
			joined := strings.TrimRight(root, "/") + "/" + strings.TrimRight(cleanRel, "/")
			if trailingSlash && !strings.HasSuffix(joined, "/") {
				joined += "/"
			}
			return joined, nil
		}
	}

	joined := "/" + acctSlicePrefix + "/" + strings.TrimRight(cleanRel, "/")
	if trailingSlash && !strings.HasSuffix(joined, "/") {
		joined += "/"
	}
	return joined, nil
}

func (r Runner) localSegmentExistsInWorkspace(ws WorkspaceConfig, seg string) bool {
	root, err := r.workspaceRoot()
	if err != nil {
		return false
	}
	if st, err := os.Stat(filepath.Join(root, seg)); err == nil && st.IsDir() {
		return true
	}
	if len(ws.IncludedPaths) == 1 {
		incRel := strings.TrimPrefix(ws.IncludedPaths[0], "/")
		if st, err := os.Stat(filepath.Join(root, filepath.FromSlash(incRel), seg)); err == nil && st.IsDir() {
			return true
		}
	}
	return false
}

func splitNonEmptySlash(s string) []string {
	parts := strings.Split(s, "/")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}
