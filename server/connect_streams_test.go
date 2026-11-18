package server

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"gitslice.io/gitslice/server/internal/connectgen"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
)

func TestGeneratedConnectAdaptersAndStreamingProceduresUpToDate(t *testing.T) {
	wantSrc, err := connectgen.Generate()
	if err != nil {
		t.Fatalf("connectgen.Generate: %v", err)
	}
	gotSrc, err := os.ReadFile("connect_adapters_gen.go")
	if err != nil {
		t.Fatalf("ReadFile(connect_adapters_gen.go): %v", err)
	}
	if !bytes.Equal(gotSrc, wantSrc) {
		t.Fatal("server/connect_adapters_gen.go is out of date; run `go generate ./server`")
	}

	// Verify streamingProcedures matches 100% of streaming RPCs in gitslice.core.v1
	// and isStreamingHTTPRequest honors every streaming procedure while leaving
	// unary procedures under unary deadlines.
	seenStreaming := 0
	seenUnary := 0
	protoregistry.GlobalFiles.RangeFilesByPackage("gitslice.core.v1", func(fd protoreflect.FileDescriptor) bool {
		svcs := fd.Services()
		for i := 0; i < svcs.Len(); i++ {
			sd := svcs.Get(i)
			methods := sd.Methods()
			for j := 0; j < methods.Len(); j++ {
				md := methods.Get(j)
				proc := fmt.Sprintf("/%s/%s", sd.FullName(), md.Name())
				isStream := md.IsStreamingClient() || md.IsStreamingServer()
				if streamingProcedures[proc] != isStream {
					t.Errorf("streamingProcedures[%q] = %v, want %v", proc, streamingProcedures[proc], isStream)
				}
				req := httptest.NewRequest(http.MethodPost, proc, nil)
				req.Header.Set("Content-Type", "application/json")
				if got := isStreamingHTTPRequest(req); got != isStream {
					t.Errorf("isStreamingHTTPRequest(%q) = %v, want %v", proc, got, isStream)
				}
				if isStream {
					seenStreaming++
				} else {
					seenUnary++
				}
			}
		}
		return true
	})
	if seenStreaming == 0 || seenUnary == 0 {
		t.Fatalf("unexpected descriptor counts: streaming=%d unary=%d", seenStreaming, seenUnary)
	}
}
