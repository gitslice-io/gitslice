package gitcompat

import (
	"strconv"
	"strings"
)

// uploadRequest is what a Git client asked for in a git-upload-pack POST. The
// lazy projection reads it to learn which file contents the fetch would need.
type uploadRequest struct {
	// fetch is false for requests that transfer no objects (ls-refs).
	fetch bool
	wants []string
	haves []string
	// filter is the partial-clone filter spec ("blob:none"), or "".
	filter string
	// depth is the shallow depth asked for, or 0.
	depth int
}

// parseUploadRequest reads a protocol v2 or v0/v1 upload-pack request body. ok
// is false when the body is not one it understands; the caller then plans for
// the worst case.
func parseUploadRequest(body []byte) (req uploadRequest, ok bool) {
	lines, ok := pktLines(body)
	if !ok || len(lines) == 0 {
		return req, false
	}
	if strings.HasPrefix(lines[0], "command=") {
		// Protocol v2: the command, its capabilities, then the arguments.
		if strings.TrimSpace(strings.TrimPrefix(lines[0], "command=")) != "fetch" {
			return req, true
		}
	}
	req.fetch = true
	for _, line := range lines {
		field, rest, _ := strings.Cut(line, " ")
		switch field {
		case "want":
			if oid, _, _ := strings.Cut(rest, " "); validOID(oid) {
				req.wants = append(req.wants, oid)
			}
		case "have":
			if oid, _, _ := strings.Cut(rest, " "); validOID(oid) {
				req.haves = append(req.haves, oid)
			}
		case "filter":
			req.filter = strings.TrimSpace(rest)
		case "deepen":
			if n, err := strconv.Atoi(strings.TrimSpace(rest)); err == nil && n > 0 {
				req.depth = n
			}
		}
	}
	return req, true
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
