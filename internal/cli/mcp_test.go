package cli

import (
	"errors"
	"testing"

	"gitslice.io/gitslice/internal/mcpserver"
)

func TestMCPToolErrorKeepsGRPCCodesAndUserErrors(t *testing.T) {
	if got := mcpToolError(errors.New("boom")); got["code"] != "error" {
		t.Fatalf("plain error = %v", got)
	}
	if got := mcpToolError(&mcpserver.UserError{Code: "slice_required", Message: "no slice given", Hint: "Pass slice."}); got["code"] != "slice_required" || got["hint"] != "Pass slice." {
		t.Fatalf("user error = %v", got)
	}
}
