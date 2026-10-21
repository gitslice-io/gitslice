package rpc_test

import (
	"bytes"
	"io"
	"net/http"
	"testing"

	"gitslice.io/gitslice/proto/core/v1"
)

func TestRPCSocialImageShowsAPublicSliceOnly(t *testing.T) {
	ts := startRPCServer(t)
	token := ts.loginViaGRPC(t, "alice")
	conn := dialTestGRPC(t, ts.addr)
	defer conn.Close()
	ctx := grpcAuthContext(token)
	slices := corev1.NewSliceServiceClient(conn)

	get := func(path string) []byte {
		t.Helper()
		res, err := http.Get("http://" + ts.addr + path)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		body, err := io.ReadAll(res.Body)
		if err != nil {
			t.Fatal(err)
		}
		if res.StatusCode != http.StatusOK || res.Header.Get("Content-Type") != "image/png" {
			t.Fatalf("GET %s: %d %q", path, res.StatusCode, res.Header.Get("Content-Type"))
		}
		return body
	}

	generic := get("/og/default.png")
	// Private: the generic card, which says nothing about the slice.
	if !bytes.Equal(get("/og/slices/acme/payment.png"), generic) {
		t.Fatal("a private slice got its own card")
	}

	slice, err := slices.ResolveSlice(ctx, &corev1.ResolveSliceRequest{Ref: testPaymentSliceRef()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := slices.UpdateSliceDefinition(ctx, &corev1.UpdateSliceDefinitionRequest{
		SliceId:                slice.Id,
		ExpectedDefinitionHash: slice.DefinitionHash,
		Definition:             &corev1.SliceDefinition{IncludedPaths: slice.Definition.IncludedPaths, Visibility: "public"},
	}); err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(get("/og/slices/acme/payment.png"), generic) {
		t.Fatal("a public slice got the generic card")
	}
}
