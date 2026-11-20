package service

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"time"

	"gitslice.io/gitslice/internal/authctx"
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
	run, err := s.Triggers.UpdateTriggerRun(ctx, runID, []string{storage.TriggerRunWaiting}, storage.TriggerRunUpdate{
		Status: storage.TriggerRunRunning,
	})
	if errors.Is(err, storage.ErrConflict) {
		current, gerr := s.Triggers.GetTriggerRun(ctx, runID)
		if gerr != nil {
			return nil, grpcError(gerr)
		}
		return nil, status.Errorf(codes.FailedPrecondition, "run %s is not waiting for callbacks (%s)", current.ID, current.Status)
	}
	if err != nil {
		return nil, grpcError(err)
	}
	_ = s.Triggers.ExtendTriggerRunLease(ctx, run.ID, triggers.RunLease)
	resp, err := s.startResponse(ctx, run)
	if err != nil {
		_, _ = s.Triggers.UpdateTriggerRun(ctx, run.ID, []string{storage.TriggerRunRunning}, storage.TriggerRunUpdate{
			Status:   storage.TriggerRunWaiting,
			Deadline: run.Deadline,
		})
		return nil, err
	}
	return resp, nil
}

// startResponse gathers what the dispatcher needs to execute a worker or bash
// automation: the event, modules or bash script, secrets, run token and state.
func (s *TriggerRunService) startResponse(ctx context.Context, run *storage.TriggerRun) (*corev1.StartTriggerRunResponse, error) {
	var spec triggers.RunSpec
	if err := json.Unmarshal(run.Spec, &spec); err != nil {
		return nil, status.Errorf(codes.FailedPrecondition, "run %s has an unreadable spec", run.ID)
	}
	kind := spec.Trigger.Action.Kind
	if kind != triggers.ActionWorker && kind != triggers.ActionBash {
		return nil, status.Errorf(codes.FailedPrecondition, "run %s has no worker or bash action", run.ID)
	}
	tree := spec.DefinitionTree
	if tree == "" {
		var err error
		if tree, err = s.Repository.RootTreeForCommit(ctx, run.DefinitionCommitID); err != nil {
			return nil, grpcError(err)
		}
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
	resp := &corev1.StartTriggerRunResponse{
		Run:            triggerRunProto(*run),
		Action:         kind,
		EventJson:      string(run.Payload),
		Secrets:        secrets,
		Reveal:         spec.Trigger.Reveal,
		Network:        spec.Trigger.Network,
		Egress:         spec.Trigger.Egress,
		RunToken:       s.Engine.RunToken(run.ID, deadline.Add(time.Hour)),
		CallbackUrl:    s.Engine.CallbackURL(run.ID),
		StateJson:      string(run.State),
		DeadlineUnixMs: deadline.UnixMilli(),
		Inputs:         spec.Inputs,
		TimeoutMs:      spec.Trigger.Timeout.Milliseconds(),
	}
	dotGitslice := run.Folder + "/.gitslice"
	hash := sha256.New()
	fmt.Fprintf(hash, "account\x00%s\x00folder\x00%s\x00action\x00%s\x00", run.Account, run.Folder, kind)
	if kind == triggers.ActionWorker {
		modules, err := loadTriggerModules(ctx, s.Tree, tree, dotGitslice, spec.Trigger.Action.Worker)
		if err != nil {
			return nil, status.Error(codes.FailedPrecondition, err.Error())
		}
		names := make([]string, 0, len(modules))
		for name := range modules {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			fmt.Fprintf(hash, "%s\x00%s\x00", name, modules[name])
		}
		fmt.Fprintf(hash, "network\x00%s", strings.Join(spec.Trigger.Network, ","))
		resp.Modules = modules
		resp.MainModule = spec.Trigger.Action.Worker
		resp.CodeHash = hex.EncodeToString(hash.Sum(nil))
		return resp, nil
	}

	bash := spec.Trigger.Action.Bash
	if bash == nil {
		return nil, status.Errorf(codes.FailedPrecondition, "run %s has no bash action", run.ID)
	}
	scriptBody := bash.Run
	if bash.Script != "" {
		rawScript, err := s.Tree.ReadFile(ctx, tree, dotGitslice+"/"+bash.Script)
		if errors.Is(err, triggers.ErrNotFound) {
			return nil, status.Errorf(codes.FailedPrecondition, "bash script %s/%s not found", dotGitslice, bash.Script)
		}
		if err != nil {
			return nil, status.Error(codes.FailedPrecondition, err.Error())
		}
		if len(rawScript) > triggers.MaxBashRunBytes {
			return nil, status.Errorf(codes.FailedPrecondition, "bash script %s exceeds %d KiB", bash.Script, triggers.MaxBashRunBytes>>10)
		}
		scriptBody = string(rawScript)
	}
	fmt.Fprintf(hash, "runtime\x00%s\x00egress\x00%s\x00network\x00%s\x00script\x00%s", bash.Runtime, spec.Trigger.Egress, strings.Join(spec.Trigger.Network, ","), scriptBody)
	resp.CodeHash = hex.EncodeToString(hash.Sum(nil))
	resp.BashScript = scriptBody
	resp.BashRuntime = bash.Runtime
	resp.BashSize = bash.Size
	resp.BashCheckout = bash.Checkout
	resp.BashInclude = bash.Include
	resp.BashWorkingDir = bash.WorkingDir
	resp.BashEnv = bash.Env
	return resp, nil
}

// loadTriggerModules reads a script and the modules it imports by relative
// path, all inside the .gitslice folder, keyed by path relative to it.
func loadTriggerModules(ctx context.Context, tree triggers.TreeReader, rootTreeID, dotGitslice, entry string) (map[string]string, error) {
	return triggers.LoadModules(ctx, tree, rootTreeID, dotGitslice, entry, maxTriggerModules, maxTriggerModuleBytes)
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
		case req.GetWait() || open || (req.GetCallback() && (run.Status == storage.TriggerRunWaiting || !run.Deadline.IsZero())):
			update.Status = storage.TriggerRunWaiting
			if run.Deadline.IsZero() {
				update.Deadline = time.Now().Add(call.spec.Trigger.Timeout)
			} else {
				update.Deadline = run.Deadline
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

// SetRunDetailsUrl links the run to an outside page (gs.setDetailsUrl).
func (s *TriggerRunService) SetRunDetailsUrl(ctx context.Context, req *corev1.SetTriggerRunDetailsUrlRequest) (*corev1.Empty, error) {
	call, err := s.call(ctx, req.GetRunToken())
	if err != nil {
		return nil, err
	}
	raw := strings.TrimSpace(req.GetDetailsUrl())
	if len(raw) > maxTriggerDetailsURLBytes {
		return nil, status.Errorf(codes.InvalidArgument, "details URL is longer than %d bytes", maxTriggerDetailsURLBytes)
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil {
		return nil, status.Error(codes.InvalidArgument, "details URL must be an https URL without credentials")
	}
	if err := s.Triggers.SetTriggerRunDetailsURL(ctx, call.run.ID, u.String()); err != nil {
		return nil, grpcError(err)
	}
	return &corev1.Empty{}, nil
}

const maxTriggerDetailsURLBytes = 2048

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
