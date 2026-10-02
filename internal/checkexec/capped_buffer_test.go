package checkexec

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
)

func TestCappedBufferKeepsHeadAndTail(t *testing.T) {
	var full bytes.Buffer
	var b cappedBuffer
	for i := 0; full.Len() < 3*MaxLogBytes; i++ {
		line := fmt.Sprintf("line %06d\n", i)
		full.WriteString(line)
		if _, err := b.Write([]byte(line)); err != nil {
			t.Fatal(err)
		}
	}
	got := b.String()
	all := full.String()
	tailSize := MaxLogBytes - logHeadBytes
	if !strings.HasPrefix(got, all[:logHeadBytes]) {
		t.Fatal("log should start with the first logHeadBytes of output")
	}
	if !strings.HasSuffix(got, all[len(all)-tailSize:]) {
		t.Fatal("log should end with the latest output")
	}
	marker := fmt.Sprintf("\n[check log truncated: %d bytes omitted]\n", len(all)-MaxLogBytes)
	if !strings.Contains(got, marker) {
		t.Fatalf("log should report the omitted bytes, want %q", marker)
	}
	if len(got) != MaxLogBytes+len(marker) {
		t.Fatalf("log length = %d, want %d", len(got), MaxLogBytes+len(marker))
	}
}

func TestCappedBufferShortAndSingleLargeWrites(t *testing.T) {
	var short cappedBuffer
	_, _ = short.Write([]byte("ok\n"))
	if got := short.String(); got != "ok\n" {
		t.Fatalf("short log = %q", got)
	}

	var exact cappedBuffer
	data := bytes.Repeat([]byte("x"), MaxLogBytes)
	_, _ = exact.Write(data)
	if got := exact.String(); got != string(data) {
		t.Fatal("a log of exactly MaxLogBytes should be kept whole")
	}

	var large cappedBuffer
	data = append(bytes.Repeat([]byte("a"), 2*MaxLogBytes), []byte("FAIL: the end\n")...)
	n, err := large.Write(data)
	if err != nil || n != len(data) {
		t.Fatalf("Write = %d, %v", n, err)
	}
	if got := large.String(); !strings.HasSuffix(got, "FAIL: the end\n") || !strings.Contains(got, "bytes omitted]") {
		t.Fatalf("a single large write should keep its end, got suffix %q", got[len(got)-40:])
	}
}
