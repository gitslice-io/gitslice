package cli_test

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io/fs"
	"maps"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"gitslice.io/gitslice/proto/core/v1"
)

// TestBenchmarkAgainstGit times gs and git on the four synthetic workloads of
// the OpenScion benchmarks (design/28_benchmarks.md): import a directory,
// commit one changed file, full checkout, and one-directory checkout. By
// default it runs a tiny fixture once, to check that every scenario works and
// that each checkout matches the fixture byte for byte. GITSLICE_BENCH=1 runs
// the real sizes and logs a table of medians (run with -v):
//
//	GITSLICE_BENCH=1 GITSLICE_BENCH_FILES_PER_DIR=100 GITSLICE_BENCH_RUNS=7 \
//	  go test ./tests/cli -run TestBenchmarkAgainstGit -v -timeout 60m
//
// Each run gets its own server (a fresh database schema and object store) and
// fresh client caches, so nothing is deduplicated against an earlier run. The
// server runs in this process over loopback gRPC, on Postgres and a filesystem
// object store; gs commands run in this process too, signed in with an API
// key, while git pays a process start per command.
func TestBenchmarkAgainstGit(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	filesPerDir, runs := 2, 1
	if os.Getenv("GITSLICE_BENCH") != "" {
		filesPerDir, runs = benchEnvInt(t, "GITSLICE_BENCH_FILES_PER_DIR", 100), benchEnvInt(t, "GITSLICE_BENCH_RUNS", 5)
	}
	const dirs, fileBytes = 10, 4096
	fixture := benchFixture(dirs, filesPerDir, fileBytes, 1)

	samples := map[string][]time.Duration{}
	record := func(name string, start time.Time) { samples[name] = append(samples[name], time.Since(start)) }
	for run := 0; run < runs; run++ {
		t.Run(fmt.Sprintf("run%d", run), func(t *testing.T) {
			// gs signs in as a registered agent with an API key, the
			// credential agents use, checked against the database per call.
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
			gitEnv := benchGitEnv(t)
			db, err := sql.Open("pgx", databaseURLWithSearchPath(t, ts.databaseURL, ts.schema))
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			settle := func() { benchSettle(t, db) }

			// Import: the fixture is on disk; the clock covers adding it and
			// landing it (gs: create, upload, submit until published; git:
			// init, add, commit).
			importHome, importWS := newHome(), t.TempDir()
			runCLI(t, importHome, importWS, "workspace", "init", account+"/home")
			benchWriteFixture(t, filepath.Join(importWS, account), fixture)
			start := time.Now()
			out := runCLI(t, importHome, importWS, "create", "--all", "--message", "import", "--submit", "--json")
			record("gs/import", start)
			var created struct {
				Submit *struct {
					Status string `json:"status"`
				} `json:"submit"`
			}
			if err := json.Unmarshal([]byte(out), &created); err != nil || created.Submit == nil || created.Submit.Status != "submitted" {
				t.Fatalf("import did not land: %v\n%s", err, out)
			}
			gitSource := t.TempDir()
			benchWriteFixture(t, gitSource, fixture)
			settle()
			start = time.Now()
			benchGit(t, gitEnv, gitSource, "init", "-q")
			benchGit(t, gitEnv, gitSource, "add", "-A")
			benchGit(t, gitEnv, gitSource, "commit", "-q", "-m", "import")
			record("git/import", start)
			benchGit(t, gitEnv, gitSource, "config", "uploadpack.allowFilter", "true")

			// Full checkout, from a cold client cache.
			fullHome, fullWS := newHome(), t.TempDir()
			settle()
			start = time.Now()
			runCLI(t, fullHome, fullWS, "workspace", "init", account+"/home")
			record("gs/full", start)
			benchCheckTree(t, filepath.Join(fullWS, account), fixture, "")
			gitFull := filepath.Join(t.TempDir(), "clone")
			settle()
			start = time.Now()
			benchGit(t, gitEnv, "", "clone", "-q", "--no-local", gitSource, gitFull)
			record("git/full", start)
			benchCheckTree(t, gitFull, fixture, "")

			// One-directory checkout: a slice of dir0000 / a blobless partial
			// clone narrowed to dir0000.
			runCLI(t, importHome, t.TempDir(), "slice", "create", account+"/dir0000")
			sparseHome, sparseWS := newHome(), t.TempDir()
			settle()
			start = time.Now()
			runCLI(t, sparseHome, sparseWS, "workspace", "init", account+"/dir0000")
			record("gs/sparse", start)
			benchCheckTree(t, filepath.Join(sparseWS, account), fixture, "dir0000/")
			gitSparse := filepath.Join(t.TempDir(), "sparse")
			settle()
			start = time.Now()
			benchGit(t, gitEnv, "", "clone", "-q", "--no-local", "--filter=blob:none", "--sparse", gitSource, gitSparse)
			benchGit(t, gitEnv, gitSparse, "sparse-checkout", "set", "dir0000")
			record("git/sparse", start)
			benchCheckTree(t, gitSparse, fixture, "dir0000/")

			// Commit one changed file in the full checkouts. They settle first,
			// so neither tool re-hashes files written in the same second as its
			// index.
			time.Sleep(1100 * time.Millisecond)
			changed := benchPath(dirs/2, 0)
			benchFlipByte(t, filepath.Join(fullWS, account, changed))
			settle()
			start = time.Now()
			runCLI(t, fullHome, fullWS, "create", "--all", "--message", "change one file")
			record("gs/commit", start)
			benchFlipByte(t, filepath.Join(gitFull, changed))
			settle()
			start = time.Now()
			benchGit(t, gitEnv, gitFull, "commit", "-q", "-a", "-m", "change one file")
			record("git/commit", start)
		})
		if t.Failed() {
			return
		}
	}

	var table strings.Builder
	fmt.Fprintf(&table, "%d files of %d bytes in %d directories, median of %d run(s), milliseconds:\n\n", dirs*filesPerDir, fileBytes, dirs, runs)
	table.WriteString("| Scenario | git | gs |\n| --- | ---: | ---: |\n")
	for _, s := range []struct{ key, label string }{
		{"import", "Import directory"},
		{"commit", "Commit one changed file"},
		{"full", "Full clone / checkout"},
		{"sparse", "One-directory clone / checkout"},
	} {
		fmt.Fprintf(&table, "| %s | %s | %s |\n", s.label, benchMedian(samples["git/"+s.key]), benchMedian(samples["gs/"+s.key]))
	}
	for _, name := range slices.Sorted(maps.Keys(samples)) {
		fmt.Fprintf(&table, "\n%s samples: %s", name, benchSamples(samples[name]))
	}
	t.Log("\n" + table.String())
}

// benchSettle runs before each timed step, so no step pays for the one
// before it: it lets the server finish indexing in the background, flushes
// what earlier steps wrote (else a step that syncs, git writing a pack, waits
// behind their writeback), and waits for the machine to go idle.
func benchSettle(t *testing.T, db *sql.DB) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Minute)
	for {
		var pending int
		if err := db.QueryRow(`select count(*) from outbox where processed_at is null`).Scan(&pending); err != nil {
			t.Fatal(err)
		}
		if pending == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("background indexing still has %d rows pending", pending)
		}
		time.Sleep(50 * time.Millisecond)
	}
	if syncPath, err := exec.LookPath("sync"); err == nil {
		_ = exec.Command(syncPath).Run()
	}
	benchWaitForIdleCPU(t)
}

// benchWaitForIdleCPU waits (up to two minutes) until the machine is mostly
// idle, so work left over from a step (Postgres vacuuming after a large
// import, background writes) does not land in the next step's time. It reads
// /proc/stat, so it only waits on Linux.
func benchWaitForIdleCPU(t *testing.T) {
	t.Helper()
	sample := func() (busy, total uint64, ok bool) {
		data, err := os.ReadFile("/proc/stat")
		if err != nil {
			return 0, 0, false
		}
		line, _, _ := strings.Cut(string(data), "\n")
		fields := strings.Fields(line)
		if len(fields) < 5 || fields[0] != "cpu" {
			return 0, 0, false
		}
		for i, field := range fields[1:] {
			v, err := strconv.ParseUint(field, 10, 64)
			if err != nil {
				return 0, 0, false
			}
			total += v
			if i != 3 && i != 4 { // idle and iowait
				busy += v
			}
		}
		return busy, total, true
	}
	deadline := time.Now().Add(2 * time.Minute)
	for time.Now().Before(deadline) {
		busy1, total1, ok := sample()
		if !ok {
			return
		}
		time.Sleep(500 * time.Millisecond)
		busy2, total2, ok := sample()
		if !ok || total2 == total1 {
			return
		}
		if float64(busy2-busy1)/float64(total2-total1) < 0.15 {
			return
		}
	}
	t.Logf("the machine did not go idle; timing anyway")
}

func benchEnvInt(t *testing.T, name string, fallback int) int {
	t.Helper()
	value := os.Getenv(name)
	if value == "" {
		return fallback
	}
	n, err := strconv.Atoi(value)
	if err != nil || n < 1 {
		t.Fatalf("%s must be a positive integer, got %q", name, value)
	}
	return n
}

func benchPath(dir, file int) string {
	return fmt.Sprintf("dir%04d/file%04d.bin", dir, file)
}

// benchFixture is dirs x filesPerDir files of seeded pseudorandom bytes, keyed
// by slash path; the same seed gives the same bytes.
func benchFixture(dirs, filesPerDir, fileBytes int, seed uint64) map[string][]byte {
	rng := rand.New(rand.NewPCG(seed, seed))
	files := make(map[string][]byte, dirs*filesPerDir)
	for d := 0; d < dirs; d++ {
		for f := 0; f < filesPerDir; f++ {
			data := make([]byte, fileBytes)
			for i := 0; i < fileBytes; i += 8 {
				v := rng.Uint64()
				for j := 0; j < 8 && i+j < fileBytes; j++ {
					data[i+j] = byte(v >> (8 * j))
				}
			}
			files[benchPath(d, f)] = data
		}
	}
	return files
}

func benchWriteFixture(t *testing.T, root string, fixture map[string][]byte) {
	t.Helper()
	for name, data := range fixture {
		p := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// benchCheckTree checks that root holds exactly the fixture files under
// prefix ("" for all), byte for byte, ignoring .git.
func benchCheckTree(t *testing.T, root string, fixture map[string][]byte, prefix string) {
	t.Helper()
	seen := 0
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		want, ok := fixture[rel]
		if !ok || !strings.HasPrefix(rel, prefix) {
			return fmt.Errorf("unexpected file %s", rel)
		}
		got, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		if !bytes.Equal(got, want) {
			return fmt.Errorf("%s differs from the fixture", rel)
		}
		seen++
		return nil
	})
	if err != nil {
		t.Fatalf("checkout %s: %v", root, err)
	}
	wantCount := 0
	for name := range fixture {
		if strings.HasPrefix(name, prefix) {
			wantCount++
		}
	}
	if seen != wantCount {
		t.Fatalf("checkout %s has %d files, want %d", root, seen, wantCount)
	}
}

func benchFlipByte(t *testing.T, p string) {
	t.Helper()
	data, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	data[0] ^= 0xff
	if err := os.WriteFile(p, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

// benchGitEnv isolates git from the user's configuration and hooks.
func benchGitEnv(t *testing.T) []string {
	home := t.TempDir()
	return append(os.Environ(),
		"HOME="+home, "XDG_CONFIG_HOME="+home, "GIT_CONFIG_NOSYSTEM=1",
		"GIT_AUTHOR_NAME=bench", "GIT_AUTHOR_EMAIL=bench@example.com",
		"GIT_COMMITTER_NAME=bench", "GIT_COMMITTER_EMAIL=bench@example.com",
	)
}

func benchGit(t *testing.T, env []string, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir, cmd.Env = dir, env
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
}

func benchMedian(samples []time.Duration) string {
	if len(samples) == 0 {
		return "-"
	}
	sorted := slices.Clone(samples)
	slices.Sort(sorted)
	mid := len(sorted) / 2
	median := sorted[mid]
	if len(sorted)%2 == 0 {
		median = (sorted[mid-1] + sorted[mid]) / 2
	}
	return benchMillis(median)
}

func benchSamples(samples []time.Duration) string {
	parts := make([]string, len(samples))
	for i, d := range samples {
		parts[i] = benchMillis(d)
	}
	return strings.Join(parts, ", ")
}

func benchMillis(d time.Duration) string {
	return strconv.FormatFloat(float64(d.Microseconds())/1000, 'f', 1, 64)
}
