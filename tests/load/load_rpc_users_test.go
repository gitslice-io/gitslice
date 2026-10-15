//go:build load

package load_test

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"testing"
	"time"

	"gitslice.io/gitslice/internal/auth/servicetoken"
	"gitslice.io/gitslice/internal/objectid"
	"gitslice.io/gitslice/internal/objectstore/filesystem"
	"gitslice.io/gitslice/internal/postgres"
	"gitslice.io/gitslice/internal/treestore"
	corev1 "gitslice.io/gitslice/proto/core/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

func TestLoadRPCMultiUserPersonalAccounts(t *testing.T) {
	ts := startLoadServer(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	conn := dialLoadGRPC(t, ts.addr)
	defer conn.Close()

	userCount := envInt("GITSLICE_LOAD_RPC_USERS", 12)
	opsPerUser := envInt("GITSLICE_LOAD_RPC_OPS_PER_USER", 4)

	var signupWG sync.WaitGroup
	signupDurations := make(chan time.Duration, userCount)
	userCh := make(chan rpcLoadUser, userCount)
	errs := make(chan error, userCount)
	signupStart := time.Now()
	for i := range userCount {
		signupWG.Add(1)
		go func(i int) {
			defer signupWG.Done()
			begin := time.Now()
			user, err := ts.signupRPCLoadUser(ctx, conn, i)
			signupDurations <- time.Since(begin)
			if err != nil {
				errs <- fmt.Errorf("signup user %d: %w", i, err)
				return
			}
			userCh <- user
		}(i)
	}
	signupWG.Wait()
	close(signupDurations)
	close(userCh)
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	users := make([]rpcLoadUser, 0, userCount)
	for user := range userCh {
		users = append(users, user)
	}
	sort.Slice(users, func(i, j int) bool { return users[i].index < users[j].index })
	if len(users) != userCount {
		t.Fatalf("signed up %d users, want %d", len(users), userCount)
	}
	reportDurations(t, "rpc_multi_user_signup", userCount, time.Since(signupStart), drainDurations(signupDurations))

	var seedWG sync.WaitGroup
	seedDurations := make(chan time.Duration, userCount)
	errs = make(chan error, userCount)
	seedStart := time.Now()
	for _, user := range users {
		seedWG.Add(1)
		go func(user rpcLoadUser) {
			defer seedWG.Done()
			begin := time.Now()
			if err := seedRPCUserHome(user); err != nil {
				errs <- fmt.Errorf("%s seed home: %w", user.account, err)
				return
			}
			seedDurations <- time.Since(begin)
		}(user)
	}
	seedWG.Wait()
	close(seedDurations)
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	reportDurations(t, "rpc_multi_user_seed_home_and_slice", userCount, time.Since(seedStart), drainDurations(seedDurations))

	if len(users) > 1 {
		for i, user := range users {
			other := users[(i+1)%len(users)]
			if err := assertRPCUserIsolation(user, other.account); err != nil {
				t.Fatal(err)
			}
		}
	}

	totalOps := userCount * opsPerUser
	var opsWG sync.WaitGroup
	opDurations := make(chan time.Duration, totalOps)
	errs = make(chan error, totalOps)
	opsStart := time.Now()
	for _, user := range users {
		opsWG.Add(1)
		go func(user rpcLoadUser) {
			defer opsWG.Done()
			for op := range opsPerUser {
				duration, err := runRPCUserOperation(user, op)
				if err != nil {
					errs <- fmt.Errorf("%s operation %d: %w", user.account, op, err)
					return
				}
				opDurations <- duration
			}
		}(user)
	}
	opsWG.Wait()
	close(opDurations)
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	reportDurations(t, "rpc_multi_user_custom_slice_ops", totalOps, time.Since(opsStart), drainDurations(opDurations))

	db, err := postgres.Open(ctx, databaseURLWithSearchPath(t, ts.databaseURL, ts.schema))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	objectStore, err := filesystem.New(ts.objectRoot)
	if err != nil {
		t.Fatal(err)
	}
	db.SetTreeStore(treestore.New(objectStore))
	assertLoadIntegrity(t, ctx, db, objectStore)
}

type rpcLoadUser struct {
	index     int
	username  string
	account   string
	subjectID string
	ctx       context.Context
	repo      corev1.RepositoryServiceClient
	blob      corev1.BlobServiceClient
	changeset corev1.ChangesetServiceClient
	slices    corev1.SliceServiceClient
}

func (ts *loadServer) signupRPCLoadUser(ctx context.Context, conn *grpc.ClientConn, index int) (rpcLoadUser, error) {
	username := fmt.Sprintf("load-rpc-%02d", index)
	token, account, subjectID, err := ts.provisionAccountNoFatal(ctx, conn, username, username)
	if err != nil {
		return rpcLoadUser{}, err
	}
	authCtx := metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer "+token)
	return rpcLoadUser{
		index:     index,
		username:  username,
		account:   account,
		subjectID: subjectID,
		ctx:       authCtx,
		repo:      corev1.NewRepositoryServiceClient(conn),
		blob:      corev1.NewBlobServiceClient(conn),
		changeset: corev1.NewChangesetServiceClient(conn),
		slices:    corev1.NewSliceServiceClient(conn),
	}, nil
}

func (ts *loadServer) provisionAccountNoFatal(ctx context.Context, conn *grpc.ClientConn, username, label string) (string, string, string, error) {
	subject := "svc_" + loadTokenLabelString(username+"_"+label)
	email := loadTokenLabelString(username+"_"+label) + "@test.local"
	token, err := servicetoken.Mint(ts.servicePriv, subject, email, servicetoken.DefaultIssuer, time.Hour)
	if err != nil {
		return "", "", "", err
	}
	authCtx := metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer "+token)
	chosen, err := corev1.NewAuthServiceClient(conn).ChooseUsername(authCtx, &corev1.ChooseUsernameRequest{Username: username})
	if err != nil {
		return "", "", "", err
	}
	if chosen.Account == "" || chosen.SubjectId == "" {
		return "", "", "", fmt.Errorf("incomplete ChooseUsername response: %#v", chosen)
	}
	return token, chosen.Account, chosen.SubjectId, nil
}

func seedRPCUserHome(user rpcLoadUser) error {
	home, err := user.slices.ResolveSlice(user.ctx, &corev1.ResolveSliceRequest{
		Ref: &corev1.SliceRef{Account: user.account, Slice: "home"},
	})
	if err != nil {
		return err
	}
	if len(home.Definition.IncludedPaths) != 1 || home.Definition.IncludedPaths[0] != "/"+user.account {
		return fmt.Errorf("home slice includes %v, want /%s", home.Definition.IncludedPaths, user.account)
	}

	projectPath := "/" + user.account + "/project"
	emptyPath := projectPath + "/empty-seed"
	readme, err := rpcUploadFileEdit(user, projectPath+"/readme.txt", []byte("hello from "+user.username+"\n"))
	if err != nil {
		return err
	}
	commitID, _, err := rpcSubmitEdits(user, "home", "seed personal home", []*corev1.FileEdit{
		{Op: "mkdir", Path: projectPath},
		{Op: "mkdir", Path: emptyPath},
		readme,
	})
	if err != nil {
		return err
	}
	accountList, err := user.repo.ListDirectory(user.ctx, &corev1.ListDirectoryRequest{
		CommitId: commitID,
		Path:     "/" + user.account,
		Slice:    &corev1.SliceRef{Account: user.account, Slice: "home"},
	})
	if err != nil {
		return err
	}
	if err := requireEntry(accountList.Entries, projectPath, corev1.EntryKind_ENTRY_KIND_DIRECTORY); err != nil {
		return err
	}
	if _, err := user.slices.CreateSlice(user.ctx, &corev1.CreateSliceRequest{
		Ref:           &corev1.SliceRef{Account: user.account, Slice: "project"},
		IncludedPaths: []string{projectPath},
		Visibility:    "private",
	}); err != nil {
		return err
	}
	ref, err := user.repo.GetRef(user.ctx, &corev1.GetRefRequest{RefName: postgres.DefaultTargetRef})
	if err != nil {
		return err
	}
	projectList, err := user.repo.ListDirectory(user.ctx, &corev1.ListDirectoryRequest{
		CommitId: ref.CommitId,
		Path:     projectPath,
		Slice:    &corev1.SliceRef{Account: user.account, Slice: "project"},
	})
	if err != nil {
		return err
	}
	if err := requireEntry(projectList.Entries, emptyPath, corev1.EntryKind_ENTRY_KIND_DIRECTORY); err != nil {
		return fmt.Errorf("custom slice did not preserve empty directory: %w", err)
	}
	return requireEntry(projectList.Entries, projectPath+"/readme.txt", corev1.EntryKind_ENTRY_KIND_FILE)
}

func assertRPCUserIsolation(user rpcLoadUser, otherAccount string) error {
	_, err := user.slices.ResolveSlice(user.ctx, &corev1.ResolveSliceRequest{
		Ref: &corev1.SliceRef{Account: otherAccount, Slice: "home"},
	})
	if status.Code(err) != codes.PermissionDenied {
		return fmt.Errorf("resolve other home returned %v, want PermissionDenied", err)
	}
	_, _, err = rpcSubmitEdits(user, "home", "outside home should fail", []*corev1.FileEdit{
		{Op: "mkdir", Path: "/" + otherAccount + "/illegal"},
	})
	if status.Code(err) != codes.FailedPrecondition {
		return fmt.Errorf("outside-home mutation returned %v, want FailedPrecondition", err)
	}
	return nil
}

func runRPCUserOperation(user rpcLoadUser, operation int) (time.Duration, error) {
	projectPath := "/" + user.account + "/project"
	srcPath := projectPath + "/src"
	filePath := fmt.Sprintf("%s/user_%02d_op_%02d.txt", srcPath, user.index, operation)
	content := []byte(fmt.Sprintf("user=%s\noperation=%d\n", user.username, operation))
	fileEdit, err := rpcUploadFileEdit(user, filePath, content)
	if err != nil {
		return 0, err
	}
	commitID, duration, err := rpcSubmitEdits(user, "project", fmt.Sprintf("rpc operation %02d", operation), []*corev1.FileEdit{
		{Op: "mkdir", Path: srcPath},
		fileEdit,
	})
	if err != nil {
		return 0, err
	}
	read, err := user.repo.ReadFile(user.ctx, &corev1.ReadFileRequest{CommitId: commitID, Path: filePath})
	if err != nil {
		return 0, err
	}
	if string(read.Data) != string(content) {
		return 0, fmt.Errorf("read %s got %q, want %q", filePath, string(read.Data), string(content))
	}
	ref, err := user.repo.GetRef(user.ctx, &corev1.GetRefRequest{RefName: postgres.DefaultTargetRef})
	if err != nil {
		return 0, err
	}
	list, err := user.repo.ListDirectory(user.ctx, &corev1.ListDirectoryRequest{
		CommitId: ref.CommitId,
		Path:     srcPath,
		Slice:    &corev1.SliceRef{Account: user.account, Slice: "project"},
	})
	if err != nil {
		return 0, err
	}
	if err := requireEntry(list.Entries, filePath, corev1.EntryKind_ENTRY_KIND_FILE); err != nil {
		return 0, err
	}
	// Path-filtered history reads the commit_changed_paths derived index,
	// which the outbox worker fills shortly after publish (design/06).
	// Poll briefly like a real client instead of assuming read-your-writes.
	deadline := time.Now().Add(5 * time.Second)
	lastCount := 0
	for {
		history, err := user.repo.ListCommits(user.ctx, &corev1.ListCommitsRequest{
			RefName: postgres.DefaultTargetRef,
			Limit:   10,
			Path:    filePath,
			Slice:   &corev1.SliceRef{Account: user.account, Slice: "project"},
		})
		if err != nil {
			return 0, err
		}
		for _, commit := range history.Commits {
			if commit.Id == commitID {
				return duration, nil
			}
		}
		lastCount = len(history.Commits)
		if time.Now().After(deadline) {
			break
		}
		select {
		case <-user.ctx.Done():
			return 0, user.ctx.Err()
		case <-time.After(25 * time.Millisecond):
		}
	}
	return 0, fmt.Errorf("commit %s missing from ListCommits(%s), got %d commits", commitID, filePath, lastCount)
}

func rpcUploadFileEdit(user rpcLoadUser, p string, content []byte) (*corev1.FileEdit, error) {
	upload, err := user.blob.UploadBlob(user.ctx, &corev1.UploadBlobRequest{
		ContentHash: objectid.RawContentHash(content),
		Data:        content,
		// Authorize against the home slice: it covers the whole account
		// root and exists from signup, while the project slice is only
		// created later in the seed flow.
		Slice: &corev1.SliceRef{Account: user.account, Slice: "home"},
	})
	if err != nil {
		return nil, err
	}
	return &corev1.FileEdit{
		Op:          "upsert",
		Path:        p,
		BlobId:      upload.BlobId,
		ContentHash: upload.ContentHash,
		Mode:        0o100644,
	}, nil
}

func rpcSubmitEdits(user rpcLoadUser, sliceSlug, title string, edits []*corev1.FileEdit) (string, time.Duration, error) {
	begin := time.Now()
	ref, err := user.repo.GetRef(user.ctx, &corev1.GetRefRequest{RefName: postgres.DefaultTargetRef})
	if err != nil {
		return "", 0, err
	}
	cs, err := user.changeset.CreateChangeset(user.ctx, &corev1.CreateChangesetRequest{
		AuthoringSlice: &corev1.SliceRef{Account: user.account, Slice: sliceSlug},
		TargetRef:      postgres.DefaultTargetRef,
		BaseCommitId:   ref.CommitId,
		Title:          title,
	})
	if err != nil {
		return "", 0, err
	}
	patchset, err := user.changeset.UpdateChangeset(user.ctx, &corev1.UpdateChangesetRequest{
		ChangesetId:  cs.Id,
		BaseCommitId: ref.CommitId,
		FileEdits:    edits,
	})
	if err != nil {
		return "", 0, err
	}
	if _, err := user.changeset.SubmitChangeset(user.ctx, &corev1.SubmitChangesetRequest{
		ChangesetId:               cs.Id,
		ExpectedCurrentPatchsetId: patchset.Id,
	}); err != nil {
		return "", 0, err
	}
	commitID, err := waitForRPCSubmittedWithTimeout(user.ctx, user.changeset, cs.Id, loadPublishTimeout(len(edits)))
	if err != nil {
		return "", 0, err
	}
	return commitID, time.Since(begin), nil
}

func waitForRPCSubmitted(ctx context.Context, client corev1.ChangesetServiceClient, changesetID string) (string, error) {
	return waitForRPCSubmittedWithTimeout(ctx, client, changesetID, 30*time.Second)
}

func waitForRPCSubmittedWithTimeout(ctx context.Context, client corev1.ChangesetServiceClient, changesetID string, timeout time.Duration) (string, error) {
	deadline := time.Now().Add(timeout)
	for {
		cs, err := client.GetChangeset(ctx, &corev1.GetChangesetRequest{ChangesetId: changesetID})
		if err != nil {
			return "", err
		}
		if cs.Status == "submitted" && cs.CommitId != "" {
			return cs.CommitId, nil
		}
		if time.Now().After(deadline) {
			return "", fmt.Errorf("changeset %s was not published before timeout, last status %s", changesetID, cs.Status)
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(10 * time.Millisecond):
		}
	}
}
