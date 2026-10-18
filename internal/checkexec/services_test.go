package checkexec

import (
	"context"
	"os/exec"
	"strings"
	"testing"
	"time"

	"gitslice.io/gitslice/internal/checks"
)

func TestRunContainerServiceIsReadyAndReachableByName(t *testing.T) {
	image := requireLocalImage(t, "postgres:16")
	rt := DetectRuntime(context.Background())
	before := gitsliceRunObjects(t, rt)

	result, err := Run(context.Background(), t.TempDir(), checks.CheckSpec{
		Image: image,
		Services: []checks.ServiceSpec{{
			Name:  "db",
			Image: image,
			Env:   map[string]string{"POSTGRES_HOST_AUTH_METHOD": "trust"},
			Ready: "pg_isready -U postgres -h 127.0.0.1",
		}},
		Run:     `psql -h db -U postgres -tAc "select 40 + 2"`,
		Timeout: 2 * time.Minute,
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.Status != "passed" || !strings.HasSuffix(strings.TrimSpace(result.Log), "42") {
		t.Fatalf("result = %#v, want the check to query the service", result)
	}
	if !strings.Contains(result.Log, "[gitslice] started services: db (postgres:16, ready in") {
		t.Fatalf("log = %q, want a note about the service", result.Log)
	}
	if after := gitsliceRunObjects(t, rt); after != before {
		t.Fatalf("service containers or networks left behind: before %q, after %q", before, after)
	}
}

// The run's network is internal: a check without network access reaches its
// services and nothing else.
func TestRunContainerServiceNetworkIsInternal(t *testing.T) {
	image := requireLocalImage(t, "postgres:16")
	rt := DetectRuntime(context.Background())
	out, err := exec.Command(rt.Path, "run", "--rm", "--network=bridge", "busybox", "sh", "-c", "wget -q -T 3 -O /dev/null http://1.1.1.1 && echo reachable").CombinedOutput()
	if err != nil || !strings.Contains(string(out), "reachable") {
		t.Skipf("no outside network to compare against: %s %v", out, err)
	}
	result, err := Run(context.Background(), t.TempDir(), checks.CheckSpec{
		Image: "busybox",
		Services: []checks.ServiceSpec{{
			Name:  "db",
			Image: image,
			Env:   map[string]string{"POSTGRES_HOST_AUTH_METHOD": "trust"},
			Ready: "pg_isready -U postgres -h 127.0.0.1",
		}},
		Run:     `! wget -q -T 3 -O /dev/null http://1.1.1.1`,
		Timeout: 2 * time.Minute,
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.Status != "passed" {
		t.Fatalf("result = %#v, want the outside unreachable", result)
	}
}

func TestRunContainerServiceThatNeverGetsReadyErrors(t *testing.T) {
	requireLocalImage(t, "busybox")
	rt := DetectRuntime(context.Background())
	before := gitsliceRunObjects(t, rt)

	result, err := Run(context.Background(), t.TempDir(), checks.CheckSpec{
		Image: "busybox",
		Services: []checks.ServiceSpec{{
			Name:         "broken",
			Image:        "busybox", // its shell exits at once
			Ready:        "false",
			ReadyTimeout: 20 * time.Second,
		}},
		Run: "true",
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.Status != "errored" || !strings.HasPrefix(result.Summary, "service broken ") {
		t.Fatalf("result = %#v, want the service failure", result)
	}
	if after := gitsliceRunObjects(t, rt); after != before {
		t.Fatalf("service containers or networks left behind: before %q, after %q", before, after)
	}
}

func TestRunHostCheckWithServicesErrors(t *testing.T) {
	result, err := Run(context.Background(), t.TempDir(), checks.CheckSpec{
		Services: []checks.ServiceSpec{{Name: "db", Image: "postgres:16"}},
		Run:      "true",
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.Status != "errored" || !strings.Contains(result.Summary, "services need a containerized check") {
		t.Fatalf("result = %#v, want services refused on the host", result)
	}
}

// gitsliceRunObjects lists the service containers and networks of check
// runs, to see that a run removes its own.
func gitsliceRunObjects(t *testing.T, rt RuntimeInfo) string {
	t.Helper()
	containers, err := exec.Command(rt.Path, "ps", "-a", "--filter", "name="+serviceNamePrefix, "--format", "{{.Names}}").Output()
	if err != nil {
		t.Fatal(err)
	}
	networks, err := exec.Command(rt.Path, "network", "ls", "--filter", "name="+networkNamePrefix, "--format", "{{.Name}}").Output()
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(containers)) + "|" + strings.TrimSpace(string(networks))
}
