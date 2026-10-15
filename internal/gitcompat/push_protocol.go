package gitcompat

import (
	"bytes"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"google.golang.org/grpc/status"
)

func writeReceivePackAdvertisement(w http.ResponseWriter, projection *Projection) {
	w.Header().Set("Content-Type", "application/x-git-receive-pack-advertisement")
	var out bytes.Buffer
	appendPktLine(&out, []byte("# service=git-receive-pack\n"))
	appendFlush(&out)
	head, ref := projection.GitCommitID, "refs/heads/main"
	if head == "" {
		// An empty repository advertises its capabilities on a placeholder ref.
		head, ref = zeroGitOID, "capabilities^{}"
	}
	line := fmt.Sprintf("%s %s\x00report-status side-band-64k agent=gitslice\n", head, ref)
	appendPktLine(&out, []byte(line))
	appendFlush(&out)
	_, _ = w.Write(out.Bytes())
}

func parseReceivePackRequest(data []byte) (*receivePackRequest, error) {
	var commands []receivePackCommand
	caps := map[string]struct{}{}
	offset := 0
	first := true
	for {
		payload, next, flush, err := readPktLine(data, offset)
		if err != nil {
			return nil, err
		}
		offset = next
		if flush {
			break
		}
		line := strings.TrimSuffix(string(payload), "\n")
		if first {
			first = false
			if command, capabilities, ok := strings.Cut(line, "\x00"); ok {
				line = command
				for _, cap := range strings.Fields(capabilities) {
					caps[cap] = struct{}{}
				}
			}
		}
		fields := strings.Fields(line)
		if len(fields) != 3 {
			return nil, fmt.Errorf("malformed receive-pack command %q", line)
		}
		commands = append(commands, receivePackCommand{
			OldOID: fields[0],
			NewOID: fields[1],
			Ref:    fields[2],
		})
	}
	return &receivePackRequest{commands: commands, capabilities: caps, packfile: data[offset:]}, nil
}

func readPktLine(data []byte, offset int) ([]byte, int, bool, error) {
	if offset+4 > len(data) {
		return nil, offset, false, errors.New("truncated pkt-line header")
	}
	n, err := strconv.ParseInt(string(data[offset:offset+4]), 16, 32)
	if err != nil {
		return nil, offset, false, fmt.Errorf("invalid pkt-line length %q", string(data[offset:offset+4]))
	}
	offset += 4
	if n == 0 {
		return nil, offset, true, nil
	}
	if n < 4 {
		return nil, offset, false, fmt.Errorf("invalid pkt-line length %d", n)
	}
	end := offset + int(n) - 4
	if end > len(data) {
		return nil, offset, false, errors.New("truncated pkt-line payload")
	}
	return data[offset:end], end, false, nil
}

func writeReceivePackResult(w http.ResponseWriter, caps map[string]struct{}, result receivePackResult) {
	if result.UnpackStatus == "" {
		result.UnpackStatus = "ok"
	}
	w.Header().Set("Content-Type", "application/x-git-receive-pack-result")
	var out bytes.Buffer
	if hasCapability(caps, "side-band-64k") || hasCapability(caps, "side-band") {
		for _, msg := range result.Messages {
			appendSidebandPktLine(&out, 2, []byte(msg))
		}
		// Channel 1 carries the report-status pkt-line stream itself, so the
		// inner payload must be pkt-line encoded before sideband framing.
		var report bytes.Buffer
		appendPktLine(&report, []byte("unpack "+result.UnpackStatus+"\n"))
		for _, ref := range result.RefStatuses {
			appendPktLine(&report, []byte(formatRefStatus(ref)))
		}
		appendFlush(&report)
		appendSidebandPktLine(&out, 1, report.Bytes())
		appendFlush(&out)
		_, _ = w.Write(out.Bytes())
		return
	}
	appendPktLine(&out, []byte("unpack "+result.UnpackStatus+"\n"))
	for _, ref := range result.RefStatuses {
		appendPktLine(&out, []byte(formatRefStatus(ref)))
	}
	appendFlush(&out)
	_, _ = w.Write(out.Bytes())
}

func formatRefStatus(ref receivePackRefStatus) string {
	if ref.OK {
		return "ok " + ref.Ref + "\n"
	}
	msg := strings.TrimSpace(ref.Message)
	if msg == "" {
		msg = "rejected"
	}
	return "ng " + ref.Ref + " " + msg + "\n"
}

func appendPktLine(out *bytes.Buffer, payload []byte) {
	fmt.Fprintf(out, "%04x", len(payload)+4)
	out.Write(payload)
}

func appendSidebandPktLine(out *bytes.Buffer, channel byte, payload []byte) {
	packet := make([]byte, 0, len(payload)+1)
	packet = append(packet, channel)
	packet = append(packet, payload...)
	appendPktLine(out, packet)
}

func appendFlush(out *bytes.Buffer) {
	out.WriteString("0000")
}

func hasCapability(caps map[string]struct{}, cap string) bool {
	_, ok := caps[cap]
	return ok
}

func acceptedReceivePack(ref, message string) receivePackResult {
	return receivePackResult{
		UnpackStatus: "ok",
		RefStatuses:  []receivePackRefStatus{{Ref: ref, OK: true}},
		Messages:     []string{message},
	}
}

func rejectedReceivePack(ref, message string) receivePackResult {
	return receivePackResult{
		UnpackStatus: "ok",
		RefStatuses:  []receivePackRefStatus{{Ref: ref, Message: message}},
		Messages:     []string{message + "\n"},
	}
}

func userFacingError(err error) string {
	if err == nil {
		return ""
	}
	if st, ok := status.FromError(err); ok {
		return st.Message()
	}
	return strings.TrimSpace(err.Error())
}

func isZeroGitOID(oid string) bool {
	return oid == "" || oid == zeroGitOID
}

func shortGitOID(oid string) string {
	if len(oid) <= 12 {
		return oid
	}
	return oid[:12]
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
