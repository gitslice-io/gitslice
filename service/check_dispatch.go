package service

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"path"
	"strings"

	"github.com/gitslice-io/gitslice/internal/checks"
	"github.com/gitslice-io/gitslice/internal/objectstore/filesystem"
	"github.com/gitslice-io/gitslice/internal/storage"
	corev1 "github.com/gitslice-io/gitslice/proto/core/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type checkTreeReader struct {
	repository storage.RepositoryStore
	objects    ObjectStore
	account    string
}

func (r checkTreeReader) ReadFile(ctx context.Context, rootTreeID, logicalPath string) ([]byte, error) {
	if r.repository == nil || r.objects == nil {
		return nil, fmt.Errorf("check tree reader is not configured")
	}
	entry, err := r.repository.GetFileAtTree(ctx, rootTreeID, r.storagePath(logicalPath))
	if errors.Is(err, storage.ErrNotFound) {
		return nil, checks.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	rc, err := r.objects.Get(ctx, filesystem.BlobKey(entry.ContentHash), 0, 0)
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	data, err := io.ReadAll(rc)
	if err != nil {
		return nil, err
	}
	return data, nil
}

func (r checkTreeReader) storagePath(logicalPath string) string {
	cleaned := path.Clean("/" + strings.TrimSpace(logicalPath))
	if cleaned == "/" {
		return "/" + r.account
	}
	return "/" + path.Join(r.account, strings.TrimPrefix(cleaned, "/"))
}

type checkDispatcher struct {
	Checks      storage.CheckStore
	Changesets  storage.ChangesetStore
	Repository  storage.RepositoryStore
	Slices      storage.SliceStore
	ObjectStore ObjectStore

	hub        *agentHub
	serverAddr string
}

func (d *checkDispatcher) resolveCheckPlan(ctx context.Context, slice *corev1.Slice, patchset *corev1.Patchset) (*checks.Plan, error) {
	if d == nil {
		return nil, fmt.Errorf("check dispatcher is not configured")
	}
	if slice == nil || slice.Ref == nil || slice.Definition == nil {
		return nil, fmt.Errorf("slice definition is required")
	}
	if patchset == nil {
		return nil, fmt.Errorf("patchset is required")
	}
	if strings.TrimSpace(patchset.ResultTreeId) == "" {
		return nil, fmt.Errorf("patchset result tree is required")
	}
	reader := checkTreeReader{
		repository: d.Repository,
		objects:    d.ObjectStore,
		account:    slice.Ref.Account,
	}
	return checks.ResolvePlan(
		ctx,
		reader,
		patchset.ResultTreeId,
		logicalPathsForAccount(slice.Ref.Account, patchset.ChangedPaths),
		logicalPathsForAccount(slice.Ref.Account, slice.Definition.IncludedPaths),
		slice.Definition.RequiredChecks,
	)
}

func (d *checkDispatcher) dispatchOutOfSliceChecks(ctx context.Context, cs *corev1.Changeset, slice *corev1.Slice, patchset *corev1.Patchset) {
	if d == nil || d.Checks == nil || d.hub == nil || cs == nil || slice == nil || patchset == nil {
		return
	}
	plan, err := d.resolveCheckPlan(ctx, slice, patchset)
	if err != nil {
		slog.Warn("failed to resolve check plan for dispatch", "changeset_id", cs.GetId(), "patchset_id", patchset.GetId(), "error", err)
		return
	}
	var runnable []checks.CheckSpec
	for _, spec := range plan.Runnable {
		if spec.OutOfSlice {
			runnable = append(runnable, spec)
		}
	}
	if len(runnable) == 0 {
		return
	}

	daemonID := strings.TrimSpace(slice.CiDaemonId)
	runSpecs := make([]*corev1.CheckRunSpec, 0, len(runnable))
	for _, spec := range runnable {
		run, err := d.Checks.CreateCheckRun(ctx, storage.CheckRunInput{
			ChangesetID: cs.Id,
			PatchsetID:  patchset.Id,
			CheckName:   spec.Name,
			DaemonID:    daemonID,
			Provenance:  "ci",
			Status:      "queued",
		})
		if err != nil {
			slog.Warn("failed to create ci check run", "changeset_id", cs.Id, "patchset_id", patchset.Id, "check", spec.Name, "error", err)
			continue
		}
		runSpecs = append(runSpecs, checkRunSpecFromPlan(run.Id, spec))
	}
	if len(runSpecs) == 0 || daemonID == "" {
		return
	}
	conn, ok := d.hub.daemon(daemonID)
	if !ok {
		return
	}
	conn.trySend(&corev1.ServerMessage{Payload: &corev1.ServerMessage_RunChecks{RunChecks: &corev1.RunChecks{
		ChangesetId:  cs.Id,
		PatchsetId:   patchset.Id,
		ResultTreeId: patchset.ResultTreeId,
		Slice:        slice.Ref,
		SliceId:      slice.Id,
		ServerAddr:   d.serverAddr,
		Checks:       runSpecs,
	}}})
}

func (s *ChangesetService) dispatchOutOfSliceChecks(ctx context.Context, cs *corev1.Changeset, slice *corev1.Slice, patchset *corev1.Patchset) {
	s.dispatcher.dispatchOutOfSliceChecks(ctx, cs, slice, patchset)
}

func (d *checkDispatcher) rerunCheck(ctx context.Context, cs *corev1.Changeset, slice *corev1.Slice, patchset *corev1.Patchset, checkName string) (*corev1.CheckRun, error) {
	if d == nil || d.Checks == nil || d.hub == nil {
		return nil, status.Error(codes.FailedPrecondition, "check dispatcher is not configured")
	}
	if cs == nil || slice == nil || patchset == nil {
		return nil, status.Error(codes.FailedPrecondition, "check run context is incomplete")
	}
	checkName = strings.TrimSpace(checkName)
	if checkName == "" {
		return nil, status.Error(codes.FailedPrecondition, "check run has no check name")
	}
	daemonID := strings.TrimSpace(slice.CiDaemonId)
	if daemonID == "" {
		return nil, status.Error(codes.FailedPrecondition, "slice has no CI daemon configured")
	}
	conn, ok := d.hub.daemon(daemonID)
	if !ok {
		return nil, status.Error(codes.FailedPrecondition, "slice CI daemon is not online")
	}

	plan, err := d.resolveCheckPlan(ctx, slice, patchset)
	if err != nil {
		return nil, status.Errorf(codes.FailedPrecondition, "check plan could not be resolved: %v", err)
	}
	spec, ok := runnableOutOfSliceCheck(plan, checkName)
	if !ok {
		if reason := erroredCheckReason(plan, checkName); reason != "" {
			return nil, status.Errorf(codes.FailedPrecondition, "check %q has no resolvable definition in this revision: %s", checkName, reason)
		}
		return nil, status.Errorf(codes.FailedPrecondition, "check %q has no runnable CI definition in this revision", checkName)
	}

	run, err := d.Checks.CreateCheckRun(ctx, storage.CheckRunInput{
		ChangesetID: cs.Id,
		PatchsetID:  patchset.Id,
		CheckName:   spec.Name,
		DaemonID:    daemonID,
		Provenance:  "ci",
		Status:      "queued",
	})
	if err != nil {
		return nil, grpcError(err)
	}

	msg := &corev1.ServerMessage{Payload: &corev1.ServerMessage_RunChecks{RunChecks: &corev1.RunChecks{
		ChangesetId:  cs.Id,
		PatchsetId:   patchset.Id,
		ResultTreeId: patchset.ResultTreeId,
		Slice:        slice.Ref,
		SliceId:      slice.Id,
		ServerAddr:   d.serverAddr,
		Checks:       []*corev1.CheckRunSpec{checkRunSpecFromPlan(run.Id, spec)},
	}}}
	if !conn.trySend(msg) {
		return nil, status.Error(codes.FailedPrecondition, "slice CI daemon is not online")
	}
	return run, nil
}

type changesetSubmitWithCheckStatuses interface {
	SubmitWithCheckStatuses(ctx context.Context, changesetID, expectedCurrentPatchsetID string, extraCheckStatuses map[string]string) (*corev1.SubmitChangesetResponse, error)
}

func (s *ChangesetService) skippedRequiredCheckStatuses(ctx context.Context, cs *corev1.Changeset) (map[string]string, error) {
	patchset := currentPatchset(cs)
	if patchset == nil {
		return nil, nil
	}
	slice, err := s.Slices.Resolve(ctx, cs.AuthoringSlice)
	if err != nil {
		return nil, err
	}
	if slice.Definition == nil || len(slice.Definition.RequiredChecks) == 0 {
		return nil, nil
	}
	if s.dispatcher == nil {
		return nil, fmt.Errorf("check dispatcher is not configured")
	}
	plan, err := s.dispatcher.resolveCheckPlan(ctx, slice, patchset)
	if err != nil {
		return nil, err
	}
	required := map[string]struct{}{}
	for _, name := range slice.Definition.RequiredChecks {
		name = strings.TrimSpace(name)
		if name != "" {
			required[name] = struct{}{}
		}
	}
	if len(required) == 0 {
		return nil, nil
	}
	out := map[string]string{}
	for _, skipped := range plan.Skipped {
		if _, ok := required[skipped.Name]; ok {
			out[skipped.Name] = storage.CheckStatusPass
		}
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}

func (s *AgentService) replayDaemonCheckRuns(ctx context.Context, daemonID string, conn *daemonConn) {
	if s.Checks == nil || s.dispatcher == nil || conn == nil {
		return
	}
	runs, err := s.Checks.ListRunsByDaemonStatus(ctx, daemonID, "queued")
	if err != nil {
		return
	}
	grouped := map[string][]*corev1.CheckRun{}
	for _, run := range runs {
		if run == nil || run.Provenance != "ci" {
			continue
		}
		key := run.ChangesetId + "\x00" + run.PatchsetId
		grouped[key] = append(grouped[key], run)
	}
	for _, group := range grouped {
		select {
		case <-ctx.Done():
			return
		default:
		}
		if len(group) == 0 {
			continue
		}
		msg, err := s.dispatcher.rebuildRunChecksMessage(ctx, group)
		if err != nil || msg == nil {
			continue
		}
		if !conn.trySend(msg) {
			return
		}
	}
}

func (d *checkDispatcher) rebuildRunChecksMessage(ctx context.Context, runs []*corev1.CheckRun) (*corev1.ServerMessage, error) {
	if d == nil || d.Changesets == nil || d.Slices == nil {
		return nil, fmt.Errorf("check dispatcher is not configured")
	}
	first := runs[0]
	cs, err := d.Changesets.Get(ctx, first.ChangesetId)
	if err != nil {
		return nil, err
	}
	patchset := patchsetByID(cs, first.PatchsetId)
	if patchset == nil {
		return nil, storage.ErrNotFound
	}
	slice, err := d.Slices.Resolve(ctx, cs.AuthoringSlice)
	if err != nil {
		return nil, err
	}
	plan, err := d.resolveCheckPlan(ctx, slice, patchset)
	if err != nil {
		return nil, err
	}
	byName := map[string]checks.CheckSpec{}
	for _, spec := range plan.Runnable {
		if spec.OutOfSlice {
			byName[spec.Name] = spec
		}
	}
	specs := make([]*corev1.CheckRunSpec, 0, len(runs))
	for _, run := range runs {
		spec, ok := byName[run.CheckName]
		if !ok {
			continue
		}
		specs = append(specs, checkRunSpecFromPlan(run.Id, spec))
	}
	if len(specs) == 0 {
		return nil, nil
	}
	return &corev1.ServerMessage{Payload: &corev1.ServerMessage_RunChecks{RunChecks: &corev1.RunChecks{
		ChangesetId:  first.ChangesetId,
		PatchsetId:   first.PatchsetId,
		ResultTreeId: patchset.ResultTreeId,
		Slice:        slice.Ref,
		SliceId:      slice.Id,
		ServerAddr:   d.serverAddr,
		Checks:       specs,
	}}}, nil
}

func (s *AgentService) handleCheckRunUpdate(ctx context.Context, daemonID string, conn *daemonConn, update *corev1.CheckRunUpdate) {
	if s.Checks == nil || conn == nil || update == nil {
		return
	}
	run, err := s.Checks.GetCheckRun(ctx, update.RunId)
	if err != nil {
		return
	}
	if run.DaemonId != daemonID {
		return
	}
	if update.LogChunk != "" && update.ClientSeq > 0 {
		streamName := defaultCheckStream(update.Stream)
		inserted, err := s.Checks.AppendCheckRunLog(ctx, update.RunId, update.ClientSeq, streamName, update.LogChunk)
		if err != nil {
			return
		}
		if inserted && s.checkLogs != nil {
			log := &corev1.CheckRunLog{
				RunId:  update.RunId,
				Seq:    update.ClientSeq,
				Stream: streamName,
				Chunk:  update.LogChunk,
			}
			if stored := s.persistedCheckRunLog(ctx, update.RunId, update.ClientSeq); stored != nil {
				log = stored
			}
			s.checkLogs.publish(update.RunId, log)
		}
	}
	if update.Status != "" {
		if _, err := s.Checks.UpdateCheckRunStatus(ctx, update.RunId, update.Status, update.ExitCode, update.Summary); err != nil {
			return
		}
		if s.checkLogs != nil {
			s.checkLogs.publish(update.RunId, nil)
		}
	} else if update.Final {
		if _, err := s.Checks.UpdateCheckRunStatus(ctx, update.RunId, "errored", update.ExitCode, update.Summary); err != nil {
			return
		}
		if s.checkLogs != nil {
			s.checkLogs.publish(update.RunId, nil)
		}
	}
	conn.trySend(&corev1.ServerMessage{Payload: &corev1.ServerMessage_CheckAck{CheckAck: &corev1.CheckRunAck{
		RunId:          update.RunId,
		AckedClientSeq: update.ClientSeq,
	}}})
}

func (s *AgentService) persistedCheckRunLog(ctx context.Context, runID string, seq int64) *corev1.CheckRunLog {
	logs, err := s.Checks.ListCheckRunLogs(ctx, runID, seq-1)
	if err != nil {
		return nil
	}
	for _, log := range logs {
		if log.GetSeq() == seq {
			return log
		}
	}
	return nil
}

func checkRunSpecFromPlan(runID string, spec checks.CheckSpec) *corev1.CheckRunSpec {
	return &corev1.CheckRunSpec{
		RunId:            runID,
		Name:             spec.Name,
		Command:          spec.Run,
		Image:            spec.Image,
		Setup:            append([]string(nil), spec.Setup...),
		WorkingDir:       spec.WorkingDir,
		MaterializePaths: append([]string(nil), spec.MaterializePaths...),
		Env:              copyStringMap(spec.Env),
		Network:          spec.Network,
		TimeoutMs:        spec.Timeout.Milliseconds(),
	}
}

func logicalPathsForAccount(account string, in []string) []string {
	out := make([]string, 0, len(in))
	accountPrefix := "/" + strings.Trim(strings.TrimSpace(account), "/")
	for _, value := range in {
		cleaned := path.Clean("/" + strings.TrimSpace(value))
		switch {
		case cleaned == accountPrefix:
			out = append(out, "/")
		case strings.HasPrefix(cleaned, accountPrefix+"/"):
			out = append(out, strings.TrimPrefix(cleaned, accountPrefix+"/"))
		default:
			out = append(out, strings.TrimPrefix(cleaned, "/"))
		}
	}
	return out
}

func patchsetByID(cs *corev1.Changeset, patchsetID string) *corev1.Patchset {
	if cs == nil {
		return nil
	}
	for _, patchset := range cs.Patchsets {
		if patchset != nil && patchset.Id == patchsetID {
			return patchset
		}
	}
	return nil
}

func runnableOutOfSliceCheck(plan *checks.Plan, checkName string) (checks.CheckSpec, bool) {
	if plan == nil {
		return checks.CheckSpec{}, false
	}
	for _, spec := range plan.Runnable {
		if spec.Name == checkName && spec.OutOfSlice {
			return spec, true
		}
	}
	return checks.CheckSpec{}, false
}

func erroredCheckReason(plan *checks.Plan, checkName string) string {
	if plan == nil {
		return ""
	}
	for _, errored := range plan.Errored {
		if errored.Name == checkName {
			return errored.Reason
		}
	}
	return ""
}

func copyStringMap(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}

func defaultCheckStream(stream string) string {
	stream = strings.ToLower(strings.TrimSpace(stream))
	if stream == "stderr" {
		return "stderr"
	}
	return "stdout"
}
