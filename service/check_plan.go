package service

import (
	"context"
	"errors"
	"fmt"
	"path"
	"sort"
	"strings"

	"gitslice.io/gitslice/internal/checks"
	"gitslice.io/gitslice/internal/gitslicecfg"
	"gitslice.io/gitslice/internal/storage"
	corev1 "gitslice.io/gitslice/proto/core/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const maxCheckTreeReadFileBytes = 64 << 20

type checkTreeReader struct {
	repository storage.RepositoryStore
	objects    ObjectStore
	account    string
}

func (r checkTreeReader) ReadFile(ctx context.Context, rootTreeID, logicalPath string) ([]byte, error) {
	if r.repository == nil || r.objects == nil {
		return nil, fmt.Errorf("check tree reader is not configured")
	}
	reader := newGitsliceTreeReader(r.repository, r.objects, maxCheckTreeReadFileBytes, r.account, checks.ErrNotFound)
	data, err := reader.ReadFile(ctx, rootTreeID, logicalPath)
	if errors.Is(err, gitslicecfg.ErrFileTooLarge) {
		return nil, status.Errorf(codes.ResourceExhausted, "check file %s exceeds %d byte limit", logicalPath, maxCheckTreeReadFileBytes)
	}
	return data, err
}

func (d *checkDispatcher) checkResolver(account string) *gitslicecfg.Resolver[*checks.File] {
	account = strings.TrimSpace(account)
	d.checkResolversMu.Lock()
	defer d.checkResolversMu.Unlock()
	if d.checkResolvers == nil {
		d.checkResolvers = make(map[string]*gitslicecfg.Resolver[*checks.File])
	}
	if r, ok := d.checkResolvers[account]; ok {
		return r
	}
	if len(d.checkResolvers) >= 256 {
		clear(d.checkResolvers)
	}
	r := checks.NewResolver(checkTreeReader{
		repository: d.Repository,
		objects:    d.ObjectStore,
		account:    account,
	})
	d.checkResolvers[account] = r
	return r
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
	return checks.ResolveWithResolver(
		ctx,
		d.checkResolver(slice.Ref.Account),
		patchset.ResultTreeId,
		logicalPathsForAccount(slice.Ref.Account, patchset.ChangedPaths),
		logicalPathsForAccount(slice.Ref.Account, slice.Definition.IncludedPaths),
		slice.Definition.RequiredChecks,
	)
}

func isRequiredSliceCheck(slice *corev1.Slice, checkName string) bool {
	checkName = strings.TrimSpace(checkName)
	if checkName == "" || slice == nil || slice.Definition == nil {
		return false
	}
	for _, req := range slice.Definition.RequiredChecks {
		if strings.TrimSpace(req) == checkName {
			return true
		}
	}
	return false
}

func checkRunSpecFromPlan(runID string, spec checks.CheckSpec, secrets map[string]string) *corev1.CheckRunSpec {
	return &corev1.CheckRunSpec{
		RunId:            runID,
		Name:             spec.Name,
		Command:          spec.Run,
		Image:            spec.Image,
		Setup:            append([]string(nil), spec.Setup...),
		Cache:            append([]string(nil), spec.Cache...),
		WorkingDir:       spec.WorkingDir,
		MaterializePaths: append([]string(nil), spec.MaterializePaths...),
		Env:              mergeCheckEnv(spec.Env, secrets),
		Network:          spec.Network,
		Memory:           spec.Memory,
		Cpus:             spec.CPUs,
		TimeoutMs:        spec.Timeout.Milliseconds(),
		Build:            checkImageBuildToProto(spec.Build),
		Services:         checkServicesToProto(spec.Services),
		SecretEnv:        sortedKeys(secrets),
	}
}

func sortedKeys(values map[string]string) []string {
	if len(values) == 0 {
		return nil
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func checkImageBuildToProto(build *checks.ImageBuild) *corev1.CheckImageBuild {
	if build == nil {
		return nil
	}
	return &corev1.CheckImageBuild{
		ContextDir: build.ContextDir,
		Dockerfile: build.Dockerfile,
		Inputs:     append([]string(nil), build.Inputs...),
		TimeoutMs:  build.Timeout.Milliseconds(),
	}
}

func checkServicesToProto(services []checks.ServiceSpec) []*corev1.CheckServiceContainer {
	out := make([]*corev1.CheckServiceContainer, 0, len(services))
	for _, service := range services {
		out = append(out, &corev1.CheckServiceContainer{
			Name:           service.Name,
			Image:          service.Image,
			Env:            copyStringMap(service.Env),
			Ready:          service.Ready,
			ReadyTimeoutMs: service.ReadyTimeout.Milliseconds(),
			Memory:         service.Memory,
		})
	}
	return out
}

func mergeCheckEnv(checkEnv, secrets map[string]string) map[string]string {
	out := copyStringMap(checkEnv)
	if len(secrets) == 0 {
		return out
	}
	if out == nil {
		out = map[string]string{}
	}
	for key, value := range secrets {
		out[key] = value
	}
	return out
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

// runnableCICheck finds a runnable check, in-slice or out-of-slice, that the
// slice's CI daemon can run for the patchset.
func runnableCICheck(plan *checks.Plan, checkName string) (checks.CheckSpec, bool) {
	return plan.Spec(checkName)
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
