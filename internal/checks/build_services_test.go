package checks

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestParseBuildAndServices(t *testing.T) {
	file, err := Parse([]byte(`
version: 1
defaults:
  build:
    dockerfile: ci/Dockerfile
    inputs: [go.mod, go.sum, "web/package-lock.json"]
services:
  postgres:
    image: postgres:16-alpine
    env: {POSTGRES_HOST_AUTH_METHOD: trust}
    ready: pg_isready -U postgres -h 127.0.0.1
    ready_timeout: 90s
checks:
  e2e:
    run: go test ./tests/...
    services: [postgres]
  lint:
    image: golangci/golangci-lint:v1.59
    run: golangci-lint run
`))
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	e2e := file.Checks["e2e"]
	wantBuild := &Build{Context: ".", Dockerfile: "ci/Dockerfile", Inputs: []string{"go.mod", "go.sum", "web/package-lock.json"}}
	if !reflect.DeepEqual(e2e.Build, wantBuild) || e2e.Image != "" {
		t.Fatalf("e2e build = %#v image %q, want %#v from defaults", e2e.Build, e2e.Image, wantBuild)
	}
	if !reflect.DeepEqual(e2e.Services, []string{"postgres"}) {
		t.Fatalf("e2e services = %#v", e2e.Services)
	}
	// A check's own image replaces the default build.
	if lint := file.Checks["lint"]; lint.Build != nil || lint.Image == "" {
		t.Fatalf("lint = %#v, want its image and no build", lint)
	}
	if got := file.Services["postgres"]; got.ReadyTimeout != 90*time.Second || got.Env["POSTGRES_HOST_AUTH_METHOD"] != "trust" {
		t.Fatalf("service = %#v", got)
	}
}

func TestParseBuildAndServicesErrors(t *testing.T) {
	cases := map[string]string{
		"image and build": `
version: 1
checks:
  c: {run: x, image: a, build: {dockerfile: Dockerfile}}
`,
		"build without dockerfile": `
version: 1
checks:
  c: {run: x, build: {inputs: [go.mod]}}
`,
		"input outside context": `
version: 1
checks:
  c: {run: x, build: {dockerfile: Dockerfile, inputs: ["../go.mod"]}}
`,
		"absolute input": `
version: 1
checks:
  c: {run: x, build: {dockerfile: Dockerfile, inputs: ["/go.mod"]}}
`,
		"undefined service": `
version: 1
checks:
  c: {run: x, image: a, services: [db]}
`,
		"services on the host": `
version: 1
services:
  db: {image: postgres}
checks:
  c: {run: x, services: [db]}
`,
		"service name not a host name": `
version: 1
services:
  Data_Base: {image: postgres}
checks:
  c: {run: x, image: a}
`,
		"service without image": `
version: 1
services:
  db: {ready: "true"}
checks:
  c: {run: x, image: a}
`,
	}
	for name, yaml := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := Parse([]byte(yaml)); err == nil {
				t.Fatal("Parse() succeeded, want an error")
			}
		})
	}
}

func TestResolvePlanCarriesBuildAndServices(t *testing.T) {
	tree := fakeTreeReader{
		"/.gitslice/checks.yaml": []byte(`
version: 1
defaults:
  build:
    dockerfile: ci/Dockerfile
    inputs: [go.sum]
services:
  postgres:
    image: postgres:16-alpine
    ready: pg_isready
checks:
  e2e:
    run: go test ./...
    services: [postgres]
`),
		"web/.gitslice/checks.yaml": []byte(`
version: 1
checks:
  build:
    run: npm run build
    build:
      context: /
      dockerfile: ci/Dockerfile
      inputs: [web/package-lock.json]
`),
	}
	plan, err := ResolvePlan(context.Background(), tree, "tree", []string{"web/src/app.ts"}, []string{"web"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	specs := map[string]CheckSpec{}
	for _, spec := range plan.Runnable {
		specs[spec.Name] = spec
	}
	root := specs["e2e"]
	if root.Build == nil || root.Build.ContextDir != "/" || root.Build.Dockerfile != "ci/Dockerfile" || root.Build.Timeout != DefaultBuildTimeout {
		t.Fatalf("e2e build = %#v", root.Build)
	}
	if len(root.Services) != 1 || root.Services[0].Name != "postgres" || root.Services[0].Image != "postgres:16-alpine" {
		t.Fatalf("e2e services = %#v", root.Services)
	}

	// A web check whose image builds from the repo root materializes the
	// Dockerfile and the inputs besides web/, which takes it out of a web
	// slice.
	web := specs["web/build"]
	if web.Build == nil || web.Build.ContextDir != "/" {
		t.Fatalf("web build = %#v; plan %+v", web.Build, plan)
	}
	want := []string{"web", "ci/Dockerfile"}
	for _, p := range want {
		found := false
		for _, got := range web.MaterializePaths {
			if got == p || strings.HasPrefix(p, got+"/") {
				found = true
			}
		}
		if !found {
			t.Fatalf("materialize paths %v lack %s", web.MaterializePaths, p)
		}
	}
	if !web.OutOfSlice {
		t.Fatal("web/build stays in the web slice although its Dockerfile is outside it")
	}
}
