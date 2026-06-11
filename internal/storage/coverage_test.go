package storage

import (
	"reflect"
	"testing"
)

func TestCoverageAncestorPrefixes(t *testing.T) {
	got := CoverageAncestorPrefixes("/acme/payment/shared/file.go")
	want := []string{"/acme", "/acme/payment", "/acme/payment/shared", "/acme/payment/shared/file.go"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("CoverageAncestorPrefixes() = %#v, want %#v", got, want)
	}
}

func TestAssembleCoverageByPathSortsAndDeduplicates(t *testing.T) {
	coverage := AssembleCoverageByPath([]string{
		"/acme/payment/shared/file.go",
		"/acme/backend/api.go",
	}, map[string][]string{
		"/acme":                {"slice_home"},
		"/acme/payment":        {"slice_payment"},
		"/acme/payment/shared": {"slice_backend", "slice_payment"},
		"/acme/backend":        {"slice_backend"},
	})

	if got, want := coverage["/acme/payment/shared/file.go"], []string{"slice_backend", "slice_home", "slice_payment"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("shared coverage = %#v, want %#v", got, want)
	}
	if got, want := coverage["/acme/backend/api.go"], []string{"slice_backend", "slice_home"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("backend coverage = %#v, want %#v", got, want)
	}
}
