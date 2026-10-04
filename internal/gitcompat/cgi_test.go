package gitcompat

import (
	"bufio"
	"net/http"
	"strings"
	"testing"
)

func TestReadCGIHeader(t *testing.T) {
	r := bufio.NewReader(strings.NewReader("Status: 404 Not Found\r\nContent-Type: text/plain\r\nCache-Control: no-cache\r\n\r\nPACK..."))
	status, header, err := readCGIHeader(r)
	if err != nil {
		t.Fatal(err)
	}
	if status != http.StatusNotFound || header.Get("Content-Type") != "text/plain" || header.Get("Cache-Control") != "no-cache" || header.Get("Status") != "" {
		t.Fatalf("status %d, header %v", status, header)
	}
	rest := make([]byte, 7)
	if n, _ := r.Read(rest); string(rest[:n]) != "PACK..." {
		t.Fatalf("the body after the header was %q", rest[:n])
	}
}

func TestReadCGIHeaderDefaultsToOK(t *testing.T) {
	status, _, err := readCGIHeader(bufio.NewReader(strings.NewReader("Content-Type: application/x-git-upload-pack-result\n\nbody")))
	if err != nil || status != http.StatusOK {
		t.Fatalf("status %d, err %v", status, err)
	}
}

func TestReadCGIHeaderRejectsGarbage(t *testing.T) {
	for _, in := range []string{"", "\r\nbody", "no newline"} {
		if _, _, err := readCGIHeader(bufio.NewReader(strings.NewReader(in))); err == nil {
			t.Fatalf("expected an error for %q", in)
		}
	}
}

func TestLimitedBufferKeepsTheStart(t *testing.T) {
	var b limitedBuffer
	for i := 0; i < 100; i++ {
		if n, err := b.Write([]byte(strings.Repeat("x", 1000))); n != 1000 || err != nil {
			t.Fatalf("write = %d, %v", n, err)
		}
	}
	if len(b.String()) != 64<<10 {
		t.Fatalf("kept %d bytes", len(b.String()))
	}
}
