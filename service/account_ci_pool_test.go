package service

import (
	"context"
	"testing"

	"gitslice.io/gitslice/internal/authctx"
	"gitslice.io/gitslice/internal/storage"
	corev1 "gitslice.io/gitslice/proto/core/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestAccountCIDaemonPoolDispatchesChecksAcrossSlicesAndAllowsOverride(t *testing.T) {
	mem, handlers := newMemoryHandlers()
	alice := asUser(t, handlers, "user_alice", "alice")
	bob := asUser(t, handlers, "user_bob", "bobby")

	if _, err := handlers.Auth.CreateOrganization(alice, &corev1.CreateOrganizationRequest{Slug: "team"}); err != nil {
		t.Fatal(err)
	}
	if _, err := handlers.Auth.InviteAccountMember(alice, &corev1.InviteAccountMemberRequest{
		Account: "team", Username: "bobby", Role: "writer",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := handlers.Auth.RespondToInvitation(bob, &corev1.RespondToInvitationRequest{
		Account: "team", Accept: true,
	}); err != nil {
		t.Fatal(err)
	}

	// Register two daemons under team, one for the account pool and one for a slice override.
	acctDaemon, err := mem.Agents.RegisterDaemon(alice, storage.AgentDaemonInput{
		SubjectID: "user_alice", Account: "team", Name: "team-ci", Runtime: "ci",
	})
	if err != nil {
		t.Fatal(err)
	}
	overrideDaemon, err := mem.Agents.RegisterDaemon(alice, storage.AgentDaemonInput{
		SubjectID: "user_alice", Account: "team", Name: "special-ci", Runtime: "ci",
	})
	if err != nil {
		t.Fatal(err)
	}

	// Non-admin bob cannot modify the account CI pool.
	if _, err := handlers.Slice.SetAccountCIDaemon(bob, &corev1.SetAccountCIDaemonRequest{
		Account: "team", DaemonId: acctDaemon.Id,
	}); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("bob SetAccountCIDaemon err = %v; want PermissionDenied", err)
	}

	// Admin alice configures the account CI pool.
	resp, err := handlers.Slice.SetAccountCIDaemon(alice, &corev1.SetAccountCIDaemonRequest{
		Account: "team", DaemonId: acctDaemon.Id,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.DaemonIds) != 1 || resp.DaemonIds[0] != acctDaemon.Id || resp.DaemonNames[0] != "team-ci" {
		t.Fatalf("unexpected account CI pool: %+v", resp)
	}

	// Non-admin member bob can read the account CI pool.
	gotPool, err := handlers.Slice.GetAccountCIDaemons(bob, &corev1.GetAccountCIDaemonsRequest{Account: "team"})
	if err != nil || len(gotPool.DaemonIds) != 1 || gotPool.DaemonIds[0] != acctDaemon.Id {
		t.Fatalf("bob GetAccountCIDaemons = %+v, %v", gotPool, err)
	}

	// ListDaemons surfaces ci_accounts on the daemon.
	daemonsResp, err := handlers.Agent.ListDaemons(alice, &corev1.ListDaemonsRequest{})
	if err != nil {
		t.Fatal(err)
	}
	var sawCIAccount bool
	for _, d := range daemonsResp.Daemons {
		if d.Id == acctDaemon.Id && len(d.CiAccounts) == 1 && d.CiAccounts[0] == "team" {
			sawCIAccount = true
		}
	}
	if !sawCIAccount {
		t.Fatalf("expected daemon %s to list ci_accounts=[team], got %+v", acctDaemon.Id, daemonsResp.Daemons)
	}

	// Seed a checks.yaml at /team/.gitslice/checks.yaml and create a brand-new slice
	// with NO per-slice CI daemon configured.
	homeRef := &corev1.SliceRef{Account: "team", Slice: "home"}
	commitSliceFile(t, alice, handlers, mem, homeRef, "/team/.gitslice/checks.yaml", "version: 1\nchecks:\n  unit:\n    run: \"echo ok\"\n")
	commitID := commitSliceFile(t, alice, handlers, mem, homeRef, "/team/web/README.md", "# web\n")

	webRef := &corev1.SliceRef{Account: "team", Slice: "web"}
	if _, err := handlers.Slice.CreateSlice(alice, &corev1.CreateSliceRequest{
		Ref: webRef, IncludedPaths: []string{"/team/web"}, Visibility: "private",
	}); err != nil {
		t.Fatal(err)
	}

	acctConn := handlers.Agent.hub.RegisterDaemon(acctDaemon.Id, storage.InstanceKey(acctDaemon.Id, ""), 4)
	defer handlers.Agent.hub.UnregisterDaemon(acctConn)

	// Bob opens a changeset on team:web (no slice CI daemon set) -> automatically dispatched to team's account CI runner!
	cs, ps := createDispatchTestPatchset(t, bob, handlers, webRef, commitID, "/team/web/app.ts", "export const x = 1;\n", "web change")
	msg := waitForRunChecksMessage(t, acctConn)
	if len(msg.Checks) != 1 || msg.Checks[0].Name != "unit" {
		t.Fatalf("expected unit check on account runner, got %+v", msg.Checks)
	}
	runs, err := handlers.Check.ListCheckRuns(bob, &corev1.ListCheckRunsRequest{ChangesetId: cs.Id, PatchsetId: ps.Id})
	if err != nil || len(runs.Runs) != 1 || runs.Runs[0].DaemonId != acctDaemon.Id {
		t.Fatalf("runs = %+v, %v; want daemon %s", runs, err, acctDaemon.Id)
	}

	// Slice override takes precedence over account CI pool when explicitly configured.
	overrideConn := handlers.Agent.hub.RegisterDaemon(overrideDaemon.Id, storage.InstanceKey(overrideDaemon.Id, ""), 4)
	defer handlers.Agent.hub.UnregisterDaemon(overrideConn)
	if _, err := handlers.Slice.SetSliceCIDaemon(alice, &corev1.SetSliceCIDaemonRequest{
		Slice: webRef, DaemonId: overrideDaemon.Id,
	}); err != nil {
		t.Fatal(err)
	}
	cs2, ps2 := createDispatchTestPatchset(t, bob, handlers, webRef, commitID, "/team/web/other.ts", "export const y = 2;\n", "override change")
	msg2 := waitForRunChecksMessage(t, overrideConn)
	if len(msg2.Checks) != 1 || msg2.Checks[0].Name != "unit" {
		t.Fatalf("expected unit check on override runner, got %+v", msg2.Checks)
	}
	runs2, err := handlers.Check.ListCheckRuns(bob, &corev1.ListCheckRunsRequest{ChangesetId: cs2.Id, PatchsetId: ps2.Id})
	if err != nil || len(runs2.Runs) != 1 || runs2.Runs[0].DaemonId != overrideDaemon.Id {
		t.Fatalf("runs2 = %+v, %v; want override daemon %s", runs2, err, overrideDaemon.Id)
	}

	// Clearing the slice override reverts to the account pool, and removing the account runner refuses queued runs.
	if _, err := handlers.Slice.SetSliceCIDaemon(alice, &corev1.SetSliceCIDaemonRequest{Slice: webRef}); err != nil {
		t.Fatal(err)
	}
	if _, err := handlers.Slice.SetAccountCIDaemon(alice, &corev1.SetAccountCIDaemonRequest{
		Account: "team", DaemonId: acctDaemon.Id, Remove: true,
	}); err != nil {
		t.Fatal(err)
	}
	sliceObj, err := mem.Slices.Resolve(alice, webRef)
	if err != nil {
		t.Fatal(err)
	}
	if refusal := handlers.Agent.dispatcher.ciDaemonRefusalForSlice(authctx.WithSubjectID(context.Background(), "user_alice"), sliceObj, acctDaemon.Id); refusal == "" {
		t.Fatalf("expected removed account runner to be refused on rebuild")
	}
}
