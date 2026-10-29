package storage

import (
	"errors"
	"reflect"
	"testing"
)

func TestNormalizeSecretPaths(t *testing.T) {
	got, err := NormalizeSecretPaths("acme", []string{"/acme/web/", " /acme ", "/acme//web", "", "acme/api"})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"/acme", "/acme/api", "/acme/web"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("NormalizeSecretPaths = %v, want %v", got, want)
	}
	for _, bad := range [][]string{nil, {" "}, {"/"}, {"/acmex"}, {"/other"}, {"/acme/../other"}} {
		if _, err := NormalizeSecretPaths("acme", bad); !errors.Is(err, ErrInvalid) {
			t.Fatalf("NormalizeSecretPaths(%q) = %v, want ErrInvalid", bad, err)
		}
	}
}

func TestSecretAppliesAt(t *testing.T) {
	secretPaths := []string{"/acme", "/acme/web"}
	for folder, want := range map[string]string{
		"/acme":          "/acme",
		"/acme/web":      "/acme/web",
		"/acme/web/ui":   "/acme/web",
		"/acme/website":  "/acme",
		"/other/acme/x":  "",
		"/acmeweb/thing": "",
	} {
		got, ok := SecretAppliesAt(secretPaths, folder)
		if got != want || ok != (want != "") {
			t.Fatalf("SecretAppliesAt(%s) = %q, %v; want %q", folder, got, ok, want)
		}
	}
	if !SecretTouches([]string{"/acme/web/ui"}, []string{"/acme/web"}) || !SecretTouches([]string{"/acme"}, []string{"/acme/web"}) || SecretTouches([]string{"/acme/api"}, []string{"/acme/web"}) {
		t.Fatal("SecretTouches: a secret inside or above a prefix touches it; a sibling does not")
	}
}

func TestPlanSetSliceSecret(t *testing.T) {
	slicePaths := []string{"/acme/a", "/acme/b"}
	if edit, err := PlanSetSliceSecret("T", slicePaths, nil); err != nil || !edit.Create {
		t.Fatalf("no secret yet = %+v, %v; want Create", edit, err)
	}
	edit, err := PlanSetSliceSecret("T", slicePaths, []Secret{{ID: "sec_1", Paths: []string{"/acme/a"}}})
	if err != nil || edit.Create || !reflect.DeepEqual(edit.Update, []string{"sec_1"}) || !reflect.DeepEqual(edit.AddPaths, []string{"/acme/b"}) {
		t.Fatalf("secret on one of the paths = %+v, %v; want update sec_1 and add /acme/b", edit, err)
	}
	if _, err := PlanSetSliceSecret("T", slicePaths, []Secret{{ID: "sec_2", Paths: []string{"/acme/a", "/acme/c"}}}); !errors.Is(err, ErrConflict) {
		t.Fatalf("secret that also applies elsewhere = %v, want ErrConflict", err)
	}
}
