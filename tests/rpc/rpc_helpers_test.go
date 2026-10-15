package rpc_test

import (
	"testing"

	"google.golang.org/grpc/codes"
	grpcstatus "google.golang.org/grpc/status"
)

func assertGRPCCode(t *testing.T, err error, want codes.Code) {
	t.Helper()
	if grpcstatus.Code(err) != want {
		t.Fatalf("grpc code = %v, want %v; err=%v", grpcstatus.Code(err), want, err)
	}
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
