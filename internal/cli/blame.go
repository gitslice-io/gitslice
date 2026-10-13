package cli

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
	"gitslice.io/gitslice/internal/storage"
	corev1 "gitslice.io/gitslice/proto/core/v1"
)

// gs blame shows which commit, and through it which changeset, last changed
// each line of a file (design/27_blame.md).

type blameRangeOutput struct {
	StartLine int32  `json:"start_line"`
	LineCount int32  `json:"line_count"`
	CommitID  string `json:"commit_id"`
}

type blameCommitOutput struct {
	ID           string `json:"id"`
	Author       string `json:"author,omitempty"`
	CreatedAt    string `json:"created_at,omitempty"`
	Summary      string `json:"summary,omitempty"`
	ChangesetID  string `json:"changeset_id,omitempty"`
	ChangesetURL string `json:"changeset_url,omitempty"`
}

type blameOutput struct {
	CommitID  string              `json:"commit_id"`
	Path      string              `json:"path"`
	LineCount int32               `json:"line_count"`
	Truncated bool                `json:"truncated,omitempty"`
	Ranges    []blameRangeOutput  `json:"ranges"`
	Commits   []blameCommitOutput `json:"commits"`
}

func (r Runner) blameCommand(opts *commandOptions) *cobra.Command {
	var slice, lines string
	cmd := &cobra.Command{
		Use:   "blame <path>",
		Short: "Show which commit and changeset last changed each line of a file",
		Long: "Show which commit, and the changeset that landed it, last changed each line of a file at the\n" +
			"latest commit, following the file across moves. Paths are workspace-relative inside a workspace,\n" +
			"or account-rooted (/account/...). --json gives line ranges and the commits they name.",
		Args: exactArgs(1, "gs blame <path> [--lines start,end] [--slice account/slice]"),
		RunE: func(cmd *cobra.Command, args []string) error {
			return r.runBlame(cmd.Context(), *opts, args[0], slice, lines)
		},
	}
	cmd.Flags().StringVar(&slice, "slice", "", "read through this slice (default: the workspace's)")
	cmd.Flags().StringVarP(&lines, "lines", "L", "", "only these lines, such as 10,20 (text output)")
	return cmd
}

func (r Runner) runBlame(ctx context.Context, opts commandOptions, rawPath, sliceValue, lineRange string) error {
	first, last, err := parseBlameLines(lineRange)
	if err != nil {
		return err
	}
	p, err := r.resolveCommitLogPath(rawPath)
	if err != nil {
		return err
	}
	cfg, conn, callCtx, err := r.authenticatedConn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	var sliceRef *corev1.SliceRef
	if strings.TrimSpace(sliceValue) != "" {
		if sliceRef, err = r.resolveSliceRefInput(callCtx, cfg, conn, sliceValue); err != nil {
			return err
		}
	} else if ws, err := r.readWorkspaceConfig(); err == nil {
		sliceRef = &corev1.SliceRef{Account: ws.Account, Slice: ws.Slice}
	}
	repo := corev1.NewRepositoryServiceClient(conn)
	res, err := repo.BlameFile(callCtx, &corev1.BlameFileRequest{Path: p, Slice: sliceRef})
	if err != nil {
		return err
	}
	out := blameOutput{CommitID: res.GetCommitId(), Path: res.GetPath(), LineCount: res.GetLineCount(), Truncated: res.GetTruncated(), Ranges: []blameRangeOutput{}, Commits: []blameCommitOutput{}}
	commits := map[string]blameCommitOutput{}
	for _, c := range res.GetCommits() {
		summary, _, _ := strings.Cut(c.GetMessage(), "\n")
		commit := blameCommitOutput{ID: c.GetId(), Author: c.GetAuthor(), CreatedAt: c.GetCreatedAt(), Summary: summary, ChangesetID: c.GetChangesetId()}
		if handle := storage.ShortChangesetID(c.GetChangesetId()); handle != "" {
			commit.ChangesetURL = webResourceURL("/cs/" + handle)
		}
		commits[commit.ID] = commit
		out.Commits = append(out.Commits, commit)
	}
	for _, rg := range res.GetRanges() {
		out.Ranges = append(out.Ranges, blameRangeOutput{StartLine: rg.GetStartLine(), LineCount: rg.GetLineCount(), CommitID: rg.GetCommitId()})
	}
	if opts.jsonOutput() {
		return r.writeJSONOutput(opts, out)
	}
	if opts.Quiet {
		return nil
	}

	// The text view prints the lines themselves, read at the same commit.
	file, err := repo.ReadFile(callCtx, &corev1.ReadFileRequest{CommitId: out.CommitID, Path: out.Path, Slice: sliceRef})
	if err != nil {
		return err
	}
	text := strings.SplitAfter(string(file.GetData()), "\n")
	width := len(strconv.Itoa(int(out.LineCount)))
	authorWidth := 0
	for _, c := range commits {
		authorWidth = max(authorWidth, len(c.Author))
	}
	for _, rg := range out.Ranges {
		c := commits[rg.CommitID]
		handle := storage.ShortChangesetID(c.ChangesetID)
		if handle == "" {
			handle = "-"
		}
		date := c.CreatedAt
		if len(date) >= 10 {
			date = date[:10]
		}
		for line := rg.StartLine; line < rg.StartLine+rg.LineCount; line++ {
			if (first > 0 && int(line) < first) || (last > 0 && int(line) > last) {
				continue
			}
			content := ""
			if int(line)-1 < len(text) {
				content = strings.TrimRight(text[line-1], "\n")
			}
			fmt.Fprintf(r.Stdout, "%s %-10s (%-*s %s %*d) %s\n", shortID(rg.CommitID), handle, authorWidth, c.Author, date, width, line, content)
		}
	}
	if out.Truncated && len(out.Ranges) > 0 {
		fmt.Fprintf(r.stderr(), "note: history was read back to a limit; lines credited to the oldest commit shown may be older\n")
	}
	return nil
}

func parseBlameLines(value string) (int, int, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, 0, nil
	}
	start, end, ok := strings.Cut(value, ",")
	first, err1 := strconv.Atoi(strings.TrimSpace(start))
	last := first
	var err2 error
	if ok {
		last, err2 = strconv.Atoi(strings.TrimSpace(end))
	}
	if err1 != nil || err2 != nil || first < 1 || last < first {
		return 0, 0, userError("invalid_args", "--lines takes start,end such as 10,20", "")
	}
	return first, last, nil
}
