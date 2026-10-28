package cli

import (
	"reflect"
	"testing"
)

func TestEnvTemplateFillsPlaceholdersOnly(t *testing.T) {
	template := "# deploy env\n" +
		"PUBLIC_API_BASE_URL=https://api.example.com\n" +
		"CLOUDFLARE_API_TOKEN=gitslice-secret:CLOUDFLARE_API_TOKEN\n" +
		"export CF_TOKEN_COPY=\"gitslice-secret:CLOUDFLARE_API_TOKEN\"\n" +
		"KEY_PEM='gitslice-secret:KEY_PEM'\n" +
		"EMPTY=\n"
	lines, names, err := parseEnvTemplate(template)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"CLOUDFLARE_API_TOKEN", "KEY_PEM"}; !reflect.DeepEqual(names, want) {
		t.Fatalf("names = %v, want %v", names, want)
	}
	got := renderEnvTemplate(lines, map[string]string{
		"CLOUDFLARE_API_TOKEN": "tok_123-abc",
		"KEY_PEM":              "-----BEGIN-----\nit's\n-----END-----",
	})
	want := "# deploy env\n" +
		"PUBLIC_API_BASE_URL=https://api.example.com\n" +
		"CLOUDFLARE_API_TOKEN=tok_123-abc\n" +
		"export CF_TOKEN_COPY=tok_123-abc\n" +
		"KEY_PEM='-----BEGIN-----\nit'\\''s\n-----END-----'\n" +
		"EMPTY=\n"
	if got != want {
		t.Fatalf("rendered:\n%s\nwant:\n%s", got, want)
	}
}

func TestEnvTemplateRejectsBadSecretName(t *testing.T) {
	if _, _, err := parseEnvTemplate("X=gitslice-secret:lower-case\n"); err == nil {
		t.Fatal("parseEnvTemplate accepted a bad secret name")
	}
}
