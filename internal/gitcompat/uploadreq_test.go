package gitcompat

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func pkt(lines ...string) string {
	var b strings.Builder
	for _, l := range lines {
		switch l {
		case "FLUSH":
			b.WriteString("0000")
		case "DELIM":
			b.WriteString("0001")
		default:
			fmt.Fprintf(&b, "%04x%s", len(l)+4, l)
		}
	}
	return b.String()
}

const (
	oidA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	oidB = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	oidC = "cccccccccccccccccccccccccccccccccccccccc"
)

func TestParseUploadRequestV2(t *testing.T) {
	body := pkt("command=fetch\n", "agent=git/2.34.1\n", "object-format=sha1\n", "DELIM",
		"thin-pack\n", "ofs-delta\n", "want "+oidA+"\n", "want "+oidB+"\n", "have "+oidC+"\n",
		"filter blob:none\n", "deepen 3\n", "done\n", "FLUSH")
	req, ok := parseUploadRequest([]byte(body))
	want := uploadRequest{fetch: true, wants: []string{oidA, oidB}, haves: []string{oidC}, filter: "blob:none", depth: 3}
	if !ok || !reflect.DeepEqual(req, want) {
		t.Fatalf("got %#v ok=%v, want %#v", req, ok, want)
	}
	if !req.excludesBlobs() {
		t.Fatal("blob:none excludes blobs")
	}
}

func TestParseUploadRequestV2LsRefsFetchesNothing(t *testing.T) {
	req, ok := parseUploadRequest([]byte(pkt("command=ls-refs\n", "agent=git/2.34.1\n", "DELIM", "peel\n", "FLUSH")))
	if !ok || req.fetch {
		t.Fatalf("ls-refs: %#v ok=%v", req, ok)
	}
}

func TestParseUploadRequestV0(t *testing.T) {
	body := pkt("want "+oidA+" multi_ack_detailed side-band-64k thin-pack ofs-delta deepen-since filter agent=git/2.34.1\n",
		"want "+oidB+"\n", "shallow "+oidC+"\n", "deepen 1\n", "FLUSH", "have "+oidC+"\n", "done\n")
	req, ok := parseUploadRequest([]byte(body))
	want := uploadRequest{fetch: true, wants: []string{oidA, oidB}, haves: []string{oidC}, depth: 1}
	if !ok || !reflect.DeepEqual(req, want) {
		t.Fatalf("got %#v ok=%v, want %#v", req, ok, want)
	}
	if req.excludesBlobs() {
		t.Fatal("no filter, so blobs are included")
	}
}

func TestParseUploadRequestFilters(t *testing.T) {
	for filter, excludes := range map[string]bool{"blob:none": true, "tree:0": true, "blob:limit=1m": false, "sparse:oid=abc": false, "": false} {
		req := uploadRequest{filter: filter}
		if req.excludesBlobs() != excludes {
			t.Errorf("filter %q: excludesBlobs = %v, want %v", filter, req.excludesBlobs(), excludes)
		}
	}
}

func TestParseUploadRequestRejectsGarbage(t *testing.T) {
	for _, in := range []string{"zzzz", "0100short", "00"} {
		if _, ok := parseUploadRequest([]byte(in)); ok {
			t.Errorf("%q should not parse", in)
		}
	}
}
