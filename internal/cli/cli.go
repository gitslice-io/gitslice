package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gitslice-io/gitslice/internal/objectid"
	"github.com/gitslice-io/gitslice/internal/paths"
	"github.com/gitslice-io/gitslice/internal/postgres"
	"github.com/gitslice-io/gitslice/internal/rpcjson"
	"github.com/gitslice-io/gitslice/proto/core/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
)

type Runner struct {
	Stdout io.Writer
	Stderr io.Writer
	Dir    string
	Home   string
}

type UserConfig struct {
	ServerAddr string `json:"server_addr"`
	Token      string `json:"token"`
	SubjectID  string `json:"subject_id"`
}

type WorkspaceConfig struct {
	Account        string   `json:"account"`
	Slice          string   `json:"slice"`
	SliceID        string   `json:"slice_id"`
	DefinitionHash string   `json:"definition_hash"`
	IncludedPaths  []string `json:"included_paths"`
	BaseCommitID   string   `json:"base_commit_id"`
}

type WorkspaceState struct {
	CurrentChangesetID string `json:"current_changeset_id"`
	CurrentPatchsetID  string `json:"current_patchset_id"`
	BaseCommitID       string `json:"base_commit_id"`
}

func Main(args []string, stdout, stderr io.Writer) int {
	r := Runner{Stdout: stdout, Stderr: stderr}
	if err := r.Run(context.Background(), args); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return 0
}

func (r Runner) Run(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return r.usage()
	}
	switch args[0] {
	case "auth":
		return r.runAuth(ctx, args[1:])
	case "workspace":
		return r.runWorkspace(ctx, args[1:])
	case "status":
		return r.runStatus(ctx, args[1:])
	case "cs":
		return r.runChangeset(ctx, args[1:])
	case "slice":
		return fmt.Errorf("multi-slice workspace commands are not supported; use gs workspace init <account>/<slice>")
	default:
		return r.usage()
	}
}

func (r Runner) usage() error {
	return fmt.Errorf("usage: gs auth login | gs workspace init <account>/<slice> | gs status | gs cs create|submit|status")
}

func (r Runner) runAuth(ctx context.Context, args []string) error {
	if len(args) == 0 || args[0] != "login" {
		return fmt.Errorf("usage: gs auth login [--server addr] [--dev-user alice]")
	}
	fs := flag.NewFlagSet("auth login", flag.ContinueOnError)
	fs.SetOutput(r.Stderr)
	serverAddr := fs.String("server", defaultServerAddr(), "server gRPC address")
	devUser := fs.String("dev-user", "alice", "development user")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	conn, err := dial(ctx, *serverAddr)
	if err != nil {
		return err
	}
	defer conn.Close()
	res, err := corev1.NewFakeAccountServiceClient(conn).Login(ctx, &corev1.LoginRequest{DevUser: *devUser})
	if err != nil {
		return err
	}
	cfg := UserConfig{ServerAddr: *serverAddr, Token: res.Token, SubjectID: res.SubjectID}
	if err := r.writeUserConfig(cfg); err != nil {
		return err
	}
	fmt.Fprintf(r.Stdout, "logged in as %s\n", res.SubjectID)
	return nil
}

func (r Runner) runWorkspace(ctx context.Context, args []string) error {
	if len(args) == 0 || args[0] != "init" || len(args) < 2 {
		return fmt.Errorf("usage: gs workspace init <account>/<slice>")
	}
	cfg, err := r.readUserConfig()
	if err != nil {
		return err
	}
	ref, err := parseSliceRef(args[1])
	if err != nil {
		return err
	}
	conn, err := dial(ctx, cfg.ServerAddr)
	if err != nil {
		return err
	}
	defer conn.Close()
	callCtx := authContext(ctx, cfg)
	slice, err := corev1.NewSliceServiceClient(conn).ResolveSlice(callCtx, &corev1.ResolveSliceRequest{Ref: ref})
	if err != nil {
		return err
	}
	refRecord, err := corev1.NewRepositoryServiceClient(conn).GetRef(callCtx, &corev1.GetRefRequest{RefName: postgres.DefaultTargetRef})
	if err != nil {
		return err
	}
	workspace := WorkspaceConfig{
		Account:        ref.Account,
		Slice:          ref.Slice,
		SliceID:        slice.ID,
		DefinitionHash: slice.DefinitionHash,
		IncludedPaths:  slice.Definition.IncludedPaths,
		BaseCommitID:   refRecord.CommitID,
	}
	if err := r.writeWorkspaceConfig(workspace); err != nil {
		return err
	}
	if err := r.writeWorkspaceState(WorkspaceState{BaseCommitID: refRecord.CommitID}); err != nil {
		return err
	}
	fmt.Fprintf(r.Stdout, "initialized workspace for %s/%s\n", ref.Account, ref.Slice)
	return nil
}

func (r Runner) runStatus(ctx context.Context, args []string) error {
	format := parseFormat(args)
	cfg, ws, state, err := r.loadLocalState()
	if err != nil {
		return err
	}
	edits, err := r.snapshotEdits(ctx, nil, cfg, ws, false)
	if err != nil {
		return err
	}
	conn, err := dial(ctx, cfg.ServerAddr)
	if err != nil {
		return err
	}
	defer conn.Close()
	validation, err := corev1.NewWorkspaceServiceClient(conn).ValidateWorkspaceDiff(authContext(ctx, cfg), &corev1.ValidateWorkspaceDiffRequest{
		Workspace:    workspaceRef(ws),
		BaseCommitID: state.BaseCommitID,
		FileEdits:    edits,
	})
	if err != nil {
		return err
	}
	if format == "json" {
		return writeJSON(r.Stdout, map[string]any{
			"workspace":          ws.Account + "/" + ws.Slice,
			"changed_path_count": len(validation.AffectedPaths),
			"changed_paths":      validation.AffectedPaths,
			"changeset_id":       state.CurrentChangesetID,
			"patchset_id":        state.CurrentPatchsetID,
		})
	}
	fmt.Fprintf(r.Stdout, "workspace: %s/%s\n", ws.Account, ws.Slice)
	if len(validation.AffectedPaths) == 0 {
		fmt.Fprintln(r.Stdout, "status: clean")
		return nil
	}
	fmt.Fprintf(r.Stdout, "status: %d changed path(s)\n", len(validation.AffectedPaths))
	for _, p := range validation.AffectedPaths {
		fmt.Fprintf(r.Stdout, "  %s\n", p)
	}
	return nil
}

func (r Runner) runChangeset(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: gs cs create|submit|status")
	}
	switch args[0] {
	case "create":
		return r.runChangesetCreate(ctx, args[1:])
	case "submit":
		return r.runChangesetSubmit(ctx, args[1:])
	case "status":
		return r.runChangesetStatus(ctx, args[1:])
	default:
		return fmt.Errorf("usage: gs cs create|submit|status")
	}
}

func (r Runner) runChangesetCreate(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("cs create", flag.ContinueOnError)
	fs.SetOutput(r.Stderr)
	title := fs.String("title", "CLI changeset", "changeset title")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg, ws, state, err := r.loadLocalState()
	if err != nil {
		return err
	}
	conn, err := dial(ctx, cfg.ServerAddr)
	if err != nil {
		return err
	}
	defer conn.Close()
	edits, err := r.snapshotEdits(ctx, conn, cfg, ws, true)
	if err != nil {
		return err
	}
	callCtx := authContext(ctx, cfg)
	changesetClient := corev1.NewChangesetServiceClient(conn)
	cs, err := changesetClient.CreateChangeset(callCtx, &corev1.CreateChangesetRequest{
		AuthoringSlice: &corev1.SliceRef{Account: ws.Account, Slice: ws.Slice},
		TargetRef:      postgres.DefaultTargetRef,
		BaseCommitID:   state.BaseCommitID,
		Title:          *title,
	})
	if err != nil {
		return err
	}
	patchset, err := changesetClient.UpdateChangeset(callCtx, &corev1.UpdateChangesetRequest{
		ChangesetID:  cs.ID,
		BaseCommitID: state.BaseCommitID,
		FileEdits:    edits,
	})
	if err != nil {
		return err
	}
	state.CurrentChangesetID = cs.ID
	state.CurrentPatchsetID = patchset.ID
	if err := r.writeWorkspaceState(state); err != nil {
		return err
	}
	fmt.Fprintf(r.Stdout, "created changeset %s patchset %s\n", cs.ID, patchset.ID)
	return nil
}

func (r Runner) runChangesetSubmit(ctx context.Context, args []string) error {
	_ = args
	cfg, _, state, err := r.loadLocalState()
	if err != nil {
		return err
	}
	if state.CurrentChangesetID == "" {
		return fmt.Errorf("no current changeset in workspace")
	}
	conn, err := dial(ctx, cfg.ServerAddr)
	if err != nil {
		return err
	}
	defer conn.Close()
	res, err := corev1.NewChangesetServiceClient(conn).SubmitChangeset(authContext(ctx, cfg), &corev1.SubmitChangesetRequest{
		ChangesetID:               state.CurrentChangesetID,
		ExpectedCurrentPatchsetID: state.CurrentPatchsetID,
	})
	if err != nil {
		return err
	}
	state.BaseCommitID = res.NewRefCommitID
	if err := r.writeWorkspaceState(state); err != nil {
		return err
	}
	fmt.Fprintf(r.Stdout, "submitted %s to %s\n", res.CommitID, res.TargetRef)
	return nil
}

func (r Runner) runChangesetStatus(ctx context.Context, args []string) error {
	format := parseFormat(args)
	cfg, _, state, err := r.loadLocalState()
	if err != nil {
		return err
	}
	if state.CurrentChangesetID == "" {
		return fmt.Errorf("no current changeset in workspace")
	}
	conn, err := dial(ctx, cfg.ServerAddr)
	if err != nil {
		return err
	}
	defer conn.Close()
	cs, err := corev1.NewChangesetServiceClient(conn).GetChangeset(authContext(ctx, cfg), &corev1.GetChangesetRequest{ChangesetID: state.CurrentChangesetID})
	if err != nil {
		return err
	}
	if format == "json" {
		return writeJSON(r.Stdout, cs)
	}
	fmt.Fprintf(r.Stdout, "changeset: %s\nstatus: %s\npatchset: %s\n", cs.ID, cs.Status, cs.CurrentPatchsetID)
	return nil
}

func (r Runner) snapshotEdits(ctx context.Context, conn *grpc.ClientConn, cfg UserConfig, ws WorkspaceConfig, upload bool) ([]*corev1.FileEdit, error) {
	root := r.cwd()
	if len(ws.IncludedPaths) == 0 {
		return nil, fmt.Errorf("workspace has no included paths")
	}
	var blobClient corev1.BlobServiceClient
	callCtx := ctx
	if upload {
		if conn == nil {
			return nil, fmt.Errorf("connection is required for blob upload")
		}
		blobClient = corev1.NewBlobServiceClient(conn)
		callCtx = authContext(ctx, cfg)
	}
	var edits []*corev1.FileEdit
	err := filepath.WalkDir(root, func(p string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if p == root {
			return nil
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if shouldSkip(rel, entry) {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		globalPath, err := paths.FromWorkspacePath(ws.IncludedPaths[0], rel)
		if err != nil {
			return err
		}
		contentHash := objectid.RawContentHash(data)
		mode := uint32(0o100644)
		if info.Mode()&0o111 != 0 {
			mode = 0o100755
		}
		edit := &corev1.FileEdit{Op: "upsert", Path: globalPath, ContentHash: contentHash, Mode: mode}
		if upload {
			uploadRes, err := blobClient.UploadBlob(callCtx, &corev1.UploadBlobRequest{ContentHash: contentHash, Data: data})
			if err != nil {
				return err
			}
			edit.BlobID = uploadRes.BlobID
		}
		edits = append(edits, edit)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return edits, nil
}

func (r Runner) loadLocalState() (UserConfig, WorkspaceConfig, WorkspaceState, error) {
	cfg, err := r.readUserConfig()
	if err != nil {
		return UserConfig{}, WorkspaceConfig{}, WorkspaceState{}, err
	}
	ws, err := r.readWorkspaceConfig()
	if err != nil {
		return UserConfig{}, WorkspaceConfig{}, WorkspaceState{}, err
	}
	state, err := r.readWorkspaceState()
	if err != nil {
		return UserConfig{}, WorkspaceConfig{}, WorkspaceState{}, err
	}
	return cfg, ws, state, nil
}

func (r Runner) readUserConfig() (UserConfig, error) {
	var cfg UserConfig
	if err := readJSONFile(r.userConfigPath(), &cfg); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return cfg, fmt.Errorf("not logged in; run gs auth login")
		}
		return cfg, err
	}
	if cfg.ServerAddr == "" || cfg.Token == "" {
		return cfg, fmt.Errorf("invalid user config; run gs auth login again")
	}
	return cfg, nil
}

func (r Runner) writeUserConfig(cfg UserConfig) error {
	if err := os.MkdirAll(filepath.Dir(r.userConfigPath()), 0o700); err != nil {
		return err
	}
	return writeJSONFile(r.userConfigPath(), cfg, 0o600)
}

func (r Runner) readWorkspaceConfig() (WorkspaceConfig, error) {
	var cfg WorkspaceConfig
	if err := readJSONFile(filepath.Join(r.cwd(), ".gs", "slice.json"), &cfg); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return cfg, fmt.Errorf("not in a gitslice workspace; run gs workspace init <account>/<slice>")
		}
		return cfg, err
	}
	return cfg, nil
}

func (r Runner) writeWorkspaceConfig(cfg WorkspaceConfig) error {
	dir := filepath.Join(r.cwd(), ".gs")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	return writeJSONFile(filepath.Join(dir, "slice.json"), cfg, 0o644)
}

func (r Runner) readWorkspaceState() (WorkspaceState, error) {
	var state WorkspaceState
	err := readJSONFile(filepath.Join(r.cwd(), ".gs", "state.json"), &state)
	if errors.Is(err, os.ErrNotExist) {
		return state, nil
	}
	return state, err
}

func (r Runner) writeWorkspaceState(state WorkspaceState) error {
	dir := filepath.Join(r.cwd(), ".gs")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	return writeJSONFile(filepath.Join(dir, "state.json"), state, 0o644)
}

func (r Runner) userConfigPath() string {
	home := r.Home
	if home == "" {
		home, _ = os.UserHomeDir()
	}
	return filepath.Join(home, ".gitslice", "config.json")
}

func (r Runner) cwd() string {
	if r.Dir != "" {
		return r.Dir
	}
	dir, _ := os.Getwd()
	return dir
}

func parseSliceRef(value string) (*corev1.SliceRef, error) {
	parts := strings.Split(value, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return nil, fmt.Errorf("slice must be account/slice")
	}
	return &corev1.SliceRef{Account: parts[0], Slice: parts[1]}, nil
}

func workspaceRef(ws WorkspaceConfig) *corev1.WorkspaceRef {
	return &corev1.WorkspaceRef{ID: ws.Account + "/" + ws.Slice}
}

func authContext(ctx context.Context, cfg UserConfig) context.Context {
	return metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer "+cfg.Token)
}

func dial(ctx context.Context, addr string) (*grpc.ClientConn, error) {
	rpcjson.Register()
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	return grpc.DialContext(ctx, addr,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithBlock(),
		grpc.WithDefaultCallOptions(grpc.ForceCodec(rpcjson.Codec{})),
	)
}

func defaultServerAddr() string {
	if value := os.Getenv("GITSLICE_GRPC_ADDR"); value != "" {
		return value
	}
	if value := os.Getenv("GITSLICE_SERVER_ADDR"); value != "" {
		return value
	}
	return "127.0.0.1:50051"
}

func parseFormat(args []string) string {
	for i := 0; i < len(args)-1; i++ {
		if args[i] == "--format" {
			return args[i+1]
		}
	}
	return "text"
}

func shouldSkip(rel string, entry fs.DirEntry) bool {
	first := rel
	if idx := strings.Index(first, "/"); idx >= 0 {
		first = first[:idx]
	}
	switch first {
	case ".git", ".gs", ".gitslice":
		return true
	default:
		return strings.HasPrefix(entry.Name(), ".")
	}
}

func readJSONFile(path string, v any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, v)
}

func writeJSONFile(path string, v any, mode fs.FileMode) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return os.WriteFile(path, data, mode)
}

func writeJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}
