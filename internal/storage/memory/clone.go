package memory

import (
	"gitslice.io/gitslice/internal/storage"
	corev1 "gitslice.io/gitslice/proto/core/v1"
	"google.golang.org/protobuf/proto"
)

func cloneFileMap(in map[string]storage.FileEntry) map[string]storage.FileEntry {
	out := map[string]storage.FileEntry{}
	for k, v := range in {
		out[k] = v
	}
	return out
}

func cloneDirSet(in map[string]struct{}) map[string]struct{} {
	out := map[string]struct{}{}
	for p := range in {
		out[p] = struct{}{}
	}
	return out
}

func cloneFile(file storage.FileEntry) *storage.FileEntry {
	out := file
	return &out
}

func cloneBlob(in *corev1.BlobRecord) *corev1.BlobRecord {
	if in == nil {
		return nil
	}
	return proto.Clone(in).(*corev1.BlobRecord)
}

func cloneRef(in *corev1.Ref) *corev1.Ref {
	if in == nil {
		return nil
	}
	return proto.Clone(in).(*corev1.Ref)
}

func cloneCommit(in *corev1.Commit) *corev1.Commit {
	if in == nil {
		return nil
	}
	return proto.Clone(in).(*corev1.Commit)
}

func cloneSliceRef(in *corev1.SliceRef) *corev1.SliceRef {
	if in == nil {
		return nil
	}
	return proto.Clone(in).(*corev1.SliceRef)
}

func cloneSlice(in *corev1.Slice) *corev1.Slice {
	if in == nil {
		return nil
	}
	return proto.Clone(in).(*corev1.Slice)
}

func cloneSliceDefinitionVersion(in *corev1.SliceDefinitionVersion) *corev1.SliceDefinitionVersion {
	if in == nil {
		return nil
	}
	return proto.Clone(in).(*corev1.SliceDefinitionVersion)
}

func clonePatchset(in *corev1.Patchset) *corev1.Patchset {
	if in == nil {
		return nil
	}
	return proto.Clone(in).(*corev1.Patchset)
}

func cloneStackEntry(in *corev1.ChangesetStackEntry) *corev1.ChangesetStackEntry {
	if in == nil {
		return nil
	}
	return proto.Clone(in).(*corev1.ChangesetStackEntry)
}

func cloneStack(in *corev1.ChangesetStack) *corev1.ChangesetStack {
	if in == nil {
		return nil
	}
	return proto.Clone(in).(*corev1.ChangesetStack)
}

func cloneChangeset(in *corev1.Changeset) *corev1.Changeset {
	if in == nil {
		return nil
	}
	return proto.Clone(in).(*corev1.Changeset)
}

func cloneCheckRun(in *corev1.CheckRun) *corev1.CheckRun {
	if in == nil {
		return nil
	}
	return proto.Clone(in).(*corev1.CheckRun)
}

func cloneCheckRunLog(in *corev1.CheckRunLog) *corev1.CheckRunLog {
	if in == nil {
		return nil
	}
	return proto.Clone(in).(*corev1.CheckRunLog)
}

func cloneGitImport(in *storage.GitImportRecord) *storage.GitImportRecord {
	if in == nil {
		return nil
	}
	out := *in
	return &out
}
