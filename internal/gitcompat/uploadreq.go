package gitcompat

import (
	"strconv"
	"strings"
)

const (
	maxUploadWants = 16_384
	maxUploadHaves = 16_384
)

// uploadRequest is what a Git client asked for in a git-upload-pack POST. The
// lazy projection reads it to learn which file contents the fetch would need.
type uploadRequest struct {
	// fetch is false for requests that transfer no objects (ls-refs).
	fetch    bool
	wants    []string
	wantRefs []string
	haves    []string
	// filter is the partial-clone filter spec ("blob:none"), or "".
	filter string
	// depth is the shallow depth asked for, or 0.
	depth int
}

// parseUploadRequest reads a protocol v2 or v0/v1 upload-pack request body. ok
// is false when the body is malformed or exceeds negotiation bounds.
func parseUploadRequest(body []byte) (req uploadRequest, ok bool) {
	lines, ok := pktLines(body)
	if !ok || len(lines) == 0 {
		return req, false
	}
	if strings.HasPrefix(lines[0], "command=") {
		// Protocol v2: the command, its capabilities, then the arguments.
		switch strings.TrimSpace(strings.TrimPrefix(lines[0], "command=")) {
		case "fetch":
		case "ls-refs", "object-info":
			return req, true
		default:
			return req, false
		}
	} else if !strings.HasPrefix(lines[0], "want ") {
		return req, false
	}
	req.fetch = true
	var wantSeen, haveSeen, refSeen map[string]struct{}
	for _, line := range lines {
		field, rest, _ := strings.Cut(line, " ")
		switch field {
		case "want":
			oid, _, _ := strings.Cut(strings.TrimSpace(rest), " ")
			if !validOID(oid) {
				return uploadRequest{}, false
			}
			if wantSeen == nil {
				wantSeen = map[string]struct{}{}
			}
			if _, dup := wantSeen[oid]; !dup {
				if len(req.wants) >= maxUploadWants {
					return uploadRequest{}, false
				}
				wantSeen[oid] = struct{}{}
				req.wants = append(req.wants, oid)
			}
		case "want-ref":
			ref := strings.TrimSpace(rest)
			if !validUploadRef(ref) {
				return uploadRequest{}, false
			}
			if refSeen == nil {
				refSeen = map[string]struct{}{}
			}
			if _, dup := refSeen[ref]; !dup {
				if len(req.wantRefs) >= maxUploadWants {
					return uploadRequest{}, false
				}
				refSeen[ref] = struct{}{}
				req.wantRefs = append(req.wantRefs, ref)
			}
		case "have":
			oid, _, _ := strings.Cut(strings.TrimSpace(rest), " ")
			if !validOID(oid) {
				return uploadRequest{}, false
			}
			if haveSeen == nil {
				haveSeen = map[string]struct{}{}
			}
			if _, dup := haveSeen[oid]; !dup {
				if len(req.haves) >= maxUploadHaves {
					return uploadRequest{}, false
				}
				haveSeen[oid] = struct{}{}
				req.haves = append(req.haves, oid)
			}
		case "filter":
			req.filter = strings.TrimSpace(rest)
		case "deepen":
			n, err := strconv.Atoi(strings.TrimSpace(rest))
			if err != nil || n < 0 {
				return uploadRequest{}, false
			}
			if n > 0 {
				req.depth = n
			}
		}
	}
	if len(req.wants) == 0 && len(req.wantRefs) == 0 {
		return uploadRequest{}, false
	}
	return req, true
}

func validUploadRef(ref string) bool {
	if !strings.HasPrefix(ref, "refs/") || len(ref) > 256 || strings.Contains(ref, "..") || strings.Contains(ref, "//") || strings.HasSuffix(ref, "/") {
		return false
	}
	for i := 0; i < len(ref); i++ {
		b := ref[i]
		if b <= ' ' || b >= 0x7f || b == '\\' || b == '~' || b == '^' || b == ':' || b == '?' || b == '*' || b == '[' {
			return false
		}
	}
	return true
}

// excludesBlobs reports whether the filter leaves file contents out.
func (r uploadRequest) excludesBlobs() bool {
	return r.filter == "blob:none" || strings.HasPrefix(r.filter, "tree:")
}

func validOID(s string) bool {
	if len(s) != 40 {
		return false
	}
	for _, c := range s {
		if !strings.ContainsRune("0123456789abcdef", c) {
			return false
		}
	}
	return true
}

// pktLines splits pkt-line framed data into its text lines, dropping flush
// (0000), delimiter (0001) and response-end (0002) packets.
func pktLines(data []byte) ([]string, bool) {
	var lines []string
	for len(data) > 0 {
		if len(data) < 4 {
			return nil, false
		}
		n, err := strconv.ParseUint(string(data[:4]), 16, 32)
		if err != nil {
			return nil, false
		}
		if n < 4 {
			data = data[4:] // flush, delimiter or response-end
			continue
		}
		if int(n) > len(data) {
			return nil, false
		}
		lines = append(lines, strings.TrimRight(string(data[4:n]), "\n"))
		data = data[n:]
	}
	return lines, true
}
