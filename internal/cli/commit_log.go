package cli

import (
	"context"
	"fmt"
	"io"
	"strings"

	"gitslice.io/gitslice/internal/postgres"
	"gitslice.io/gitslice/internal/storage"
	corev1 "gitslice.io/gitslice/proto/core/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	grpcstatus "google.golang.org/grpc/status"
)

type commitLogOutput struct {
	Scope         *commitLogScopeOutput `json:"scope,omitempty"`
	Commits       []commitOutput        `json:"commits"`
	NextPageToken string                `json:"next_page_token,omitempty"`
}

type commitLogScopeOutput struct {
	RefName     string           `json:"ref_name"`
	Slice       *corev1.SliceRef `json:"slice,omitempty"`
	Path        string           `json:"path,omitempty"`
	FollowMoves bool             `json:"follow_moves"`
	All         bool             `json:"all,omitempty"`
}

type commitOutput struct {
	ID           string                 `json:"id"`
	ShortID      string                 `json:"short_id"`
	ParentIDs    []string               `json:"parent_ids,omitempty"`
	RootTreeID   string                 `json:"root_tree_id,omitempty"`
	Author       string                 `json:"author,omitempty"`
	CreatedAt    string                 `json:"created_at,omitempty"`
	Message      string                 `json:"message"`
	ChangedPaths []string               `json:"changed_paths,omitempty"`
	GitImport    *commitGitImportOutput `json:"git_import,omitempty"`
	// The changeset that landed the commit, and its web page. A commit id is
	// not a changeset id: commits have no page of their own.
	Changeset    string `json:"changeset,omitempty"`
	ChangesetURL string `json:"changeset_url,omitempty"`
}

// commitGitImportOutput describes the original Git commit behind an imported
// native commit.
type commitGitImportOutput struct {
	GitCommitID string `json:"git_commit_id"`
	AuthorName  string `json:"author_name,omitempty"`
	AuthorEmail string `json:"author_email,omitempty"`
	AuthoredAt  string `json:"authored_at,omitempty"`
	Message     string `json:"message,omitempty"`
}

type commitLogOptions struct {
	Limit         int
	Path          string
	Slice         string
	PageToken     string
	NoFollowMoves bool
	Follow        bool
	All           bool
	Full          bool
	Oneline       bool
	NameOnly      bool
	Stat          bool
	Patch         bool
	IncludeScope  bool
}

type commitShowOptions struct {
	Full     bool
	NameOnly bool
	Stat     bool
	Patch    bool
}

func (r Runner) runCommitLog(ctx context.Context, opts commandOptions, logOpts commitLogOptions) error {
	if logOpts.Patch || logOpts.Stat {
		return userError("unsupported_option", "commit log patch/stat output is not supported yet", "Use gs show --name-only <commit> or gs diff <commit>.")
	}
	if logOpts.Oneline && opts.Format == "medium" {
		return userError("invalid_args", "--oneline cannot be combined with --format medium", "Use either gs log --oneline or gs log --format medium.")
	}
	if logOpts.All && strings.TrimSpace(logOpts.Slice) != "" {
		return userError("invalid_args", "--all cannot be combined with --slice", "Use --slice for one slice or --all for broader readable history.")
	}
	cfg, err := r.readUserConfig()
	if err != nil {
		return err
	}
	conn, err := dial(ctx, cfg.ServerAddr)
	if err != nil {
		return err
	}
	defer conn.Close()
	callCtx := authContext(ctx, cfg)
	pathFilter, err := r.resolveCommitLogPath(logOpts.Path)
	if err != nil {
		return err
	}
	if logOpts.Follow && strings.TrimSpace(pathFilter) == "" {
		return userError("invalid_args", "--follow requires exactly one path", "Pass a path with gs log --follow -- <path>.")
	}
	scopeSlice, scopeAll, err := r.resolveCommitLogScope(callCtx, cfg, conn, logOpts.Slice, logOpts.All)
	if err != nil {
		return err
	}
	req := &corev1.ListCommitsRequest{
		RefName:   postgres.DefaultTargetRef,
		Limit:     int32(logOpts.Limit),
		Path:      pathFilter,
		Slice:     scopeSlice,
		PageToken: logOpts.PageToken,
	}
	if logOpts.NoFollowMoves {
		followMoves := false
		req.FollowMoves = &followMoves
	}
	res, err := corev1.NewRepositoryServiceClient(conn).ListCommits(callCtx, req)
	if err != nil {
		return err
	}
	output := commitLogOutput{
		Commits:       commitsToOutput(res.Commits),
		NextPageToken: res.NextPageToken,
	}
	if logOpts.IncludeScope {
		output.Scope = &commitLogScopeOutput{
			RefName:     req.RefName,
			Slice:       scopeSlice,
			Path:        pathFilter,
			FollowMoves: req.FollowMoves == nil || req.GetFollowMoves(),
			All:         scopeAll,
		}
	}
	if opts.jsonOutput() {
		return r.writeJSONOutput(opts, output)
	}
	if opts.Quiet {
		return nil
	}
	color := r.colorEnabled(opts)
	if opts.Format == "medium" {
		printCommitLogMedium(r.Stdout, res.Commits, logOpts.Full, logOpts.NameOnly, color)
	} else {
		printCommitLogOneline(r.Stdout, res.Commits, logOpts.Full, logOpts.NameOnly, color)
	}
	if res.NextPageToken != "" {
		fmt.Fprintf(r.Stdout, "%s %s\n", colorize(color, ansiDim, "next_page_token:"), res.NextPageToken)
		fmt.Fprintf(r.Stdout, "%s Run gs log --page-token %s with the same filters.\n", colorize(color, ansiDim, "hint:"), res.NextPageToken)
	}
	return nil
}

func (r Runner) runCommitShow(ctx context.Context, opts commandOptions, commitID string, showOpts commitShowOptions) error {
	cfg, err := r.readUserConfig()
	if err != nil {
		return err
	}
	conn, err := dial(ctx, cfg.ServerAddr)
	if err != nil {
		return err
	}
	defer conn.Close()
	commit, err := r.resolveCommit(ctx, cfg, conn, commitID, nil)
	if err != nil {
		return err
	}
	out := commitToOutput(commit)
	if opts.jsonOutput() {
		return r.writeJSONOutput(opts, out)
	}
	if opts.Quiet {
		return nil
	}
	color := r.colorEnabled(opts)
	if showOpts.NameOnly {
		printPathListColor(r.Stdout, commit.ChangedPaths, color)
		return nil
	}
	if showOpts.Stat {
		printPathStatColor(r.Stdout, commit.ChangedPaths, color)
		return nil
	}
	if showOpts.Patch {
		diffText, err := r.commitDiffText(ctx, cfg, conn, "", commit.Id)
		if err != nil {
			return err
		}
		_, err = io.WriteString(r.Stdout, colorUnifiedDiff(compactUnifiedDiff(diffText, opts.diffContext()), color))
		return err
	}
	printCommitDetails(r.Stdout, commit, showOpts.Full, color)
	return nil
}

func (r Runner) resolveCommitLogPath(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", nil
	}
	if strings.HasPrefix(raw, "/") {
		return cleanShellGlobalPath(raw)
	}
	ws, err := r.readWorkspaceConfig()
	if err != nil {
		if isUserErrorCode(err, "not_in_workspace") {
			return "", userError("invalid_args", "relative log paths require a workspace", "Pass an absolute account-rooted path such as /account/project or run gs log from inside a workspace.")
		}
		return "", err
	}
	return workspaceInputToGlobalPath(ws, raw)
}

func (r Runner) resolveCommitLogScope(ctx context.Context, cfg UserConfig, conn *grpc.ClientConn, sliceRef string, all bool) (*corev1.SliceRef, bool, error) {
	if all {
		return nil, true, nil
	}
	if strings.TrimSpace(sliceRef) != "" {
		ref, err := r.resolveSliceRefInput(ctx, cfg, conn, sliceRef)
		return ref, false, err
	}
	if ws, err := r.readWorkspaceConfig(); err == nil {
		return &corev1.SliceRef{Account: ws.Account, Slice: ws.Slice}, false, nil
	} else if !isUserErrorCode(err, "not_in_workspace") {
		return nil, false, err
	}
	slice, err := r.personalHomeSlice(ctx, cfg, conn)
	if err != nil {
		return nil, false, err
	}
	return slice.Ref, false, nil
}

type commitResolveScope struct {
	Path        string
	Slice       *corev1.SliceRef
	FollowMoves *bool
}

func (r Runner) resolveCommit(ctx context.Context, cfg UserConfig, conn *grpc.ClientConn, input string, scope *commitResolveScope) (*corev1.Commit, error) {
	req := &corev1.ResolveCommitRequest{
		CommitId: input,
		RefName:  postgres.DefaultTargetRef,
	}
	if scope != nil {
		req.Path = scope.Path
		req.Slice = scope.Slice
		req.FollowMoves = scope.FollowMoves
	}
	res, err := corev1.NewRepositoryServiceClient(conn).ResolveCommit(authContext(ctx, cfg), req)
	if err != nil {
		if grpcstatus.Code(err) == codes.InvalidArgument {
			return nil, userError("invalid_commit_id", grpcstatus.Convert(err).Message(), "Pass a full commit id, sha256:<prefix>, or at least 8 hex characters.")
		}
		if grpcstatus.Code(err) == codes.FailedPrecondition && strings.Contains(err.Error(), "COMMIT_PREFIX_AMBIGUOUS") {
			return nil, userError("ambiguous_commit", grpcstatus.Convert(err).Message(), "Use a longer commit id prefix.")
		}
		return nil, err
	}
	return res.Commit, nil
}

func commitsToOutput(commits []*corev1.Commit) []commitOutput {
	out := make([]commitOutput, 0, len(commits))
	for _, commit := range commits {
		out = append(out, commitToOutput(commit))
	}
	return out
}

func commitToOutput(commit *corev1.Commit) commitOutput {
	if commit == nil {
		return commitOutput{}
	}
	out := commitOutput{
		ID:           commit.Id,
		ShortID:      shortID(commit.Id),
		ParentIDs:    append([]string(nil), commit.ParentIds...),
		RootTreeID:   commit.RootTreeId,
		Author:       commit.Author,
		CreatedAt:    commit.CreatedAt,
		Message:      commit.Message,
		ChangedPaths: append([]string(nil), commit.ChangedPaths...),
	}
	if commit.ChangesetId != "" {
		out.Changeset = storage.ShortChangesetID(commit.ChangesetId)
		out.ChangesetURL = webResourceURL("/cs/" + out.Changeset)
	}
	if imported := commit.GetGitImport(); imported != nil {
		out.GitImport = &commitGitImportOutput{
			GitCommitID: imported.GitCommitId,
			AuthorName:  imported.AuthorName,
			AuthorEmail: imported.AuthorEmail,
			AuthoredAt:  imported.AuthoredAt,
			Message:     imported.Message,
		}
	}
	return out
}

func printCommitLogOneline(w io.Writer, commits []*corev1.Commit, full, nameOnly, color bool) {
	for _, commit := range commits {
		id := displayCommitID(commit.Id, full)
		// Name the changeset next to the commit: its id is the one with a
		// web page.
		landed := ""
		if commit.ChangesetId != "" {
			landed = colorize(color, ansiDim, "  (changeset "+storage.ShortChangesetID(commit.ChangesetId)+")")
		}
		fmt.Fprintf(w, "%s  %s%s\n", colorize(color, ansiYellow, id), commit.Message, landed)
		if nameOnly {
			printIndentedPathsColor(w, commit.ChangedPaths, "  ", color)
		}
	}
}

func printCommitLogMedium(w io.Writer, commits []*corev1.Commit, full, nameOnly, color bool) {
	for i, commit := range commits {
		if i > 0 {
			fmt.Fprintln(w)
		}
		printCommitDetails(w, commit, full, color)
		if nameOnly && len(commit.ChangedPaths) > 0 {
			fmt.Fprintln(w)
			printIndentedPathsColor(w, commit.ChangedPaths, "  ", color)
		}
	}
}

func printCommitDetails(w io.Writer, commit *corev1.Commit, full, color bool) {
	id := displayCommitID(commit.Id, full)
	fmt.Fprintf(w, "%s %s\n", colorize(color, ansiDim, "commit"), colorize(color, ansiYellow, id))
	if len(commit.ParentIds) > 0 {
		parents := make([]string, 0, len(commit.ParentIds))
		for _, parent := range commit.ParentIds {
			parents = append(parents, displayCommitID(parent, full))
		}
		fmt.Fprintf(w, "%s %s\n", colorize(color, ansiDim, "Parents:"), strings.Join(parents, " "))
	}
	if commit.RootTreeId != "" {
		fmt.Fprintf(w, "%s %s\n", colorize(color, ansiDim, "Root:"), displayCommitID(commit.RootTreeId, full))
	}
	if commit.ChangesetId != "" {
		handle := storage.ShortChangesetID(commit.ChangesetId)
		fmt.Fprintf(w, "%s %s %s\n", colorize(color, ansiDim, "Changeset:"), handle, webResourceURL("/cs/"+handle))
	}
	if commit.Author != "" {
		fmt.Fprintf(w, "%s %s\n", colorize(color, ansiDim, "Author:"), commit.Author)
	}
	if commit.CreatedAt != "" {
		fmt.Fprintf(w, "%s   %s\n", colorize(color, ansiDim, "Date:"), commit.CreatedAt)
	}
	message := commit.Message
	if imported := commit.GetGitImport(); imported != nil {
		origin := "git " + displayCommitID(imported.GitCommitId, full)
		if imported.AuthorName != "" {
			origin += fmt.Sprintf(" by %s <%s>", imported.AuthorName, imported.AuthorEmail)
		}
		if imported.AuthoredAt != "" {
			origin += " on " + imported.AuthoredAt
		}
		fmt.Fprintf(w, "%s %s\n", colorize(color, ansiDim, "Imported:"), origin)
		if strings.TrimSpace(imported.Message) != "" {
			message = imported.Message
		}
	}
	if strings.TrimSpace(message) != "" {
		fmt.Fprintln(w)
		for _, line := range strings.Split(message, "\n") {
			fmt.Fprintf(w, "    %s\n", line)
		}
	}
	if len(commit.ChangedPaths) > 0 {
		fmt.Fprintln(w)
		fmt.Fprintln(w, colorize(color, ansiDim, "Changed paths:"))
		printIndentedPathsColor(w, commit.ChangedPaths, "  ", color)
	}
}

func displayCommitID(id string, full bool) string {
	if full {
		return id
	}
	return shortID(id)
}
