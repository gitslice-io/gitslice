package gitcompat

import (
	"strings"
	"time"

	"gitslice.io/gitslice/internal/storage"
	corev1 "gitslice.io/gitslice/proto/core/v1"
)

// projectablePath rejects paths Git cannot store, such as anything inside a
// ".git" directory.
func projectablePath(path string) bool {
	if path == "" {
		return false
	}
	for _, part := range strings.Split(path, "/") {
		if part == "" || part == "." || part == ".." || strings.EqualFold(part, ".git") {
			return false
		}
	}
	return true
}

// gitFileMode maps a native file mode to a Git tree mode. Symlinks keep their
// type; their content is the link target, as in Git.
func gitFileMode(mode uint32) string {
	switch {
	case mode&0o170000 == 0o120000:
		return "120000"
	case mode&0o111 != 0:
		return "100755"
	default:
		return "100644"
	}
}

func projectedIdentity(subjectID string, usernames map[string]string) (string, string) {
	name := usernames[subjectID]
	if name == "" || strings.ContainsAny(name, "<>\n\r\x00") {
		return "Gitslice", "gitslice@" + noreplyDomain
	}
	return name, name + "@" + noreplyDomain
}

func projectedTime(createdAt string) int64 {
	t, err := time.Parse(time.RFC3339Nano, createdAt)
	if err != nil {
		return 0
	}
	return t.UTC().Unix()
}

// projectedCommitMessage is the commit message followed by a trailer naming
// the native commit, which lets clients and the GitHub exporter map a projected
// commit back to Gitslice. A commit published by a Git import keeps its
// original full message and gains a Git-Commit trailer naming the original
// commit.
func projectedCommitMessage(commit *corev1.Commit, imported *storage.GitImportedCommitRecord) string {
	source := commit.Message
	if imported != nil && strings.TrimSpace(imported.FullMessage) != "" {
		source = imported.FullMessage
	}
	message := strings.TrimRight(strings.ReplaceAll(source, "\r\n", "\n"), " \t\n")
	if strings.TrimSpace(message) == "" {
		message = "Gitslice commit " + shortNativeID(commit.Id)
	}
	trailers := ""
	if imported != nil && imported.GitCommitID != "" {
		trailers = gitCommitTrailer + ": " + imported.GitCommitID + "\n"
	}
	return message + "\n\n" + trailers + commitTrailer + ": " + commit.Id + "\n"
}

// importedAuthor returns the original Git author of an imported commit, or
// the native identity when there is no usable record. The committer stays the
// native identity, which records who published the import and when.
func importedAuthor(imported *storage.GitImportedCommitRecord, name, email string, when int64) (string, string, int64) {
	if imported == nil || imported.AuthorName == "" || strings.ContainsAny(imported.AuthorName+imported.AuthorEmail, "<>\n\r\x00") {
		return name, email, when
	}
	authored := when
	if imported.AuthoredAt != "" {
		if t, err := time.Parse(time.RFC3339Nano, imported.AuthoredAt); err == nil {
			authored = t.UTC().Unix()
		}
	}
	return imported.AuthorName, imported.AuthorEmail, authored
}

func shortNativeID(id string) string {
	if len(id) > 12 {
		return id[:12]
	}
	return id
}
