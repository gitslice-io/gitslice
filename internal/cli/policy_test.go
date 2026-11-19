package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	corev1 "gitslice.io/gitslice/proto/core/v1"
)

func TestPrintFolderPolicyAndAccountCIRunners(t *testing.T) {
	resp := &corev1.FolderPolicyResponse{
		Folder:            "/acme/web",
		Account:           "acme",
		CommitId:          "commit_123",
		OwnerConfigPath:   "/acme/.gitslice/owners.yaml",
		OwnerSourceFolder: "/acme",
		OwnerRules: []*corev1.FolderOwnerRule{
			{Paths: []string{"**"}, Owners: []string{"alice", "@acme/web"}, Require: 1, RequireHuman: 1},
		},
		Checks: []*corev1.FolderCheckDefinition{
			{Id: "lint", Name: "lint", Folder: "/acme", ConfigPath: "/acme/.gitslice/checks.yaml", Run: "go vet ./...", Required: true},
			{Id: "web/build", Name: "build", Folder: "/acme/web", ConfigPath: "/acme/web/.gitslice/checks.yaml", Run: "npm run build", Required: false},
		},
		Triggers: []*corev1.TriggerDefinition{
			{Id: "web/deploy", Name: "deploy", Folder: "/acme/web", On: []string{"push", "manual"}, Action: "http", HttpUrl: "https://hooks.example.com/deploy"},
		},
		Secrets: []*corev1.FolderSecretEntry{
			{Name: "SHARED_KEY", SecretId: "sec_1", SourceFolder: "/acme", Paths: []string{"/acme"}},
			{Name: "WEB_TOKEN", SecretId: "sec_2", SourceFolder: "/acme/web", Paths: []string{"/acme/web"}},
		},
		CanManageSecrets: true,
		CiDaemonIds:      []string{"daemon_1"},
		CiDaemonNames:    []string{"org-ci"},
		Problems: []*corev1.FolderPolicyProblem{
			{Kind: "triggers", Folder: "/acme/web", File: "/acme/web/.gitslice/triggers.yaml", Message: "bad syntax"},
		},
	}

	out := folderPolicyFromProto(resp)
	var stdout, stderr bytes.Buffer
	r := Runner{Stdout: &stdout, Stderr: &stderr}
	if err := r.printFolderPolicy(out); err != nil {
		t.Fatalf("printFolderPolicy: %v", err)
	}
	text := stdout.String()
	for _, want := range []string{
		"folder: /acme/web",
		"source: /acme/.gitslice/owners.yaml",
		"require=1 require_human=1",
		"lint [required, from /acme]",
		"web/build\tnpm run build",
		"web/deploy\ton push,manual\thttp https://hooks.example.com/deploy",
		"SHARED_KEY\t(inherited from /acme)",
		"WEB_TOKEN\t(defined here)",
		"1. org-ci (daemon_1)",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("printFolderPolicy output missing %q:\n%s", want, text)
		}
	}
	if !strings.Contains(stderr.String(), "problem (triggers) /acme/web/.gitslice/triggers.yaml: bad syntax") {
		t.Fatalf("stderr missing problem: %q", stderr.String())
	}

	var ciBuf bytes.Buffer
	if err := (Runner{Stdout: &ciBuf}).printAccountCIRunners(accountCIRunnersOutput{
		Account:     "acme",
		DaemonIDs:   []string{"daemon_1", "daemon_2"},
		DaemonNames: []string{"primary", "backup"},
	}); err != nil {
		t.Fatalf("printAccountCIRunners: %v", err)
	}
	if !strings.Contains(ciBuf.String(), "1. primary (daemon_1)") || !strings.Contains(ciBuf.String(), "2. backup (daemon_2)") {
		t.Fatalf("unexpected ci-runners output:\n%s", ciBuf.String())
	}
}

func TestSecretCreateDefaultsToWorkspaceFolder(t *testing.T) {
	// Outside any workspace, omitting --path returns scope_required.
	tmpOutside := t.TempDir()
	var stdout, stderr bytes.Buffer
	r := Runner{
		Dir:    tmpOutside,
		Stdin:  strings.NewReader("supersecret"),
		Stdout: &stdout,
		Stderr: &stderr,
	}
	err := r.Run(context.Background(), []string{"secret", "create", "MY_TOKEN"})
	if !isUserErrorCode(err, "scope_required") {
		t.Fatalf("secret create outside workspace err = %v, want scope_required", err)
	}

	// Inside a workspace folder (/acme/web), triggerFolder("") resolves /acme/web
	// so secret create without --path defaults to that folder.
	wsRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(wsRoot, ".gs"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wsRoot, ".gs", "slice.json"), []byte(`{"account":"acme","slice":"home"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	sub := filepath.Join(wsRoot, "acme", "web")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	rIn := Runner{Dir: sub}
	folder, err := rIn.triggerFolder("")
	if err != nil {
		t.Fatalf("triggerFolder inside workspace: %v", err)
	}
	if folder != "/acme/web" {
		t.Fatalf("folder = %q, want /acme/web", folder)
	}
}
