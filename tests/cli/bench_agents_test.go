package cli_test

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"gitslice.io/gitslice/proto/core/v1"
)

// TestBenchmarkParallelAgents times many agents landing changes on one
// codebase at the same moment (design/28_benchmarks.md, "Landing in
// parallel"), gs next to git pushing to one branch:
//
//   - own files: every agent edits a file of its own;
//   - one file: every agent edits a different block of the same file.
//
// Every agent works in its own workspace or clone, set up outside the clock,
// all based on the same head. The clock runs from the moment they all start
// until the last change has landed. A git agent pushes, and when the push is
// rejected (someone landed first) pulls with rebase and pushes again; a gs
// agent runs gs create --submit, and when the submit is refused for a stale
// base runs gs sync and gs submit again. Retries are those extra rounds. After
// each run the landed content is checked: every agent's change is there.
//
// By default it runs 4 agents once, to keep every scenario working in the e2e
// suite. GITSLICE_BENCH=1 runs GITSLICE_BENCH_AGENTS (default 8) agents
// GITSLICE_BENCH_RUNS (default 5) times and logs a table:
//
//	GITSLICE_BENCH=1 GITSLICE_BENCH_AGENTS=8 GITSLICE_BENCH_RUNS=5 \
//	  go test ./tests/cli -run TestBenchmarkParallelAgents -v -timeout 60m
func TestBenchmarkParallelAgents(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	agents, runs := 4, 1
	if os.Getenv("GITSLICE_BENCH") != "" {
		agents, runs = benchEnvInt(t, "GITSLICE_BENCH_AGENTS", 8), benchEnvInt(t, "GITSLICE_BENCH_RUNS", 5)
	}

	type result struct {
		wall    time.Duration
		retries int
	}
	results := map[string][]result{}
	for run := 0; run < runs; run++ {
		t.Run(fmt.Sprintf("run%d", run), func(t *testing.T) {
			for _, scenario := range []struct {
				name  string
				run   func(t *testing.T, agents int) (time.Duration, int)
				label string
			}{
				{"own/git", benchGitOwnFiles, ""},
				{"own/gs", func(t *testing.T, n int) (time.Duration, int) { return benchGSAgents(t, n, false, false) }, ""},
				{"one/git", benchGitOneFile, ""},
				{"one/gs", func(t *testing.T, n int) (time.Duration, int) { return benchGSAgents(t, n, true, false) }, ""},
				{"one/gs-merge", func(t *testing.T, n int) (time.Duration, int) { return benchGSAgents(t, n, true, true) }, ""},
			} {
				wall, retries := scenario.run(t, agents)
				results[scenario.name] = append(results[scenario.name], result{wall, retries})
			}
		})
		if t.Failed() {
			return
		}
	}

	median := func(name string) (string, string) {
		var walls []time.Duration
		var retries []int
		for _, r := range results[name] {
			walls = append(walls, r.wall)
			retries = append(retries, r.retries)
		}
		if len(walls) == 0 {
			// The scenario was skipped (the gs ones need the test database).
			return "-", "-"
		}
		slices.Sort(retries)
		return benchMedian(walls), fmt.Sprint(retries[len(retries)/2])
	}
	var table strings.Builder
	fmt.Fprintf(&table, "%d agents landing at once, median of %d run(s); milliseconds to the last landing, and retries (extra rounds of pull/sync and push/submit, summed over agents):\n\n", agents, runs)
	table.WriteString("| Scenario | git: time | git: retries | gs: time | gs: retries |\n| --- | ---: | ---: | ---: | ---: |\n")
	gitOwn, gitOwnR := median("own/git")
	gsOwn, gsOwnR := median("own/gs")
	fmt.Fprintf(&table, "| Each agent edits its own file | %s | %s | %s | %s |\n", gitOwn, gitOwnR, gsOwn, gsOwnR)
	gitOne, gitOneR := median("one/git")
	gsOne, gsOneR := median("one/gs")
	fmt.Fprintf(&table, "| One file, a block each | %s | %s | %s | %s |\n", gitOne, gitOneR, gsOne, gsOneR)
	gsMerge, gsMergeR := median("one/gs-merge")
	fmt.Fprintf(&table, "| One file, a block each, merge on submit | | | %s | %s |\n", gsMerge, gsMergeR)
	t.Log("\n" + table.String())
}

const benchAgentFileLines = 200

func benchAgentSeed(agents int, oneFile bool) map[string]string {
	files := map[string]string{}
	if oneFile {
		var b strings.Builder
		for i := 0; i < agents; i++ {
			fmt.Fprintf(&b, "block %02d\n\n\n", i)
		}
		files["shared/notes.txt"] = b.String()
		return files
	}
	for i := 0; i < agents; i++ {
		var b strings.Builder
		for l := 0; l < benchAgentFileLines; l++ {
			fmt.Fprintf(&b, "file %02d line %03d\n", i, l)
		}
		files[fmt.Sprintf("agents/file%02d.txt", i)] = b.String()
	}
	return files
}

// benchAgentEdit is agent i's change to the seeded content of its file.
func benchAgentEdit(i int, oneFile bool, content string) string {
	if oneFile {
		lines := strings.Split(content, "\n")
		lines[i*3] = fmt.Sprintf("block %02d edited by agent %02d", i, i)
		return strings.Join(lines, "\n")
	}
	return content + fmt.Sprintf("added by agent %02d\n", i)
}

func benchAgentPath(i int, oneFile bool) string {
	if oneFile {
		return "shared/notes.txt"
	}
	return fmt.Sprintf("agents/file%02d.txt", i)
}

// benchRace starts every agent at once and returns the wall time until the
// last one is done, and the retries they report.
func benchRace(t *testing.T, agents int, agent func(i int) (retries int)) (time.Duration, int) {
	t.Helper()
	start := make(chan struct{})
	var wg sync.WaitGroup
	var retries atomic.Int64
	for i := 0; i < agents; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			retries.Add(int64(agent(i)))
		}(i)
	}
	began := time.Now()
	close(start)
	wg.Wait()
	return time.Since(began), int(retries.Load())
}

func benchGitOwnFiles(t *testing.T, agents int) (time.Duration, int) {
	return benchGitAgents(t, agents, false)
}

func benchGitOneFile(t *testing.T, agents int) (time.Duration, int) {
	return benchGitAgents(t, agents, true)
}

func benchGitAgents(t *testing.T, agents int, oneFile bool) (time.Duration, int) {
	t.Helper()
	env := benchGitEnv(t)
	remote := filepath.Join(t.TempDir(), "remote.git")
	benchGit(t, env, "", "init", "-q", "--bare", "-b", "main", remote)
	seed := filepath.Join(t.TempDir(), "seed")
	benchGit(t, env, "", "init", "-q", "-b", "main", seed)
	for rel, content := range benchAgentSeed(agents, oneFile) {
		benchWriteFile(t, filepath.Join(seed, rel), content)
	}
	benchGit(t, env, seed, "add", "-A")
	benchGit(t, env, seed, "commit", "-q", "-m", "seed")
	benchGit(t, env, seed, "push", "-q", remote, "main")
	clones := make([]string, agents)
	for i := range clones {
		clones[i] = filepath.Join(t.TempDir(), fmt.Sprintf("agent%02d", i))
		benchGit(t, env, "", "clone", "-q", "--no-local", remote, clones[i])
		path := filepath.Join(clones[i], benchAgentPath(i, oneFile))
		content, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		benchWriteFile(t, path, benchAgentEdit(i, oneFile, string(content)))
		benchGit(t, env, clones[i], "commit", "-q", "-a", "-m", fmt.Sprintf("agent %02d", i))
	}
	benchSettleIdle(t)

	wall, retries := benchRace(t, agents, func(i int) int {
		retries := 0
		for {
			if err := benchGitTry(env, clones[i], "push", "-q", "origin", "HEAD:main"); err == nil {
				return retries
			}
			retries++
			if retries > 200 {
				t.Errorf("agent %d could not push after %d tries", i, retries)
				return retries
			}
			if err := benchGitTry(env, clones[i], "pull", "-q", "--rebase", "origin", "main"); err != nil {
				t.Errorf("agent %d: pull --rebase: %v", i, err)
				return retries
			}
		}
	})

	verify := filepath.Join(t.TempDir(), "verify")
	benchGit(t, env, "", "clone", "-q", "--no-local", remote, verify)
	for i := 0; i < agents; i++ {
		content, err := os.ReadFile(filepath.Join(verify, benchAgentPath(i, oneFile)))
		if err != nil || !strings.Contains(string(content), fmt.Sprintf("agent %02d", i)) {
			t.Fatalf("git: agent %d's change did not land (%v)", i, err)
		}
	}
	return wall, retries
}

func benchGitTry(env []string, dir string, args ...string) error {
	cmd := exec.Command("git", args...)
	cmd.Dir, cmd.Env = dir, env
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("%v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func benchGSAgents(t *testing.T, agents int, oneFile, mergeOnSubmit bool) (time.Duration, int) {
	t.Helper()
	ts := startTestServer(t)
	ts.agentSignup = true
	ts.restart(t)
	const account = "bench"
	conn := dialTestGRPC(t, ts.addr)
	agent, err := corev1.NewAuthServiceClient(conn).RegisterAgent(context.Background(), &corev1.RegisterAgentRequest{Username: account, OwnerEmail: "owner@example.com"})
	conn.Close()
	if err != nil {
		t.Fatal(err)
	}
	newHome := func() string {
		home := t.TempDir()
		writeCLIAuthConfig(t, home, ts.addr, agent.ApiKey, agent.SubjectId)
		return home
	}
	seedHome, seedWS := newHome(), t.TempDir()
	runCLI(t, seedHome, seedWS, "workspace", "init", account+"/home")
	for rel, content := range benchAgentSeed(agents, oneFile) {
		benchWriteFile(t, filepath.Join(seedWS, account, rel), content)
	}
	runCLI(t, seedHome, seedWS, "create", "--all", "--message", "seed", "--submit")
	if mergeOnSubmit {
		runCLI(t, seedHome, seedWS, "slice", "update", account+"/home", "--merge-on-submit", "on")
	}
	homes, workspaces := make([]string, agents), make([]string, agents)
	for i := range homes {
		homes[i], workspaces[i] = newHome(), t.TempDir()
		runCLI(t, homes[i], workspaces[i], "workspace", "init", account+"/home")
		path := filepath.Join(workspaces[i], account, benchAgentPath(i, oneFile))
		content, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		benchWriteFile(t, path, benchAgentEdit(i, oneFile, string(content)))
	}
	benchSettleIdle(t)

	wall, retries := benchRace(t, agents, func(i int) int {
		message := fmt.Sprintf("agent %02d", i)
		out, err := runCLIResult(homes[i], workspaces[i], "create", "--all", "--message", message, "--submit", "--watch-timeout", "2m")
		retries := 0
		for err != nil {
			if !strings.Contains(err.Error(), "path base conflict") {
				t.Errorf("agent %d: %v\n%s", i, err, out)
				return retries
			}
			retries++
			if retries > 200 {
				t.Errorf("agent %d could not submit after %d tries", i, retries)
				return retries
			}
			if _, syncErr := runCLIResult(homes[i], workspaces[i], "sync"); syncErr != nil {
				t.Errorf("agent %d: sync: %v", i, syncErr)
				return retries
			}
			out, err = runCLIResult(homes[i], workspaces[i], "submit", "--watch-timeout", "2m")
		}
		return retries
	})

	verifyHome, verifyWS := newHome(), t.TempDir()
	runCLI(t, verifyHome, verifyWS, "workspace", "init", account+"/home")
	for i := 0; i < agents; i++ {
		content, err := os.ReadFile(filepath.Join(verifyWS, account, benchAgentPath(i, oneFile)))
		if err != nil || !strings.Contains(string(content), fmt.Sprintf("agent %02d", i)) {
			t.Fatalf("gs: agent %d's change did not land (%v)", i, err)
		}
	}
	return wall, retries
}

func benchWriteFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// benchSettleIdle waits for the machine to go quiet before the race.
func benchSettleIdle(t *testing.T) {
	t.Helper()
	time.Sleep(1100 * time.Millisecond)
	benchWaitForIdleCPU(t)
}
