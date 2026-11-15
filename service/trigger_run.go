package service

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"gitslice.io/gitslice/internal/authctx"
	gspaths "gitslice.io/gitslice/internal/paths"
	"gitslice.io/gitslice/internal/secretmask"
	"gitslice.io/gitslice/internal/storage"
	"gitslice.io/gitslice/internal/triggers"
	corev1 "gitslice.io/gitslice/proto/core/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Limits of what a running trigger may do (design/34_triggers.md).
const (
	maxTriggerModules     = 50
	maxTriggerModuleBytes = 1 << 20
	maxTriggerStateBytes  = 64 << 10
	maxTriggerLogLine     = 4 << 10
	maxTriggerLogLines    = 200
	maxTriggerSummary     = 64 << 10
)

// TriggerRunService is what a running trigger calls: the dispatcher, with
// its key, to start a run or a callback, and the run's script, with the run
// token, for everything else.
type TriggerRunService struct {
	Changesets storage.ChangesetStore
	Slices     storage.SliceStore
	Repository storage.RepositoryStore
	Checks     storage.CheckStore
	Triggers   storage.TriggerStore
	Tree       TreeFiles
	Engine     *triggers.Engine
}

func (s *TriggerRunService) ready() error {
	if s == nil || s.Triggers == nil || s.Engine == nil {
		return status.Error(codes.Unavailable, "triggers are not enabled on this server")
	}
	return nil
}

// runCall is an authenticated call of one run.
type runCall struct {
	run  *storage.TriggerRun
	spec triggers.RunSpec
	// ctx carries the run as the subject, so the events its writes cause can
	// be traced back to it.
	ctx context.Context
}

func (s *TriggerRunService) call(ctx context.Context, token string) (*runCall, error) {
	if err := s.ready(); err != nil {
		return nil, err
	}
	runID, err := s.Engine.ParseRunToken(token)
	if err != nil {
		return nil, status.Error(codes.Unauthenticated, err.Error())
	}
	run, err := s.Triggers.GetTriggerRun(ctx, runID)
	if err != nil {
		return nil, grpcError(err)
	}
	if storage.TriggerRunFinal(run.Status) {
		return nil, status.Errorf(codes.FailedPrecondition, "run %s has finished (%s)", run.ID, run.Status)
	}
	if run.Status == storage.TriggerRunRunning {
		// Any call is a sign of life.
		_ = s.Triggers.ExtendTriggerRunLease(ctx, run.ID, triggers.RunLease)
	}
	var spec triggers.RunSpec
	if err := json.Unmarshal(run.Spec, &spec); err != nil {
		return nil, status.Errorf(codes.Internal, "run %s has an unreadable spec", run.ID)
	}
	return &runCall{run: run, spec: spec, ctx: authctx.WithSubjectID(ctx, triggers.ActorPrefix+run.ID)}, nil
}

func (s *TriggerRunService) StartRun(ctx context.Context, req *corev1.StartTriggerRunRequest) (*corev1.StartTriggerRunResponse, error) {
	if err := s.ready(); err != nil {
		return nil, err
	}
	if !s.Engine.CheckDispatcherKey(req.GetDispatcherKey()) {
		return nil, status.Error(codes.Unauthenticated, "not the trigger dispatcher")
	}
	run, err := s.Triggers.ClaimTriggerRun(ctx, strings.TrimSpace(req.GetRunId()), triggers.RunLease)
	if errors.Is(err, storage.ErrConflict) {
		return nil, status.Errorf(codes.FailedPrecondition, "run %s is not queued", req.GetRunId())
	}
	if err != nil {
		return nil, grpcError(err)
	}
	resp, err := s.startResponse(ctx, run)
	if err != nil {
		// The run cannot start: say why on the run, and stop.
		reason := status.Convert(err).Message()
		if updated, uerr := s.Triggers.UpdateTriggerRun(ctx, run.ID, []string{storage.TriggerRunRunning}, storage.TriggerRunUpdate{Status: storage.TriggerRunErrored, Error: reason}); uerr == nil {
			s.Engine.CloseChecks(ctx, *updated, "errored", reason)
		}
		return nil, err
	}
	return resp, nil
}

func (s *TriggerRunService) StartCallback(ctx context.Context, req *corev1.StartTriggerRunRequest) (*corev1.StartTriggerRunResponse, error) {
	if err := s.ready(); err != nil {
		return nil, err
	}
	if !s.Engine.CheckDispatcherKey(req.GetDispatcherKey()) {
		return nil, status.Error(codes.Unauthenticated, "not the trigger dispatcher")
	}
	runID := strings.TrimSpace(req.GetRunId())
	if !hmac.Equal([]byte(req.GetCallbackSignature()), []byte(s.Engine.CallbackSignature(runID))) {
		return nil, status.Error(codes.PermissionDenied, "the callback URL is not genuine")
	}
	run, err := s.Triggers.GetTriggerRun(ctx, runID)
	if err != nil {
		return nil, grpcError(err)
	}
	if run.Status != storage.TriggerRunWaiting && run.Status != storage.TriggerRunRunning {
		return nil, status.Errorf(codes.FailedPrecondition, "run %s is not waiting for callbacks (%s)", run.ID, run.Status)
	}
	return s.startResponse(ctx, run)
}

// startResponse gathers what the dispatcher needs to call the script: the
// event, the modules, the secrets, the run token and the state.
func (s *TriggerRunService) startResponse(ctx context.Context, run *storage.TriggerRun) (*corev1.StartTriggerRunResponse, error) {
	var spec triggers.RunSpec
	if err := json.Unmarshal(run.Spec, &spec); err != nil || spec.Trigger.Action.Kind != triggers.ActionWorker {
		return nil, status.Errorf(codes.FailedPrecondition, "run %s has no script", run.ID)
	}
	tree, err := s.Repository.RootTreeForCommit(ctx, run.DefinitionCommitID)
	if err != nil {
		return nil, grpcError(err)
	}
	dotGitslice := run.Folder + "/.gitslice"
	modules, err := loadTriggerModules(ctx, s.Tree, tree, dotGitslice, spec.Trigger.Action.Worker)
	if err != nil {
		return nil, status.Error(codes.FailedPrecondition, err.Error())
	}
	secrets, missing, err := s.Engine.SecretsFor(ctx, run.Folder, spec.Trigger.Secrets)
	if err != nil {
		return nil, grpcError(err)
	}
	if len(missing) > 0 {
		return nil, status.Errorf(codes.FailedPrecondition, "no secret %s applies at %s", strings.Join(missing, ", "), run.Folder)
	}
	deadline := run.Deadline
	if deadline.IsZero() {
		deadline = time.Now().Add(spec.Trigger.Timeout)
	}
	hash := sha256.New()
	names := make([]string, 0, len(modules))
	for name := range modules {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		fmt.Fprintf(hash, "%s\x00%s\x00", name, modules[name])
	}
	fmt.Fprintf(hash, "network\x00%s", strings.Join(spec.Trigger.Network, ","))
	return &corev1.StartTriggerRunResponse{
		Run:            triggerRunProto(*run),
		EventJson:      string(run.Payload),
		Modules:        modules,
		MainModule:     spec.Trigger.Action.Worker,
		CodeHash:       hex.EncodeToString(hash.Sum(nil)),
		Secrets:        secrets,
		Reveal:         spec.Trigger.Reveal,
		Network:        spec.Trigger.Network,
		RunToken:       s.Engine.RunToken(run.ID, deadline.Add(time.Hour)),
		CallbackUrl:    s.Engine.CallbackURL(run.ID),
		StateJson:      string(run.State),
		DeadlineUnixMs: deadline.UnixMilli(),
	}, nil
}

var moduleImport = regexp.MustCompile(`(?:\bfrom\s*|\bimport\s*\(?\s*)["'](\.{1,2}/[^"']+)["']`)

// loadTriggerModules reads a script and the modules it imports by relative
// path, all inside the .gitslice folder, keyed by path relative to it.
func loadTriggerModules(ctx context.Context, tree triggers.TreeReader, rootTreeID, dotGitslice, entry string) (map[string]string, error) {
	modules := map[string]string{}
	total := 0
	queue := []string{entry}
	for len(queue) > 0 {
		rel := queue[0]
		queue = queue[1:]
		if _, done := modules[rel]; done {
			continue
		}
		if len(modules) >= maxTriggerModules {
			return nil, fmt.Errorf("the script imports more than %d modules", maxTriggerModules)
		}
		data, err := tree.ReadFile(ctx, rootTreeID, dotGitslice+"/"+rel)
		if errors.Is(err, triggers.ErrNotFound) {
			return nil, fmt.Errorf("script module %s/%s does not exist", dotGitslice, rel)
		}
		if err != nil {
			return nil, err
		}
		total += len(data)
		if total > maxTriggerModuleBytes {
			return nil, fmt.Errorf("the script's modules are larger than %d KiB", maxTriggerModuleBytes>>10)
		}
		if !utf8.Valid(data) {
			return nil, fmt.Errorf("script module %s is not UTF-8", rel)
		}
		modules[rel] = string(data)
		for _, m := range moduleImport.FindAllStringSubmatch(string(data), -1) {
			target := path.Clean(path.Join(path.Dir(rel), m[1]))
			if target == ".." || strings.HasPrefix(target, "../") {
				return nil, fmt.Errorf("%s imports %s, outside the .gitslice folder", rel, m[1])
			}
			if path.Ext(target) == "" {
				target += ".js"
			}
			queue = append(queue, target)
		}
	}
	return modules, nil
}

func (s *TriggerRunService) FinishRun(ctx context.Context, req *corev1.FinishTriggerRunRequest) (*corev1.TriggerRun, error) {
	call, err := s.call(ctx, req.GetRunToken())
	if err != nil {
		return nil, err
	}
	run := call.run
	from := []string{storage.TriggerRunRunning, storage.TriggerRunWaiting}
	update := storage.TriggerRunUpdate{}
	switch outcome := req.GetOutcome(); outcome {
	case "failed", "errored":
		// A script's error may quote a value it revealed; it is shown to
		// readers of the changeset, as logs are.
		message := req.GetError()
		if secrets, _, err := s.Engine.SecretsFor(ctx, run.Folder, call.spec.Trigger.Secrets); err == nil {
			message = secretmask.New(secretValuesOf(secrets)).Mask(message)
		}
		update.Status, update.Error = outcome, truncate(message, 2000)
	case "", "succeeded":
		open := len(s.Engine.OpenChecks(ctx, *run)) > 0
		switch {
		case req.GetFinish():
			update.Status = s.checksOutcome(ctx, *run)
		case req.GetWait() || open || (req.GetCallback() && run.Status == storage.TriggerRunWaiting):
			update.Status = storage.TriggerRunWaiting
			if run.Deadline.IsZero() {
				update.Deadline = time.Now().Add(call.spec.Trigger.Timeout)
			}
		default:
			update.Status = s.checksOutcome(ctx, *run)
		}
	default:
		return nil, status.Errorf(codes.InvalidArgument, "outcome %q (use succeeded, failed or errored)", outcome)
	}
	updated, err := s.Triggers.UpdateTriggerRun(ctx, run.ID, from, update)
	if errors.Is(err, storage.ErrConflict) {
		return nil, status.Errorf(codes.FailedPrecondition, "run %s has finished", run.ID)
	}
	if err != nil {
		return nil, grpcError(err)
	}
	if storage.TriggerRunFinal(updated.Status) {
		reason := "the trigger run ended without a result"
		if updated.Status != storage.TriggerRunSucceeded {
			reason = "the trigger run " + updated.Status + ": " + updated.Error
		}
		s.Engine.CloseChecks(call.ctx, *updated, "errored", truncate(reason, 500))
	}
	return triggerRunProto(*updated), nil
}

// checksOutcome is how a run ends that its script says is done: failed if a
// check it reported did not pass, else succeeded.
func (s *TriggerRunService) checksOutcome(ctx context.Context, run storage.TriggerRun) string {
	return s.Engine.ChecksOutcome(ctx, run)
}

func (s *TriggerRunService) AppendRunLog(ctx context.Context, req *corev1.AppendTriggerRunLogRequest) (*corev1.Empty, error) {
	call, err := s.call(ctx, req.GetRunToken())
	if err != nil {
		return nil, err
	}
	if len(req.GetLines()) > maxTriggerLogLines {
		return nil, status.Errorf(codes.InvalidArgument, "at most %d lines at a time", maxTriggerLogLines)
	}
	secrets, _, err := s.Engine.SecretsFor(ctx, call.run.Folder, call.spec.Trigger.Secrets)
	if err != nil {
		return nil, grpcError(err)
	}
	masker := secretmask.New(secretValuesOf(secrets))
	var b strings.Builder
	stream := "stdout"
	flush := func() error {
		if b.Len() == 0 {
			return nil
		}
		_, err := s.Triggers.AppendTriggerRunLog(ctx, call.run.ID, stream, b.String())
		b.Reset()
		return err
	}
	for _, line := range req.GetLines() {
		lineStream := "stdout"
		if line.GetStream() == "stderr" {
			lineStream = "stderr"
		}
		if lineStream != stream {
			if err := flush(); err != nil {
				return nil, grpcError(err)
			}
			stream = lineStream
		}
		b.WriteString(masker.Mask(truncate(strings.ToValidUTF8(line.GetText(), "?"), maxTriggerLogLine)))
		b.WriteString("\n")
	}
	if err := flush(); err != nil {
		return nil, grpcError(err)
	}
	return &corev1.Empty{}, nil
}

func secretValuesOf(secrets map[string]string) []string {
	out := make([]string, 0, len(secrets))
	for _, value := range secrets {
		out = append(out, value)
	}
	return out
}

func (s *TriggerRunService) GetRunState(ctx context.Context, req *corev1.TriggerRunTokenRequest) (*corev1.TriggerRunState, error) {
	call, err := s.call(ctx, req.GetRunToken())
	if err != nil {
		return nil, err
	}
	return &corev1.TriggerRunState{StateJson: string(call.run.State)}, nil
}

func (s *TriggerRunService) SetRunState(ctx context.Context, req *corev1.SetTriggerRunStateRequest) (*corev1.Empty, error) {
	call, err := s.call(ctx, req.GetRunToken())
	if err != nil {
		return nil, err
	}
	state := req.GetStateJson()
	if len(state) > maxTriggerStateBytes {
		return nil, status.Errorf(codes.InvalidArgument, "state is larger than %d KiB", maxTriggerStateBytes>>10)
	}
	if state != "" && !json.Valid([]byte(state)) {
		return nil, status.Error(codes.InvalidArgument, "state must be JSON")
	}
	if err := s.Triggers.SetTriggerRunState(ctx, call.run.ID, []byte(state)); err != nil {
		return nil, grpcError(err)
	}
	return &corev1.Empty{}, nil
}

var triggerCheckStatuses = map[string]bool{"queued": true, "running": true, "passed": true, "failed": true, "errored": true, "canceled": true, "skipped": true}

func terminalCheck(status string) bool {
	return status != "queued" && status != "running"
}

func (s *TriggerRunService) CreateCheck(ctx context.Context, req *corev1.CreateTriggerCheckRequest) (*corev1.CheckRun, error) {
	call, err := s.call(ctx, req.GetRunToken())
	if err != nil {
		return nil, err
	}
	run := call.run
	name := strings.TrimSpace(req.GetName())
	listed := false
	for _, check := range call.spec.Trigger.Checks {
		listed = listed || check == name
	}
	if !listed {
		return nil, status.Errorf(codes.PermissionDenied, "trigger %s does not list check %q in checks", run.TriggerID, name)
	}
	if run.ChangesetID == "" || run.PatchsetID == "" {
		return nil, status.Errorf(codes.FailedPrecondition, "a %s event has no changeset to report checks on", run.EventKind)
	}
	want := strings.TrimSpace(req.GetStatus())
	if want == "" {
		want = "queued"
	}
	if !triggerCheckStatuses[want] {
		return nil, status.Errorf(codes.InvalidArgument, "check status %q", want)
	}
	initial := want
	if terminalCheck(want) {
		// Only an update records a result for the submit gate.
		initial = "running"
	}
	check, err := s.Checks.CreateCheckRun(call.ctx, storage.CheckRunInput{
		ChangesetID: run.ChangesetID, PatchsetID: run.PatchsetID, CheckName: triggers.CheckID(run.TriggerID, name),
		Provenance: "trigger", Status: initial, TriggerRunID: run.ID,
	})
	if err != nil {
		return nil, grpcError(err)
	}
	summary, url := req.GetSummary(), req.GetDetailsUrl()
	return s.applyCheck(call, check, want, &summary, &url, req.GetAnnotations(), true)
}

func (s *TriggerRunService) UpdateCheck(ctx context.Context, req *corev1.UpdateTriggerCheckRequest) (*corev1.CheckRun, error) {
	call, err := s.call(ctx, req.GetRunToken())
	if err != nil {
		return nil, err
	}
	check, err := s.Checks.GetCheckRun(ctx, strings.TrimSpace(req.GetCheckRunId()))
	if err != nil || check.GetTriggerRunId() != call.run.ID {
		return nil, status.Errorf(codes.NotFound, "run %s reported no check %s", call.run.ID, req.GetCheckRunId())
	}
	want := strings.TrimSpace(req.GetStatus())
	if want != "" && !triggerCheckStatuses[want] {
		return nil, status.Errorf(codes.InvalidArgument, "check status %q", want)
	}
	var summary, url *string
	if req.Summary != nil {
		summary = req.Summary
	}
	if req.DetailsUrl != nil {
		url = req.DetailsUrl
	}
	return s.applyCheck(call, check, want, summary, url, req.GetAnnotations(), len(req.GetAnnotations()) > 0)
}

// applyCheck sets a check's details and status. Setting the status a check
// already has is a no-op, so repeated callbacks are harmless.
func (s *TriggerRunService) applyCheck(call *runCall, check *corev1.CheckRun, want string, summary, url *string, annotations []*corev1.CheckAnnotation, setAnnotations bool) (*corev1.CheckRun, error) {
	ctx := call.ctx
	if summary != nil || url != nil || setAnnotations {
		details := storage.CheckRunDetails{DetailsURL: check.GetDetailsUrl(), Summary: check.GetSummary()}
		for _, a := range check.GetAnnotations() {
			details.Annotations = append(details.Annotations, storage.CheckAnnotation{Path: a.Path, StartLine: int(a.StartLine), EndLine: int(a.EndLine), Level: a.Level, Message: a.Message})
		}
		if summary != nil {
			details.Summary = truncate(*summary, maxTriggerSummary)
		}
		if url != nil {
			u := strings.TrimSpace(*url)
			if u != "" && !strings.HasPrefix(u, "https://") && !strings.HasPrefix(u, "http://") {
				return nil, status.Error(codes.InvalidArgument, "details_url must be an http(s) URL")
			}
			details.DetailsURL = u
		}
		if setAnnotations {
			details.Annotations = nil
			for _, a := range annotations {
				p := strings.TrimSpace(a.GetPath())
				if p != "" && !strings.HasPrefix(p, "/") {
					p = call.run.Folder + "/" + p
				}
				level := a.GetLevel()
				if level != "notice" && level != "warning" {
					level = "failure"
				}
				details.Annotations = append(details.Annotations, storage.CheckAnnotation{
					Path: path.Clean(p), StartLine: int(a.GetStartLine()), EndLine: int(a.GetEndLine()), Level: level, Message: truncate(a.GetMessage(), 2000),
				})
			}
		}
		updated, err := s.Checks.SetCheckRunDetails(ctx, check.GetId(), details)
		if err != nil {
			return nil, grpcError(err)
		}
		check = updated
	}
	if want != "" && want != check.GetStatus() {
		if terminalCheck(check.GetStatus()) {
			return nil, status.Errorf(codes.FailedPrecondition, "check %s already %s", check.GetCheckName(), check.GetStatus())
		}
		exitCode := int32(-1)
		switch want {
		case "passed":
			exitCode = 0
		case "failed":
			exitCode = 1
		}
		updated, err := s.Checks.UpdateCheckRunStatus(ctx, check.GetId(), want, exitCode, check.GetSummary())
		if err != nil {
			return nil, grpcError(err)
		}
		check = updated
		// Details went first, but a terminal update writes the summary again.
		if check.GetDetailsUrl() == "" && url != nil {
			check.DetailsUrl = *url
		}
	}
	s.finishIfDone(ctx, call.run)
	return check, nil
}

// finishIfDone ends a waiting run once every check it reported has a result.
func (s *TriggerRunService) finishIfDone(ctx context.Context, run *storage.TriggerRun) {
	s.Engine.FinishIfDone(ctx, run.ID)
}

// eventTree is the tree a run's event is about: the patchset's result (or
// base, at "base"), else the commit, else the definition's commit.
func (s *TriggerRunService) eventTree(ctx context.Context, run *storage.TriggerRun, at string) (string, error) {
	if run.PatchsetID != "" {
		cs, err := s.Changesets.Get(ctx, run.ChangesetID)
		if err != nil {
			return "", err
		}
		for _, ps := range cs.GetPatchsets() {
			if ps.GetId() == run.PatchsetID {
				if at == "base" {
					return ps.GetBaseTreeId(), nil
				}
				return ps.GetResultTreeId(), nil
			}
		}
	}
	commit := run.CommitID
	if commit == "" || at == "base" {
		commit = run.DefinitionCommitID
	}
	return s.Repository.RootTreeForCommit(ctx, commit)
}

func (s *TriggerRunService) triggerPath(run *storage.TriggerRun, raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		raw = "."
	}
	if !strings.HasPrefix(raw, "/") {
		raw = run.Folder + "/" + raw
	}
	p, err := gspaths.CanonicalPrefix(raw)
	if err != nil {
		return "", status.Errorf(codes.InvalidArgument, "path: %v", err)
	}
	if !gspaths.Contains("/"+run.Account, p) {
		return "", status.Errorf(codes.PermissionDenied, "%s is outside account %s", p, run.Account)
	}
	return p, nil
}

func (s *TriggerRunService) ReadFile(ctx context.Context, req *corev1.TriggerReadFileRequest) (*corev1.TriggerReadFileResponse, error) {
	call, err := s.call(ctx, req.GetRunToken())
	if err != nil {
		return nil, err
	}
	p, err := s.triggerPath(call.run, req.GetPath())
	if err != nil {
		return nil, err
	}
	tree, err := s.eventTree(ctx, call.run, req.GetAt())
	if err != nil {
		return nil, grpcError(err)
	}
	data, err := s.Tree.ReadFile(ctx, tree, p)
	if errors.Is(err, triggers.ErrNotFound) {
		return &corev1.TriggerReadFileResponse{}, nil
	}
	if err != nil {
		return nil, grpcError(err)
	}
	return &corev1.TriggerReadFileResponse{Found: true, Content: data}, nil
}

func (s *TriggerRunService) ListDirectory(ctx context.Context, req *corev1.TriggerListDirectoryRequest) (*corev1.TriggerListDirectoryResponse, error) {
	call, err := s.call(ctx, req.GetRunToken())
	if err != nil {
		return nil, err
	}
	p, err := s.triggerPath(call.run, req.GetPath())
	if err != nil {
		return nil, err
	}
	tree, err := s.eventTree(ctx, call.run, req.GetAt())
	if err != nil {
		return nil, grpcError(err)
	}
	entries, err := s.Repository.ListDirectoryAtTree(ctx, tree, p)
	if errors.Is(err, storage.ErrNotFound) {
		return &corev1.TriggerListDirectoryResponse{}, nil
	}
	if err != nil {
		return nil, grpcError(err)
	}
	out := &corev1.TriggerListDirectoryResponse{Found: true}
	for _, entry := range entries {
		kind := "file"
		if entry.Kind == "tree" || entry.Kind == "dir" || entry.Kind == "directory" {
			kind = "directory"
		}
		full := entry.Path
		if full == "" {
			full = p + "/" + entry.Name
		}
		out.Entries = append(out.Entries, &corev1.TriggerDirectoryEntry{Name: entry.Name, Path: full, Kind: kind})
	}
	return out, nil
}

func (s *TriggerRunService) GetChangeset(ctx context.Context, req *corev1.TriggerRunTokenRequest) (*corev1.Changeset, error) {
	call, err := s.call(ctx, req.GetRunToken())
	if err != nil {
		return nil, err
	}
	if call.run.ChangesetID == "" {
		return nil, status.Errorf(codes.FailedPrecondition, "a %s event is not about a changeset", call.run.EventKind)
	}
	cs, err := s.Changesets.Get(ctx, call.run.ChangesetID)
	if err != nil {
		return nil, grpcError(err)
	}
	storage.PopulateChangesetHandles(cs)
	return cs, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	cut := n
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "…"
}
