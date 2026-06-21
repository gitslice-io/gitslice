package memory

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"path"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/gitslice-io/gitslice/internal/objectid"
	"github.com/gitslice-io/gitslice/internal/paths"
	"github.com/gitslice-io/gitslice/internal/storage"
	corev1 "github.com/gitslice-io/gitslice/proto/core/v1"
)

type Stores struct {
	Auth       *AuthStore
	Blobs      *BlobStore
	Changesets *ChangesetStore
	Repository *RepositoryStore
	Slices     *SliceStore
	Objects    *ObjectStore
	Agents     *AgentStore

	backend *backend
}

type backend struct {
	mu   sync.Mutex
	next int64

	subjects         map[string]storage.Subject
	accountMembers   map[string]map[string]string
	personalAccounts map[string]string
	sessions         map[string]string
	cliLoginSessions map[string]cliLoginSession

	blobs   map[string]*corev1.BlobRecord
	objects map[string][]byte

	refs        map[string]*corev1.Ref
	commits     map[string]*corev1.Commit
	commitFiles map[string]map[string]storage.FileEntry
	commitDirs  map[string]map[string]struct{}

	slices                  map[string]*corev1.Slice
	sliceRefs               map[string]string
	sliceDefinitionVersions map[string][]*corev1.SliceDefinitionVersion

	changesets        map[string]*corev1.Changeset
	stacks            map[string]*corev1.ChangesetStack
	previewFiles      map[string]map[string]storage.FileEntry
	previewDirs       map[string]map[string]struct{}
	pendingAcceptedAt map[string]time.Time
	pendingSequence   map[string]int64
	nextPendingSeq    int64
	approvals         map[string]map[string]struct{}
	checkResults      map[string]map[string]string

	imports         map[string]*storage.GitImportRecord
	importsByKey    map[string]string
	importedCommits map[string][]storage.GitImportedCommitRecord

	entitiesByPath map[string]storage.CurrentPathEntity
	entityChanges  map[string][]storage.HistoryEntityRef

	agentDaemons     map[string]*corev1.AgentDaemon
	agentConvs       map[string]*corev1.Conversation
	agentEvents      map[string][]*corev1.ConversationEvent
	agentConvNextSeq map[string]int64
}

type cliLoginSession struct {
	status       string
	subjectID    string
	sessionToken string
	expiresAt    time.Time
}

type AuthStore struct{ b *backend }
type BlobStore struct{ b *backend }
type ChangesetStore struct{ b *backend }
type RepositoryStore struct{ b *backend }
type SliceStore struct{ b *backend }
type ObjectStore struct{ b *backend }

func New() *Stores {
	b := &backend{
		subjects:                map[string]storage.Subject{},
		accountMembers:          map[string]map[string]string{},
		personalAccounts:        map[string]string{},
		sessions:                map[string]string{},
		cliLoginSessions:        map[string]cliLoginSession{},
		blobs:                   map[string]*corev1.BlobRecord{},
		objects:                 map[string][]byte{},
		refs:                    map[string]*corev1.Ref{},
		commits:                 map[string]*corev1.Commit{},
		commitFiles:             map[string]map[string]storage.FileEntry{},
		commitDirs:              map[string]map[string]struct{}{},
		slices:                  map[string]*corev1.Slice{},
		sliceRefs:               map[string]string{},
		sliceDefinitionVersions: map[string][]*corev1.SliceDefinitionVersion{},
		changesets:              map[string]*corev1.Changeset{},
		stacks:                  map[string]*corev1.ChangesetStack{},
		previewFiles:            map[string]map[string]storage.FileEntry{},
		previewDirs:             map[string]map[string]struct{}{},
		pendingAcceptedAt:       map[string]time.Time{},
		pendingSequence:         map[string]int64{},
		approvals:               map[string]map[string]struct{}{},
		checkResults:            map[string]map[string]string{},
		imports:                 map[string]*storage.GitImportRecord{},
		importsByKey:            map[string]string{},
		importedCommits:         map[string][]storage.GitImportedCommitRecord{},
		entitiesByPath:          map[string]storage.CurrentPathEntity{},
		entityChanges:           map[string][]storage.HistoryEntityRef{},
		agentDaemons:            map[string]*corev1.AgentDaemon{},
		agentConvs:              map[string]*corev1.Conversation{},
		agentEvents:             map[string][]*corev1.ConversationEvent{},
		agentConvNextSeq:        map[string]int64{},
	}
	root := &corev1.Commit{
		Id:         "mem_root",
		RootTreeId: "mem_tree_root",
		Message:    "root",
		CreatedAt:  time.Unix(0, 0).UTC().Format(time.RFC3339Nano),
	}
	b.commits[root.Id] = cloneCommit(root)
	b.commitFiles[root.Id] = map[string]storage.FileEntry{}
	b.commitDirs[root.Id] = map[string]struct{}{}
	b.refs[storage.DefaultTargetRef] = &corev1.Ref{Name: storage.DefaultTargetRef, CommitId: root.Id, UpdatedAt: root.CreatedAt}
	return &Stores{
		Auth:       &AuthStore{b: b},
		Blobs:      &BlobStore{b: b},
		Changesets: &ChangesetStore{b: b},
		Repository: &RepositoryStore{b: b},
		Slices:     &SliceStore{b: b},
		Objects:    &ObjectStore{b: b},
		Agents:     &AgentStore{b: b},
		backend:    b,
	}
}

func (s *Stores) AddAccount(subjectID, accountSlug string) {
	s.backend.mu.Lock()
	defer s.backend.mu.Unlock()
	s.backend.addAccountLocked(subjectID, accountSlug)
}

func (s *Stores) AddAccountRole(subjectID, accountSlug, role string) {
	s.backend.mu.Lock()
	defer s.backend.mu.Unlock()
	s.backend.addAccountRoleLocked(subjectID, accountSlug, role)
}

func (s *Stores) PutSlice(ref *corev1.SliceRef, includedPaths []string, visibility string) *corev1.Slice {
	return s.PutSliceWithSubmitSettings(ref, includedPaths, visibility, 0, nil)
}

func (s *Stores) PutSliceWithSubmitSettings(ref *corev1.SliceRef, includedPaths []string, visibility string, requiredApprovals int32, requiredChecks []string) *corev1.Slice {
	s.backend.mu.Lock()
	defer s.backend.mu.Unlock()
	slice, _ := s.backend.putSliceLocked(ref, includedPaths, visibility, requiredApprovals, requiredChecks, "system")
	return slice
}

func (s *Stores) PutCommitWithFiles(commitID string, files []storage.FileEntry, changedPaths []string) {
	s.backend.mu.Lock()
	defer s.backend.mu.Unlock()
	s.backend.putCommitWithFilesLocked(commitID, files, changedPaths, "memory commit")
}

func (s *Stores) PutObject(key string, data []byte) {
	s.backend.mu.Lock()
	defer s.backend.mu.Unlock()
	s.backend.objects[key] = append([]byte(nil), data...)
}

func (b *backend) addAccountLocked(subjectID, accountSlug string) {
	b.addAccountRoleLocked(subjectID, accountSlug, "admin")
}

func (b *backend) addAccountRoleLocked(subjectID, accountSlug, role string) {
	subjectID = strings.TrimSpace(subjectID)
	accountSlug = strings.TrimSpace(accountSlug)
	role = strings.TrimSpace(role)
	if subjectID == "" || accountSlug == "" {
		return
	}
	if role == "" {
		role = "member"
	}
	if _, ok := b.subjects[subjectID]; !ok {
		b.subjects[subjectID] = storage.Subject{ID: subjectID, DisplayName: strings.TrimPrefix(subjectID, "user_")}
	}
	if b.accountMembers[subjectID] == nil {
		b.accountMembers[subjectID] = map[string]string{}
	}
	b.accountMembers[subjectID][accountSlug] = role
	home := &corev1.SliceRef{Account: accountSlug, Slice: "home"}
	if _, ok := b.sliceRefs[sliceRefKey(home)]; !ok {
		_, _ = b.putSliceLocked(home, []string{"/" + accountSlug}, "private", 0, nil, subjectID)
	}
	b.ensureAccountRootDirectoryLocked(accountSlug, subjectID)
}

func (b *backend) putSliceLocked(ref *corev1.SliceRef, includedPaths []string, visibility string, requiredApprovals int32, requiredChecks []string, createdBy string) (*corev1.Slice, error) {
	ref, err := normalizeSliceRef(ref)
	if err != nil {
		return nil, err
	}
	includedPaths, visibility, requiredApprovals, requiredChecks, err = validateSliceDefinition(ref, includedPaths, visibility, requiredApprovals, requiredChecks)
	if err != nil {
		return nil, err
	}
	id := sliceID(ref.Account, ref.Slice)
	version := int64(1)
	if existing := b.slices[id]; existing != nil && existing.Definition != nil {
		version = existing.Definition.Version + 1
	}
	definitionHash := memoryDefinitionHash(id, version, includedPaths, visibility, requiredApprovals, requiredChecks)
	slice := &corev1.Slice{
		Id:             id,
		Ref:            cloneSliceRef(ref),
		DefinitionHash: definitionHash,
		Definition: &corev1.SliceDefinition{
			SliceId:           id,
			Version:           version,
			IncludedPaths:     append([]string(nil), includedPaths...),
			Visibility:        visibility,
			RequiredApprovals: requiredApprovals,
			RequiredChecks:    append([]string(nil), requiredChecks...),
		},
	}
	b.slices[id] = cloneSlice(slice)
	b.sliceRefs[sliceRefKey(ref)] = id
	b.appendSliceDefinitionVersionLocked(slice, createdBy)
	return cloneSlice(slice), nil
}

func (b *backend) appendSliceDefinitionVersionLocked(slice *corev1.Slice, createdBy string) {
	if slice == nil || slice.Definition == nil {
		return
	}
	version := &corev1.SliceDefinitionVersion{
		SliceId:           slice.Id,
		Version:           slice.Definition.Version,
		DefinitionHash:    slice.DefinitionHash,
		Visibility:        slice.Definition.Visibility,
		IncludedPaths:     append([]string(nil), slice.Definition.IncludedPaths...),
		RequiredApprovals: slice.Definition.RequiredApprovals,
		RequiredChecks:    append([]string(nil), slice.Definition.RequiredChecks...),
		CreatedAt:         time.Now().UTC().Format(time.RFC3339Nano),
		CreatedBy:         strings.TrimSpace(createdBy),
	}
	b.sliceDefinitionVersions[slice.Id] = append(b.sliceDefinitionVersions[slice.Id], cloneSliceDefinitionVersion(version))
}

func (b *backend) putCommitWithFilesLocked(commitID string, files []storage.FileEntry, changedPaths []string, message string) {
	b.putCommitWithFilesAndDirsLocked(commitID, files, nil, changedPaths, message)
}

func (b *backend) putCommitWithFilesAndDirsLocked(commitID string, files []storage.FileEntry, dirs map[string]struct{}, changedPaths []string, message string) {
	if commitID == "" {
		commitID = b.nextIDLocked("commit")
	}
	parent := ""
	if ref := b.refs[storage.DefaultTargetRef]; ref != nil {
		parent = ref.CommitId
	}
	createdAt := time.Now().UTC().Format(time.RFC3339Nano)
	commit := &corev1.Commit{
		Id:           commitID,
		RootTreeId:   "mem_tree_" + commitID,
		Message:      message,
		CreatedAt:    createdAt,
		ChangedPaths: append([]string(nil), changedPaths...),
	}
	if parent != "" {
		commit.ParentIds = []string{parent}
	}
	byPath := map[string]storage.FileEntry{}
	if dirs == nil {
		dirs = cloneDirSet(b.commitDirs[parent])
	} else {
		dirs = cloneDirSet(dirs)
	}
	for _, file := range files {
		byPath[file.Path] = file
		for parent := path.Dir(file.Path); parent != "" && parent != "/" && parent != "."; parent = path.Dir(parent) {
			dirs[parent] = struct{}{}
		}
		b.entitiesByPath[file.Path] = storage.CurrentPathEntity{
			Path:        file.Path,
			AccountID:   accountFromPath(file.Path),
			EntityID:    "ent_" + strings.ReplaceAll(strings.Trim(file.Path, "/"), "/", "_"),
			Kind:        "file",
			ContentHash: file.ContentHash,
			Mode:        file.Mode,
		}
	}
	b.commits[commitID] = cloneCommit(commit)
	b.commitFiles[commitID] = byPath
	b.commitDirs[commitID] = dirs
	b.refs[storage.DefaultTargetRef] = &corev1.Ref{Name: storage.DefaultTargetRef, CommitId: commitID, UpdatedAt: createdAt, UpdatedBy: "memory"}
}

func (b *backend) ensureAccountRootDirectoryLocked(accountSlug, subjectID string) {
	accountSlug = strings.TrimSpace(accountSlug)
	if accountSlug == "" {
		return
	}
	ref := b.refs[storage.DefaultTargetRef]
	if ref == nil {
		return
	}
	accountRoot := "/" + accountSlug
	if _, ok := b.commitDirs[ref.CommitId][accountRoot]; ok {
		return
	}
	files := cloneFileMap(b.commitFiles[ref.CommitId])
	dirs := cloneDirSet(b.commitDirs[ref.CommitId])
	dirs[accountRoot] = struct{}{}

	commitID := b.nextIDLocked("commit")
	createdAt := time.Now().UTC().Format(time.RFC3339Nano)
	commit := &corev1.Commit{
		Id:           commitID,
		ParentIds:    []string{ref.CommitId},
		RootTreeId:   "mem_tree_" + commitID,
		Author:       subjectID,
		Message:      "Create account root " + accountRoot,
		CreatedAt:    createdAt,
		ChangedPaths: []string{accountRoot},
	}
	b.commits[commitID] = cloneCommit(commit)
	b.commitFiles[commitID] = files
	b.commitDirs[commitID] = dirs
	b.refs[storage.DefaultTargetRef] = &corev1.Ref{Name: storage.DefaultTargetRef, CommitId: commitID, UpdatedAt: createdAt, UpdatedBy: subjectID}
	b.entitiesByPath[accountRoot] = storage.CurrentPathEntity{
		Path:      accountRoot,
		AccountID: accountSlug,
		EntityID:  "ent_" + accountSlug,
		Kind:      "directory",
	}
}

func (b *backend) nextIDLocked(prefix string) string {
	b.next++
	return fmt.Sprintf("%s_%d", prefix, b.next)
}

func (b *backend) nextChangesetNumberLocked(sliceID string) int64 {
	var max int64
	for _, cs := range b.changesets {
		if cs == nil {
			continue
		}
		if b.sliceRefs[sliceRefKey(cs.AuthoringSlice)] == sliceID && cs.Number > max {
			max = cs.Number
		}
	}
	return max + 1
}

func memoryDefinitionHash(sliceID string, version int64, included []string, visibility string, requiredApprovals int32, requiredChecks []string) string {
	payload, _ := json.Marshal(struct {
		SliceID           string   `json:"slice_id"`
		Version           int64    `json:"version"`
		Included          []string `json:"included_paths"`
		Visibility        string   `json:"visibility"`
		RequiredApprovals int32    `json:"required_approvals"`
		RequiredChecks    []string `json:"required_checks"`
	}{SliceID: sliceID, Version: version, Included: included, Visibility: visibility, RequiredApprovals: requiredApprovals, RequiredChecks: requiredChecks})
	sum := sha256.Sum256(payload)
	return "mem_sha256:" + hex.EncodeToString(sum[:])
}

func memoryTokenHash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func (b *backend) resolveChangesetSelectorLocked(selector string) string {
	prefix, ok := storage.ChangesetIDLookupPrefix(selector)
	if !ok {
		return ""
	}
	var match string
	for id, cs := range b.changesets {
		if cs == nil {
			continue
		}
		if strings.HasPrefix(strings.ToLower(id), prefix) {
			if match != "" {
				return ""
			}
			match = id
		}
	}
	return match
}

func (s *ObjectStore) Put(ctx context.Context, key string, r io.Reader) error {
	data, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	s.b.objects[key] = append([]byte(nil), data...)
	return nil
}

func (s *ObjectStore) Get(ctx context.Context, key string, offset, length int64) (io.ReadCloser, error) {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	data, ok := s.b.objects[key]
	if !ok {
		return nil, storage.ErrNotFound
	}
	if offset < 0 || length < 0 {
		return nil, storage.ErrInvalid
	}
	if offset > int64(len(data)) {
		offset = int64(len(data))
	}
	end := int64(len(data))
	if length > 0 && offset+length < end {
		end = offset + length
	}
	return io.NopCloser(bytes.NewReader(append([]byte(nil), data[offset:end]...))), nil
}

func (s *ObjectStore) Delete(ctx context.Context, key string) error {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	delete(s.b.objects, key)
	return nil
}

func (s *AuthStore) StartCliLogin(ctx context.Context) (string, time.Time, error) {
	code, err := objectid.RandomID("cli")
	if err != nil {
		return "", time.Time{}, err
	}
	expiresAt := time.Now().UTC().Add(10 * time.Minute)

	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	s.b.cliLoginSessions[memoryTokenHash(code)] = cliLoginSession{
		status:    "pending",
		expiresAt: expiresAt,
	}
	return code, expiresAt, nil
}

func (s *AuthStore) CompleteCliLogin(ctx context.Context, code, subjectID string) error {
	code = strings.TrimSpace(code)
	subjectID = strings.TrimSpace(subjectID)
	if code == "" || subjectID == "" {
		return storage.ErrNotFound
	}
	token, err := objectid.RandomID("clitok")
	if err != nil {
		return err
	}

	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	if _, ok := s.b.subjects[subjectID]; !ok {
		return storage.ErrNotFound
	}
	codeHash := memoryTokenHash(code)
	session, ok := s.b.cliLoginSessions[codeHash]
	if !ok || session.status != "pending" || !session.expiresAt.After(time.Now().UTC()) {
		if ok {
			delete(s.b.cliLoginSessions, codeHash)
		}
		return storage.ErrNotFound
	}
	s.b.sessions[token] = subjectID
	session.status = "approved"
	session.subjectID = subjectID
	session.sessionToken = token
	s.b.cliLoginSessions[codeHash] = session
	return nil
}

func (s *AuthStore) PollCliLogin(ctx context.Context, code string) (string, string, string, error) {
	code = strings.TrimSpace(code)
	if code == "" {
		return "expired", "", "", nil
	}

	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	codeHash := memoryTokenHash(code)
	session, ok := s.b.cliLoginSessions[codeHash]
	if !ok {
		return "expired", "", "", nil
	}
	if !session.expiresAt.After(time.Now().UTC()) {
		delete(s.b.cliLoginSessions, codeHash)
		return "expired", "", "", nil
	}
	if session.status == "pending" {
		return "pending", "", "", nil
	}
	if session.status == "approved" {
		delete(s.b.cliLoginSessions, codeHash)
		return "approved", session.sessionToken, session.subjectID, nil
	}
	return "expired", "", "", nil
}

func (s *AuthStore) EnsureExternalSubject(ctx context.Context, externalID, email string) (string, error) {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	externalID = strings.TrimSpace(externalID)
	if externalID == "" {
		return "", storage.ErrInvalid
	}
	subjectID := storage.ExternalSubjectID(externalID)
	displayName := strings.TrimSpace(email)
	if displayName == "" {
		displayName = subjectID
	}
	if _, ok := s.b.subjects[subjectID]; !ok {
		s.b.subjects[subjectID] = storage.Subject{ID: subjectID, DisplayName: displayName}
	}
	return subjectID, nil
}

func (s *AuthStore) UsernameAvailable(ctx context.Context, username string) (bool, string, string, error) {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	normalized, err := normalizeSignupUsername(username)
	if err != nil {
		return false, "", err.Error(), nil
	}
	if s.b.accountSlugTakenLocked(normalized) {
		return false, normalized, "username is taken", nil
	}
	return true, normalized, "", nil
}

func (s *AuthStore) ChooseUsername(ctx context.Context, subjectID, username string) (string, error) {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	subjectID = strings.TrimSpace(subjectID)
	if subjectID == "" {
		return "", fmt.Errorf("%w: subject is required", storage.ErrInvalid)
	}
	username, err := normalizeSignupUsername(username)
	if err != nil {
		return "", fmt.Errorf("%w: %v", storage.ErrInvalid, err)
	}
	if _, ok := s.b.subjects[subjectID]; !ok {
		return "", storage.ErrNotFound
	}
	if existing := s.b.personalAccounts[subjectID]; existing != "" {
		return existing, nil
	}
	if s.b.accountSlugTakenLocked(username) {
		return "", fmt.Errorf("%w: username %q is not available", storage.ErrConflict, username)
	}
	s.b.provisionPersonalAccountLocked(subjectID, username, username)
	return username, nil
}

func (s *AuthStore) UsernamesForSubjects(ctx context.Context, subjectIDs []string) (map[string]string, error) {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()

	out := map[string]string{}
	for _, subjectID := range subjectIDs {
		subjectID = strings.TrimSpace(subjectID)
		if subjectID == "" {
			continue
		}
		if username := s.b.personalAccounts[subjectID]; username != "" {
			out[subjectID] = username
		}
	}
	return out, nil
}

func (b *backend) provisionPersonalAccountLocked(subjectID, username, displayName string) {
	if _, ok := b.subjects[subjectID]; !ok {
		b.subjects[subjectID] = storage.Subject{ID: subjectID, DisplayName: displayName}
	}
	b.personalAccounts[subjectID] = username
	b.addAccountLocked(subjectID, username)
}

func (b *backend) accountSlugTakenLocked(accountSlug string) bool {
	accountSlug = strings.TrimSpace(accountSlug)
	if accountSlug == "" {
		return false
	}
	for _, memberships := range b.accountMembers {
		if _, ok := memberships[accountSlug]; ok {
			return true
		}
	}
	return false
}

func (s *AuthStore) SubjectForToken(ctx context.Context, token string) (*storage.Subject, error) {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	subjectID := s.b.sessions[strings.TrimSpace(token)]
	if subjectID == "" {
		return nil, storage.ErrUnauthenticated
	}
	subject := s.b.subjects[subjectID]
	return &subject, nil
}

func (s *AuthStore) EnsureAccountMember(ctx context.Context, subjectID, accountSlug string) error {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	if _, ok := s.b.accountMembers[subjectID][accountSlug]; !ok {
		return storage.ErrUnauthorized
	}
	return nil
}

func (s *AuthStore) AccountRole(ctx context.Context, subjectID, accountSlug string) (string, error) {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	role, ok := s.b.accountMembers[subjectID][accountSlug]
	if !ok {
		return "", storage.ErrUnauthorized
	}
	return role, nil
}

func (s *AuthStore) ListSubjectAccountSlugs(ctx context.Context, subjectID string) ([]string, error) {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	memberships := s.b.accountMembers[subjectID]
	if len(memberships) == 0 {
		return nil, nil
	}
	out := make([]string, 0, len(memberships))
	for slug := range memberships {
		out = append(out, slug)
	}
	sort.Strings(out)
	personalSlug := s.b.personalAccounts[subjectID]
	if personalSlug == "" {
		personalSlug = strings.TrimPrefix(strings.TrimSpace(subjectID), "user_")
		personalSlug = strings.ReplaceAll(personalSlug, "_", "-")
	}
	for i, slug := range out {
		if slug == personalSlug {
			copy(out[1:i+1], out[:i])
			out[0] = slug
			break
		}
	}
	return out, nil
}

func (s *BlobStore) Upsert(ctx context.Context, blobID, contentHash string, size int64, storageLocation string) error {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	s.b.blobs[blobID] = &corev1.BlobRecord{Id: blobID, ContentHash: contentHash, Size: size, StorageLocation: storageLocation, State: "available"}
	return nil
}

func (s *BlobStore) GetByID(ctx context.Context, blobID string) (*corev1.BlobRecord, error) {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	blob := s.b.blobs[blobID]
	if blob == nil {
		return nil, storage.ErrNotFound
	}
	return cloneBlob(blob), nil
}

func (s *BlobStore) GetByContentHash(ctx context.Context, hashes []string) ([]*corev1.BlobRecord, error) {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	wanted := map[string]struct{}{}
	for _, hash := range hashes {
		wanted[hash] = struct{}{}
	}
	var out []*corev1.BlobRecord
	for _, blob := range s.b.blobs {
		if _, ok := wanted[blob.ContentHash]; ok {
			out = append(out, cloneBlob(blob))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ContentHash < out[j].ContentHash })
	return out, nil
}

func (s *ChangesetStore) CreateStack(ctx context.Context, subjectID string, req *corev1.CreateStackRequest) (*corev1.ChangesetStack, error) {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	if req.AuthoringSlice == nil {
		return nil, storage.ErrInvalid
	}
	sliceID := s.b.sliceRefs[sliceRefKey(req.AuthoringSlice)]
	if sliceID == "" {
		return nil, storage.ErrNotFound
	}
	targetRef := req.TargetRef
	if targetRef == "" {
		targetRef = storage.DefaultTargetRef
	}
	baseCommitID := req.BaseCommitId
	if baseCommitID == "" {
		ref := s.b.refs[targetRef]
		if ref == nil {
			return nil, storage.ErrNotFound
		}
		baseCommitID = ref.CommitId
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	stack := &corev1.ChangesetStack{
		Id:             s.b.nextIDLocked("stk"),
		AuthoringSlice: cloneSliceRef(req.AuthoringSlice),
		TargetRef:      targetRef,
		BaseCommitId:   baseCommitID,
		Title:          strings.TrimSpace(req.Title),
		Status:         "open",
		CreatedBy:      strings.TrimSpace(subjectID),
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	s.b.stacks[stack.Id] = cloneStack(stack)
	return cloneStack(stack), nil
}

func (s *ChangesetStore) GetStack(ctx context.Context, stackID string) (*corev1.ChangesetStack, error) {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	stack := s.b.stacks[strings.TrimSpace(stackID)]
	if stack == nil {
		return nil, storage.ErrNotFound
	}
	return s.b.hydrateStackLocked(stack), nil
}

func (s *ChangesetStore) ListStacks(ctx context.Context, req *corev1.ListStacksRequest) ([]*corev1.ChangesetStack, error) {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	limit := int(req.Limit)
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	status := strings.TrimSpace(req.Status)
	out := make([]*corev1.ChangesetStack, 0, len(s.b.stacks))
	for _, stack := range s.b.stacks {
		if req.AuthoringSlice != nil && !sameSliceRef(stack.AuthoringSlice, req.AuthoringSlice) {
			continue
		}
		if status != "" && stack.Status != status {
			continue
		}
		if status == "" && stack.Status == "closed" {
			continue
		}
		out = append(out, s.b.hydrateStackLocked(stack))
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].UpdatedAt == out[j].UpdatedAt {
			return out[i].Id < out[j].Id
		}
		return out[i].UpdatedAt > out[j].UpdatedAt
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (s *ChangesetStore) SetStackStatus(ctx context.Context, stackID, stackStatus string) error {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	stack := s.b.stacks[strings.TrimSpace(stackID)]
	if stack == nil {
		return storage.ErrNotFound
	}
	stackStatus = strings.TrimSpace(stackStatus)
	if stackStatus == "" {
		return storage.ErrInvalid
	}
	stack.Status = stackStatus
	stack.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	return nil
}

func (s *ChangesetStore) MoveStackEntry(ctx context.Context, req *corev1.MoveStackEntryRequest) (*corev1.ChangesetStack, error) {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	stack := s.b.stacks[strings.TrimSpace(req.StackId)]
	if stack == nil {
		return nil, storage.ErrNotFound
	}
	entry := stackEntryByChangeset(stack, strings.TrimSpace(req.ChangesetId))
	if entry == nil {
		return nil, storage.ErrNotFound
	}
	if cs := s.b.changesets[entry.ChangesetId]; cs == nil {
		return nil, storage.ErrNotFound
	} else if cs.Status == "submitted" {
		return nil, storage.ErrConflict
	}
	s.b.reorderStackSiblingLocked(stack, entry, req.SiblingOrder)
	s.b.recomputeStackDisplayLocked(stack)
	stack.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	return s.b.hydrateStackLocked(stack), nil
}

func (s *ChangesetStore) ReparentStackEntry(ctx context.Context, req *corev1.ReparentStackEntryRequest) (*corev1.ChangesetStack, error) {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	stack := s.b.stacks[strings.TrimSpace(req.StackId)]
	if stack == nil {
		return nil, storage.ErrNotFound
	}
	entry := stackEntryByChangeset(stack, strings.TrimSpace(req.ChangesetId))
	if entry == nil {
		return nil, storage.ErrNotFound
	}
	cs := s.b.changesets[entry.ChangesetId]
	if cs == nil {
		return nil, storage.ErrNotFound
	}
	if cs.Status == "submitted" {
		return nil, storage.ErrConflict
	}
	newParentID := strings.TrimSpace(req.NewParentChangesetId)
	newParentPatchsetID := strings.TrimSpace(req.NewParentPatchsetId)
	if newParentID == "" {
		if stack.RootEntryId != "" && stack.RootEntryId != entry.ChangesetId {
			return nil, storage.ErrConflict
		}
		newParentPatchsetID = ""
	} else {
		parentEntry := stackEntryByChangeset(stack, newParentID)
		parent := s.b.changesets[newParentID]
		if parentEntry == nil || parent == nil {
			return nil, storage.ErrInvalid
		}
		if s.b.stackEntryHasAncestorLocked(stack, newParentID, entry.ChangesetId) {
			return nil, storage.ErrConflict
		}
		if newParentPatchsetID == "" {
			newParentPatchsetID = parent.CurrentPatchsetId
		}
		if newParentPatchsetID == "" || newParentPatchsetID != parent.CurrentPatchsetId {
			return nil, storage.ErrConflict
		}
	}
	entry.ParentChangesetId = newParentID
	entry.ParentPatchsetId = newParentPatchsetID
	entry.State = "needs_restack"
	cs.ParentChangesetId = newParentID
	cs.ParentPatchsetId = newParentPatchsetID
	if newParentID == "" {
		cs.BaseKind = "commit"
		stack.RootEntryId = entry.ChangesetId
	} else {
		cs.BaseKind = "patchset"
	}
	s.b.markSubtreeStateLocked(stack, entry.ChangesetId, "needs_restack")
	s.b.reorderStackSiblingLocked(stack, entry, req.SiblingOrder)
	s.b.recomputeStackDisplayLocked(stack)
	stack.ActiveEntryId = entry.ChangesetId
	stack.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	return s.b.hydrateStackLocked(stack), nil
}

func (s *ChangesetStore) DetachStackEntry(ctx context.Context, subjectID string, req *corev1.DetachStackEntryRequest) (*corev1.DetachStackEntryResponse, error) {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	source := s.b.stacks[strings.TrimSpace(req.StackId)]
	if source == nil {
		return nil, storage.ErrNotFound
	}
	entry := stackEntryByChangeset(source, strings.TrimSpace(req.ChangesetId))
	if entry == nil {
		return nil, storage.ErrNotFound
	}
	if entry.ParentChangesetId == "" {
		return nil, storage.ErrInvalid
	}
	cs := s.b.changesets[entry.ChangesetId]
	if cs == nil {
		return nil, storage.ErrNotFound
	}
	if cs.Status == "submitted" {
		return nil, storage.ErrConflict
	}

	descendants := s.b.stackSubtreeIDsLocked(source, entry.ChangesetId)
	for id := range descendants {
		child := s.b.changesets[id]
		if child == nil {
			return nil, storage.ErrNotFound
		}
		if child.Status == "submitted" {
			return nil, storage.ErrConflict
		}
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	title := strings.TrimSpace(req.Title)
	if title == "" {
		title = strings.TrimSpace(cs.Title)
	}
	if title == "" {
		title = strings.TrimSpace(source.Title)
	}
	if title == "" {
		title = "Detached stack"
	}
	detached := &corev1.ChangesetStack{
		Id:             s.b.nextIDLocked("stk"),
		AuthoringSlice: cloneSliceRef(source.AuthoringSlice),
		TargetRef:      source.TargetRef,
		BaseCommitId:   source.BaseCommitId,
		Title:          title,
		Status:         "open",
		ActiveEntryId:  entry.ChangesetId,
		RootEntryId:    entry.ChangesetId,
		CreatedBy:      strings.TrimSpace(subjectID),
		CreatedAt:      now,
		UpdatedAt:      now,
	}

	remaining := make([]*corev1.ChangesetStackEntry, 0, len(source.Entries)-len(descendants))
	for _, candidate := range source.Entries {
		if candidate == nil {
			continue
		}
		if _, ok := descendants[candidate.ChangesetId]; ok {
			next := cloneStackEntry(candidate)
			next.StackId = detached.Id
			next.State = "needs_restack"
			if next.ChangesetId == entry.ChangesetId {
				next.ParentChangesetId = ""
				next.ParentPatchsetId = ""
				next.SiblingOrder = 1
			}
			detached.Entries = append(detached.Entries, next)
			if child := s.b.changesets[next.ChangesetId]; child != nil {
				child.StackId = detached.Id
				child.ParentChangesetId = next.ParentChangesetId
				child.ParentPatchsetId = next.ParentPatchsetId
				if next.ChangesetId == entry.ChangesetId {
					child.BaseKind = "commit"
				}
			}
			continue
		}
		remaining = append(remaining, candidate)
	}
	source.Entries = remaining
	if _, ok := descendants[source.ActiveEntryId]; ok {
		source.ActiveEntryId = source.RootEntryId
	}
	source.UpdatedAt = now
	s.b.stacks[detached.Id] = detached
	s.b.recomputeStackDisplayLocked(source)
	s.b.recomputeStackDisplayLocked(detached)
	return &corev1.DetachStackEntryResponse{
		SourceStack:   s.b.hydrateStackLocked(source),
		DetachedStack: s.b.hydrateStackLocked(detached),
	}, nil
}

func (s *ChangesetStore) Create(ctx context.Context, subjectID string, req *corev1.CreateChangesetRequest) (*corev1.Changeset, error) {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	if req.AuthoringSlice == nil {
		return nil, storage.ErrInvalid
	}
	sliceID := s.b.sliceRefs[sliceRefKey(req.AuthoringSlice)]
	if sliceID == "" {
		return nil, storage.ErrNotFound
	}
	targetRef := req.TargetRef
	if targetRef == "" {
		targetRef = storage.DefaultTargetRef
	}
	baseCommitID := req.BaseCommitId
	if baseCommitID == "" {
		ref := s.b.refs[targetRef]
		if ref == nil {
			return nil, storage.ErrNotFound
		}
		baseCommitID = ref.CommitId
	}
	id := s.b.nextIDLocked("cs")
	number := s.b.nextChangesetNumberLocked(sliceID)
	ref := cloneSliceRef(req.AuthoringSlice)
	cs := &corev1.Changeset{
		Id:             id,
		AuthoringSlice: ref,
		Author:         subjectID,
		TargetRef:      targetRef,
		BaseCommitId:   baseCommitID,
		Status:         "draft",
		Title:          req.Title,
		Number:         number,
	}
	if req.StackId != "" {
		if err := s.b.attachChangesetToStackLocked(cs, req.StackId, req.ParentChangesetId, req.ParentPatchsetId); err != nil {
			return nil, err
		}
	}
	storage.PopulateChangesetHandles(cs)
	s.b.changesets[id] = cloneChangeset(cs)
	return cloneChangeset(cs), nil
}

func (s *ChangesetStore) Get(ctx context.Context, changesetID string) (*corev1.Changeset, error) {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	if resolved := s.b.resolveChangesetSelectorLocked(changesetID); resolved != "" {
		changesetID = resolved
	}
	cs := s.b.changesets[changesetID]
	if cs == nil {
		return nil, storage.ErrNotFound
	}
	out := cloneChangeset(cs)
	storage.PopulateChangesetHandles(out)
	return out, nil
}

func (s *ChangesetStore) List(ctx context.Context, req *corev1.ListChangesetsRequest) ([]*corev1.Changeset, error) {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	var out []*corev1.Changeset
	for _, cs := range s.b.changesets {
		if req.AuthoringSlice != nil && !sameSliceRef(cs.AuthoringSlice, req.AuthoringSlice) {
			continue
		}
		if req.Status != "" && cs.Status != req.Status {
			continue
		}
		clone := cloneChangeset(cs)
		storage.PopulateChangesetHandles(clone)
		out = append(out, clone)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Id < out[j].Id })
	return out, nil
}

func (s *ChangesetStore) AddPatchset(ctx context.Context, changesetID, expectedCurrentPatchsetID string, patchset *corev1.Patchset) (*corev1.Patchset, error) {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	cs := s.b.changesets[changesetID]
	if cs == nil {
		return nil, storage.ErrNotFound
	}
	if expectedCurrentPatchsetID != "" && cs.CurrentPatchsetId != expectedCurrentPatchsetID {
		return nil, storage.ErrConflict
	}
	next := clonePatchset(patchset)
	if next.BaseKind == "" {
		next.BaseKind = "commit"
	}
	if next.BaseKind == "patchset" && next.BasePatchsetId == "" {
		next.BasePatchsetId = next.StackParentPatchsetId
	}
	if next.StackParentPatchsetId == "" && next.BaseKind == "patchset" {
		next.StackParentPatchsetId = next.BasePatchsetId
	}
	baseTreeID, err := s.b.baseTreeForPatchsetLocked(next)
	if err != nil {
		return nil, err
	}
	next.BaseTreeId = baseTreeID
	resultTreeID, err := s.b.previewTreeForPatchsetLocked(baseTreeID, next.FileEdits)
	if err != nil {
		return nil, err
	}
	next.ResultTreeId = resultTreeID
	next.Id = s.b.nextIDLocked("ps")
	next.ChangesetId = changesetID
	next.Number = int64(len(cs.Patchsets) + 1)
	if next.SubmitRequirements == nil || next.SubmitRequirements.SourceSliceDefinitionHash == "" {
		sliceID := s.b.sliceRefs[sliceRefKey(cs.AuthoringSlice)]
		slice := s.b.slices[sliceID]
		next.SubmitRequirements = submitRequirementsForMemorySlice(slice)
	}
	cs.Patchsets = append(cs.Patchsets, next)
	cs.CurrentPatchsetId = next.Id
	cs.CurrentPatchsetNumber = next.Number
	cs.SubmitBlockedReason = ""
	if cs.StackId != "" {
		if stack := s.b.stacks[cs.StackId]; stack != nil {
			if entry := stackEntryByChangeset(stack, cs.Id); entry != nil {
				if next.BaseKind == "patchset" {
					entry.ParentPatchsetId = next.BasePatchsetId
					cs.ParentPatchsetId = next.BasePatchsetId
				}
				entry.State = "draft"
				stack.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
			}
		}
		s.b.markChildrenStaleLocked(cs.StackId, cs.Id, next.Id)
	}
	return clonePatchset(next), nil
}

func (s *ChangesetStore) Approve(ctx context.Context, changesetID, subjectID string) (*corev1.ApproveChangesetResponse, error) {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	if resolved := s.b.resolveChangesetSelectorLocked(changesetID); resolved != "" {
		changesetID = resolved
	}
	cs := s.b.changesets[changesetID]
	if cs == nil {
		return nil, storage.ErrNotFound
	}
	patchset := currentPatchset(cs)
	if patchset == nil {
		return nil, storage.ErrConflict
	}
	if cs.Status == "abandoned" || cs.Status == "submitted" {
		return nil, storage.ErrConflict
	}
	key := patchsetRequirementKey(changesetID, patchset.Id)
	if s.b.approvals[key] == nil {
		s.b.approvals[key] = map[string]struct{}{}
	}
	s.b.approvals[key][strings.TrimSpace(subjectID)] = struct{}{}
	return &corev1.ApproveChangesetResponse{ChangesetId: changesetID, PatchsetId: patchset.Id, SubjectId: strings.TrimSpace(subjectID)}, nil
}

func (s *ChangesetStore) ReportCheckResult(ctx context.Context, changesetID, subjectID, checkName, resultStatus string) (*corev1.ReportCheckResultResponse, error) {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	if resolved := s.b.resolveChangesetSelectorLocked(changesetID); resolved != "" {
		changesetID = resolved
	}
	cs := s.b.changesets[changesetID]
	if cs == nil {
		return nil, storage.ErrNotFound
	}
	patchset := currentPatchset(cs)
	if patchset == nil {
		return nil, storage.ErrConflict
	}
	if cs.Status == "abandoned" || cs.Status == "submitted" {
		return nil, storage.ErrConflict
	}
	checkName = strings.TrimSpace(checkName)
	resultStatus, ok := storage.NormalizeCheckStatus(resultStatus)
	if checkName == "" || !ok {
		return nil, storage.ErrInvalid
	}
	key := patchsetRequirementKey(changesetID, patchset.Id)
	if s.b.checkResults[key] == nil {
		s.b.checkResults[key] = map[string]string{}
	}
	s.b.checkResults[key][checkName] = resultStatus
	return &corev1.ReportCheckResultResponse{ChangesetId: changesetID, PatchsetId: patchset.Id, CheckName: checkName, Status: resultStatus}, nil
}

func (s *ChangesetStore) Submit(ctx context.Context, changesetID, expectedCurrentPatchsetID string) (res *corev1.SubmitChangesetResponse, err error) {
	defer func() {
		storage.RecordSubmitResult(err)
	}()
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	cs := s.b.changesets[changesetID]
	if cs == nil {
		return nil, storage.ErrNotFound
	}
	if expectedCurrentPatchsetID != "" && cs.CurrentPatchsetId != expectedCurrentPatchsetID {
		return nil, storage.ErrConflict
	}
	if cs.Status == "pending_publish" {
		return &corev1.SubmitChangesetResponse{TargetRef: cs.TargetRef, Status: cs.Status, PendingPublishId: cs.Id}, nil
	}
	patchset := currentPatchset(cs)
	if patchset == nil {
		return nil, storage.ErrConflict
	}
	if cs.ParentChangesetId != "" {
		parent := s.b.changesets[cs.ParentChangesetId]
		if parent == nil {
			return nil, storage.ErrNotFound
		}
		if parent.Status != "submitted" && parent.Status != "pending_publish" {
			return nil, s.b.blockSubmitLocked(cs, "BlockedOnBaseChangeset")
		}
	}
	if len(patchset.Conflicts) > 0 {
		return nil, fmt.Errorf("%w: unresolved patchset conflicts", storage.ErrConflict)
	}
	sliceID := s.b.sliceRefs[sliceRefKey(cs.AuthoringSlice)]
	slice := s.b.slices[sliceID]
	latestReq := submitRequirementsForMemorySlice(slice)
	for _, p := range patchset.ChangedPaths {
		if slice == nil || slice.Definition == nil || !pathInAnyPrefix(slice.Definition.IncludedPaths, p) {
			return nil, s.b.blockSubmitLocked(cs, fmt.Sprintf("changed path %s is outside latest slice definition, refresh the changeset", p))
		}
	}
	recordedHash := ""
	if patchset.SubmitRequirements != nil {
		recordedHash = patchset.SubmitRequirements.SourceSliceDefinitionHash
	}
	if recordedHash == "" {
		recordedHash = latestReq.SourceSliceDefinitionHash
	}
	if recordedHash != latestReq.SourceSliceDefinitionHash {
		return nil, s.b.blockSubmitLocked(cs, "requirements changed, refresh the changeset")
	}
	key := patchsetRequirementKey(changesetID, patchset.Id)
	approvalSubjects := make([]string, 0, len(s.b.approvals[key]))
	for subjectID := range s.b.approvals[key] {
		approvalSubjects = append(approvalSubjects, subjectID)
	}
	if reason := storage.EvaluateSubmitRequirements(latestReq, cs.Author, approvalSubjects, s.b.checkResults[key]); reason != "" {
		return nil, s.b.blockSubmitLocked(cs, reason)
	}
	cs.Status = "pending_publish"
	cs.SubmitBlockedReason = ""
	s.b.pendingAcceptedAt[cs.Id] = time.Now()
	s.b.nextPendingSeq++
	s.b.pendingSequence[cs.Id] = s.b.nextPendingSeq
	return &corev1.SubmitChangesetResponse{TargetRef: cs.TargetRef, Status: cs.Status, PendingPublishId: cs.Id}, nil
}

func (s *ChangesetStore) PublishPending(ctx context.Context, limit int) (published int, err error) {
	defer func() {
		storage.RecordPublishBatch(published, err)
	}()
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	if limit <= 0 {
		limit = 128
	}
	for _, cs := range s.b.pendingChangesetsInSequenceLocked() {
		if published >= limit {
			break
		}
		if cs.Status != "pending_publish" {
			continue
		}
		patchset := currentPatchset(cs)
		if patchset == nil {
			continue
		}
		ref := s.b.refs[cs.TargetRef]
		if ref == nil {
			return published, storage.ErrNotFound
		}
		baseFiles := cloneFileMap(s.b.commitFiles[ref.CommitId])
		baseDirs := cloneDirSet(s.b.commitDirs[ref.CommitId])
		for _, edit := range patchset.FileEdits {
			switch edit.Op {
			case "delete":
				delete(baseFiles, edit.Path)
				delete(baseDirs, edit.Path)
			case "rename":
				file, ok := baseFiles[edit.OldPath]
				if ok {
					delete(baseFiles, edit.OldPath)
					file.Path = edit.Path
					baseFiles[edit.Path] = file
				}
				if _, ok := baseDirs[edit.OldPath]; ok {
					delete(baseDirs, edit.OldPath)
					baseDirs[edit.Path] = struct{}{}
				}
			case "mkdir":
				baseDirs[edit.Path] = struct{}{}
			default:
				blob := s.b.blobs[edit.BlobId]
				if blob == nil && edit.ContentHash == "" {
					return published, storage.ErrNotFound
				}
				contentHash := edit.ContentHash
				size := int64(0)
				if blob != nil {
					contentHash = blob.ContentHash
					size = blob.Size
				}
				baseFiles[edit.Path] = storage.FileEntry{Path: edit.Path, BlobID: edit.BlobId, ContentHash: contentHash, Mode: edit.Mode, Size: size}
			}
		}
		files := make([]storage.FileEntry, 0, len(baseFiles))
		for _, file := range baseFiles {
			files = append(files, file)
		}
		commitID := s.b.nextIDLocked("commit")
		s.b.putCommitWithFilesAndDirsLocked(commitID, files, baseDirs, patchset.ChangedPaths, cs.Title)
		cs.Status = "submitted"
		cs.CommitId = commitID
		if acceptedAt, ok := s.b.pendingAcceptedAt[cs.Id]; ok {
			storage.ObservePublishLatency(time.Since(acceptedAt))
			delete(s.b.pendingAcceptedAt, cs.Id)
		}
		delete(s.b.pendingSequence, cs.Id)
		published++
		s.b.refreshStackStatusLocked(cs.StackId)
	}
	return published, nil
}

func (s *ChangesetStore) PendingPublishDepth(ctx context.Context) (int, error) {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	depth := 0
	for _, cs := range s.b.changesets {
		if cs != nil && cs.Status == "pending_publish" {
			depth++
		}
	}
	return depth, nil
}

func (s *ChangesetStore) Abandon(ctx context.Context, changesetID string) error {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	cs := s.b.changesets[changesetID]
	if cs == nil {
		return storage.ErrNotFound
	}
	if cs.Status == "submitted" {
		return storage.ErrConflict
	}
	for _, child := range s.b.changesets {
		if child != nil && child.ParentChangesetId == changesetID && child.Status != "submitted" && child.Status != "abandoned" {
			return storage.ErrConflict
		}
	}
	cs.Status = "abandoned"
	s.b.refreshStackStatusLocked(cs.StackId)
	return nil
}

func (s *RepositoryStore) GetRef(ctx context.Context, name string) (*corev1.Ref, error) {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	if name == "" {
		name = storage.DefaultTargetRef
	}
	ref := s.b.refs[name]
	if ref == nil {
		return nil, storage.ErrNotFound
	}
	return cloneRef(ref), nil
}

func (s *RepositoryStore) RootTreeForCommit(ctx context.Context, commitID string) (string, error) {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	commit := s.b.commits[commitID]
	if commit == nil {
		return "", storage.ErrNotFound
	}
	return commit.RootTreeId, nil
}

func (s *RepositoryStore) GetFileAtTree(ctx context.Context, rootTreeID, p string) (*storage.FileEntry, error) {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	files, _, ok := s.b.filesAndDirsForRootTreeLocked(rootTreeID)
	if !ok {
		return nil, storage.ErrNotFound
	}
	file, ok := files[p]
	if !ok {
		return nil, storage.ErrNotFound
	}
	return cloneFile(file), nil
}

func (s *RepositoryStore) GetEntryAtTree(ctx context.Context, rootTreeID, p string) (*storage.TreeEntry, error) {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	files, dirs, ok := s.b.filesAndDirsForRootTreeLocked(rootTreeID)
	if !ok {
		return nil, storage.ErrNotFound
	}
	return treeEntryFromMemorySnapshot(files, dirs, p)
}

func (s *RepositoryStore) ListDirectoryAtTree(ctx context.Context, rootTreeID, p string) ([]storage.TreeEntry, error) {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	files, dirs, ok := s.b.filesAndDirsForRootTreeLocked(rootTreeID)
	if !ok {
		return nil, storage.ErrNotFound
	}
	entries := directoryEntries(p, files, dirs)
	if len(entries) == 0 && p != "/" && !hasDescendant(files, p) && !hasDescendantDir(dirs, p) {
		if _, ok := dirs[p]; ok {
			return nil, nil
		}
		return nil, storage.ErrNotFound
	}
	return entries, nil
}

func (s *RepositoryStore) GetOrCreateGitImport(ctx context.Context, subjectID, source, mountPath string, sliceRef *corev1.SliceRef, sliceID, targetRef, mode string, totalCommits int) (*storage.GitImportRecord, error) {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	key := gitImportKey(source, mountPath, sliceID, targetRef, mode)
	if id := s.b.importsByKey[key]; id != "" {
		record := *s.b.imports[id]
		record.Status = "running"
		record.TotalCommits = totalCommits
		s.b.imports[id] = &record
		return cloneGitImport(&record), nil
	}
	record := &storage.GitImportRecord{
		ID:               s.b.nextIDLocked("gimp"),
		SubjectID:        subjectID,
		Source:           source,
		MountPath:        mountPath,
		AuthoringAccount: sliceRef.Account,
		AuthoringSlice:   sliceRef.Slice,
		AuthoringSliceID: sliceID,
		TargetRef:        targetRef,
		Mode:             mode,
		Status:           "running",
		TotalCommits:     totalCommits,
	}
	s.b.imports[record.ID] = cloneGitImport(record)
	s.b.importsByKey[key] = record.ID
	return cloneGitImport(record), nil
}

func (s *RepositoryStore) GetGitImport(ctx context.Context, source, mountPath, sliceID, targetRef, mode string) (*storage.GitImportRecord, error) {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	id := s.b.importsByKey[gitImportKey(source, mountPath, sliceID, targetRef, mode)]
	if id == "" {
		return nil, storage.ErrNotFound
	}
	return cloneGitImport(s.b.imports[id]), nil
}

func (s *RepositoryStore) ListGitImportCommits(ctx context.Context, importID string) ([]storage.GitImportedCommitRecord, error) {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	return append([]storage.GitImportedCommitRecord(nil), s.b.importedCommits[importID]...), nil
}

func (s *RepositoryStore) RecordGitImportCommit(ctx context.Context, importID, gitCommitID, nativeCommitID, message string, position, changedPathCount int) error {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	if s.b.imports[importID] == nil {
		return storage.ErrNotFound
	}
	s.b.importedCommits[importID] = append(s.b.importedCommits[importID], storage.GitImportedCommitRecord{
		ImportID: importID, GitCommitID: gitCommitID, NativeCommitID: nativeCommitID,
		Message: message, Position: position, ChangedPathCount: changedPathCount,
	})
	return nil
}

func (s *RepositoryStore) CompleteGitImport(ctx context.Context, importID, finalNativeCommitID string) error {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	record := s.b.imports[importID]
	if record == nil {
		return storage.ErrNotFound
	}
	record.Status = "completed"
	record.FinalNativeCommitID = finalNativeCommitID
	record.ImportedCount = len(s.b.importedCommits[importID])
	return nil
}

func (s *RepositoryStore) GetCommit(ctx context.Context, commitID string) (*corev1.Commit, error) {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	commit := s.b.commits[commitID]
	if commit == nil {
		return nil, storage.ErrNotFound
	}
	return cloneCommit(commit), nil
}

func (s *RepositoryStore) ResolveCommitCandidates(ctx context.Context, filter storage.CommitResolveFilter) ([]*corev1.Commit, error) {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	idPrefix := strings.TrimSpace(filter.IDPrefix)
	if idPrefix == "" {
		return nil, storage.ErrInvalid
	}
	limit := filter.Limit
	if limit <= 0 || limit > 20 {
		limit = 2
	}
	prefixes := normalizePathPrefixes(filter.PathPrefixes)
	refSet := map[string]struct{}{}
	for _, ref := range filter.EntityRefs {
		ref.AccountID = strings.TrimSpace(ref.AccountID)
		ref.EntityID = strings.TrimSpace(ref.EntityID)
		if ref.AccountID == "" || ref.EntityID == "" {
			continue
		}
		refSet[ref.AccountID+"\x00"+ref.EntityID] = struct{}{}
	}
	commits := make([]*corev1.Commit, 0, limit)
	for _, commit := range s.b.commits {
		if commit.Id == "mem_root" || !strings.HasPrefix(commit.Id, idPrefix) {
			continue
		}
		if !commitMatchesResolveFilter(s.b, commit, prefixes, refSet, filter.IncludePrefixesWithEntities) {
			continue
		}
		commits = append(commits, cloneCommit(commit))
	}
	sortCommits(commits)
	if len(commits) > limit {
		commits = commits[:limit]
	}
	return commits, nil
}

func (s *RepositoryStore) ListCommits(ctx context.Context, refName string, limit int) ([]*corev1.Commit, error) {
	page, err := s.ListCommitPage(ctx, refName, limit, "")
	if err != nil {
		return nil, err
	}
	return page.Commits, nil
}

func (s *RepositoryStore) ListCommitPage(ctx context.Context, refName string, limit int, pageToken string) (*storage.CommitListPage, error) {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	return s.b.listCommitPageLocked(limit, func(commit *corev1.Commit) bool { return commit.Id != "mem_root" }), nil
}

func (s *RepositoryStore) ListCommitsByPathPrefixes(ctx context.Context, refName string, prefixes []string, limit int) ([]*corev1.Commit, error) {
	page, err := s.ListCommitPageByPathPrefixes(ctx, refName, prefixes, limit, "")
	if err != nil {
		return nil, err
	}
	return page.Commits, nil
}

func (s *RepositoryStore) ListCommitPageByPathPrefixes(ctx context.Context, refName string, prefixes []string, limit int, pageToken string) (*storage.CommitListPage, error) {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	return s.b.listCommitPageLocked(limit, func(commit *corev1.Commit) bool {
		for _, changed := range commit.ChangedPaths {
			for _, prefix := range prefixes {
				if pathContains(prefix, changed) {
					return true
				}
			}
		}
		return false
	}), nil
}

func (s *RepositoryStore) ListCommitPageByEntityRefs(ctx context.Context, refName string, refs []storage.HistoryEntityRef, limit int, pageToken string) (*storage.CommitListPage, error) {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	refSet := map[string]struct{}{}
	for _, ref := range refs {
		refSet[ref.AccountID+"\x00"+ref.EntityID] = struct{}{}
	}
	return s.b.listCommitPageLocked(limit, func(commit *corev1.Commit) bool {
		for _, changed := range commit.ChangedPaths {
			for _, ref := range s.b.entityChanges[changed] {
				if _, ok := refSet[ref.AccountID+"\x00"+ref.EntityID]; ok {
					return true
				}
			}
		}
		return false
	}), nil
}

func (s *RepositoryStore) ListCommitPageByEntityRefsOrPathPrefixes(ctx context.Context, refName string, refs []storage.HistoryEntityRef, prefixes []string, limit int, pageToken string) (*storage.CommitListPage, error) {
	entityPage, err := s.ListCommitPageByEntityRefs(ctx, refName, refs, 0, "")
	if err != nil {
		return nil, err
	}
	pathPage, err := s.ListCommitPageByPathPrefixes(ctx, refName, prefixes, 0, "")
	if err != nil {
		return nil, err
	}
	seen := map[string]*corev1.Commit{}
	for _, commit := range entityPage.Commits {
		seen[commit.Id] = commit
	}
	for _, commit := range pathPage.Commits {
		seen[commit.Id] = commit
	}
	var commits []*corev1.Commit
	for _, commit := range seen {
		commits = append(commits, commit)
	}
	sortCommits(commits)
	if limit <= 0 {
		limit = 50
	}
	if len(commits) > limit {
		commits = commits[:limit]
	}
	return &storage.CommitListPage{Commits: commits}, nil
}

func (s *RepositoryStore) CurrentPathEntitiesByPrefixes(ctx context.Context, refName string, prefixes []string) ([]storage.CurrentPathEntity, error) {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	var out []storage.CurrentPathEntity
	for _, entity := range s.b.entitiesByPath {
		for _, prefix := range prefixes {
			if pathContains(prefix, entity.Path) {
				out = append(out, entity)
				break
			}
		}
	}
	return out, nil
}

func (s *RepositoryStore) CurrentPathEntitiesByPaths(ctx context.Context, refName string, in []string) ([]storage.CurrentPathEntity, error) {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	var out []storage.CurrentPathEntity
	for _, p := range in {
		if entity, ok := s.b.entitiesByPath[p]; ok {
			out = append(out, entity)
		}
	}
	return out, nil
}

func (s *RepositoryStore) GetFile(ctx context.Context, commitID, p string) (*storage.FileEntry, error) {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	file, ok := s.b.commitFiles[commitID][p]
	if !ok {
		return nil, storage.ErrNotFound
	}
	return cloneFile(file), nil
}

func (s *RepositoryStore) GetEntry(ctx context.Context, commitID, p string) (*storage.TreeEntry, error) {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	files := s.b.commitFiles[commitID]
	if files == nil {
		return nil, storage.ErrNotFound
	}
	dirs := s.b.commitDirs[commitID]
	if file, ok := files[p]; ok {
		return &storage.TreeEntry{Path: file.Path, Name: path.Base(file.Path), Kind: "file", Mode: file.Mode, BlobID: file.BlobID, ContentHash: file.ContentHash, Size: file.Size}, nil
	}
	if _, ok := dirs[p]; ok {
		return &storage.TreeEntry{Path: p, Name: path.Base(p), Kind: "directory", TreeID: "mem_tree_" + strings.Trim(p, "/")}, nil
	}
	if p == "/" || hasDescendant(files, p) || hasDescendantDir(dirs, p) {
		return &storage.TreeEntry{Path: p, Name: path.Base(p), Kind: "directory", TreeID: "mem_tree_" + strings.Trim(p, "/")}, nil
	}
	return nil, storage.ErrNotFound
}

func (s *RepositoryStore) ListDirectory(ctx context.Context, commitID, p string) ([]storage.TreeEntry, error) {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	files := s.b.commitFiles[commitID]
	if files == nil {
		return nil, storage.ErrNotFound
	}
	dirs := s.b.commitDirs[commitID]
	entries := directoryEntries(p, files, dirs)
	if len(entries) == 0 && p != "/" && !hasDescendant(files, p) && !hasDescendantDir(dirs, p) {
		if _, ok := dirs[p]; ok {
			return nil, nil
		}
		return nil, storage.ErrNotFound
	}
	return entries, nil
}

func (s *RepositoryStore) ListFiles(ctx context.Context, commitID, prefix string) ([]storage.FileEntry, error) {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	files := s.b.commitFiles[commitID]
	if files == nil {
		return nil, storage.ErrNotFound
	}
	var out []storage.FileEntry
	for _, file := range files {
		if pathContains(prefix, file.Path) {
			out = append(out, file)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, nil
}

func (s *SliceStore) Create(ctx context.Context, subjectID string, ref *corev1.SliceRef, includedPaths []string, visibility string, requiredApprovals int32, requiredChecks []string) (*corev1.Slice, error) {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	if _, ok := s.b.sliceRefs[sliceRefKey(ref)]; ok {
		return nil, storage.ErrConflict
	}
	return s.b.putSliceLocked(ref, includedPaths, visibility, requiredApprovals, requiredChecks, subjectID)
}

func (s *SliceStore) ValidateDefinition(ref *corev1.SliceRef, includedPaths []string, visibility string, requiredApprovals int32, requiredChecks []string) ([]string, string, int32, []string, error) {
	return validateSliceDefinition(ref, includedPaths, visibility, requiredApprovals, requiredChecks)
}

func (s *SliceStore) Resolve(ctx context.Context, ref *corev1.SliceRef) (*corev1.Slice, error) {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	id := s.b.sliceRefs[sliceRefKey(ref)]
	if id == "" {
		return nil, storage.ErrNotFound
	}
	return cloneSlice(s.b.slices[id]), nil
}

func (s *SliceStore) Get(ctx context.Context, sliceID string) (*corev1.Slice, error) {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	slice := s.b.slices[sliceID]
	if slice == nil {
		return nil, storage.ErrNotFound
	}
	return cloneSlice(slice), nil
}

func (s *SliceStore) List(ctx context.Context, account string, limit int) ([]*corev1.Slice, error) {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	if limit <= 0 {
		limit = 50
	}
	var out []*corev1.Slice
	for _, slice := range s.b.slices {
		if slice.Ref.Account == account {
			out = append(out, cloneSlice(slice))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Ref.Slice < out[j].Ref.Slice })
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (s *SliceStore) ListDefinitionVersions(ctx context.Context, sliceID string, limit int) ([]*corev1.SliceDefinitionVersion, error) {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	if s.b.slices[sliceID] == nil {
		return nil, storage.ErrNotFound
	}
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	versions := s.b.sliceDefinitionVersions[sliceID]
	out := make([]*corev1.SliceDefinitionVersion, 0, len(versions))
	for i := len(versions) - 1; i >= 0 && len(out) < limit; i-- {
		out = append(out, cloneSliceDefinitionVersion(versions[i]))
	}
	return out, nil
}

func (s *SliceStore) UpdateDefinition(ctx context.Context, subjectID, sliceID, expectedHash string, definition *corev1.SliceDefinition) (*corev1.SliceDefinition, error) {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	current := s.b.slices[sliceID]
	if current == nil {
		return nil, storage.ErrNotFound
	}
	if expectedHash != "" && current.DefinitionHash != expectedHash {
		return nil, storage.ErrConflict
	}
	included, visibility, requiredApprovals, requiredChecks, err := validateSliceDefinition(current.Ref, definition.IncludedPaths, definition.Visibility, definition.RequiredApprovals, definition.RequiredChecks)
	if err != nil {
		return nil, err
	}
	current.Definition.Version++
	current.Definition.IncludedPaths = included
	current.Definition.Visibility = visibility
	current.Definition.RequiredApprovals = requiredApprovals
	current.Definition.RequiredChecks = append([]string(nil), requiredChecks...)
	current.DefinitionHash = memoryDefinitionHash(sliceID, current.Definition.Version, included, visibility, requiredApprovals, requiredChecks)
	s.b.appendSliceDefinitionVersionLocked(current, subjectID)
	return cloneSlice(current).Definition, nil
}

func (s *SliceStore) Delete(ctx context.Context, sliceID string) error {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	slice := s.b.slices[sliceID]
	if slice == nil {
		return storage.ErrNotFound
	}
	delete(s.b.sliceRefs, sliceRefKey(slice.Ref))
	delete(s.b.slices, sliceID)
	delete(s.b.sliceDefinitionVersions, sliceID)
	return nil
}

func (s *SliceStore) CoveringIDsByPath(ctx context.Context, changedPaths []string) (map[string][]string, error) {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	byPrefix := map[string][]string{}
	for _, slice := range s.b.slices {
		for _, prefix := range slice.Definition.IncludedPaths {
			byPrefix[prefix] = append(byPrefix[prefix], slice.Id)
		}
	}
	return storage.AssembleCoverageByPath(changedPaths, byPrefix), nil
}

func (b *backend) listCommitPageLocked(limit int, include func(*corev1.Commit) bool) *storage.CommitListPage {
	if limit <= 0 {
		limit = 50
	}
	var commits []*corev1.Commit
	for _, commit := range b.commits {
		if include(commit) {
			commits = append(commits, cloneCommit(commit))
		}
	}
	sortCommits(commits)
	if len(commits) > limit {
		commits = commits[:limit]
	}
	return &storage.CommitListPage{Commits: commits}
}

func validateSliceDefinition(ref *corev1.SliceRef, includedPaths []string, visibility string, requiredApprovals int32, requiredChecks []string) ([]string, string, int32, []string, error) {
	ref, err := normalizeSliceRef(ref)
	if err != nil {
		return nil, "", 0, nil, err
	}
	visibility = strings.TrimSpace(visibility)
	if visibility == "" {
		visibility = "private"
	}
	if visibility != "private" && visibility != "public" {
		return nil, "", 0, nil, storage.ErrInvalid
	}
	seen := map[string]struct{}{}
	out := make([]string, 0, len(includedPaths))
	for _, raw := range includedPaths {
		cleaned, err := canonicalIncludedPath(ref, raw)
		if err != nil {
			return nil, "", 0, nil, err
		}
		if _, ok := seen[cleaned]; ok {
			continue
		}
		seen[cleaned] = struct{}{}
		out = append(out, cleaned)
	}
	if len(out) == 0 {
		return nil, "", 0, nil, storage.ErrInvalid
	}
	if requiredApprovals < 0 {
		return nil, "", 0, nil, storage.ErrInvalid
	}
	checks := make([]string, 0, len(requiredChecks))
	checkSeen := map[string]struct{}{}
	for _, raw := range requiredChecks {
		check := strings.TrimSpace(raw)
		if check == "" || strings.Contains(check, ",") {
			return nil, "", 0, nil, storage.ErrInvalid
		}
		if _, ok := checkSeen[check]; ok {
			continue
		}
		checkSeen[check] = struct{}{}
		checks = append(checks, check)
	}
	return out, visibility, requiredApprovals, checks, nil
}

func canonicalIncludedPath(ref *corev1.SliceRef, raw string) (string, error) {
	cleaned, err := paths.Canonical(raw)
	if err != nil {
		value := strings.TrimSpace(strings.ReplaceAll(raw, "\\", "/"))
		if !strings.HasPrefix(value, "/") {
			value = "/" + value
		}
		value = path.Clean(value)
		if ref.Slice != "home" || strings.Count(strings.Trim(value, "/"), "/") != 0 {
			return "", storage.ErrInvalid
		}
		cleaned = value
	}
	segments := strings.Split(strings.Trim(cleaned, "/"), "/")
	if len(segments) == 0 || segments[0] != ref.Account {
		return "", storage.ErrInvalid
	}
	if len(segments) == 1 && ref.Slice != "home" {
		return "", storage.ErrInvalid
	}
	return cleaned, nil
}

func normalizeSliceRef(ref *corev1.SliceRef) (*corev1.SliceRef, error) {
	if ref == nil {
		return nil, storage.ErrInvalid
	}
	account := normalizeSlug(ref.Account)
	slice := normalizeSlug(ref.Slice)
	if account == "" || slice == "" {
		return nil, storage.ErrInvalid
	}
	return &corev1.SliceRef{Account: account, Slice: slice}, nil
}

func normalizeSlug(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	value = strings.ReplaceAll(value, "_", "-")
	if value == "" || len(value) > 63 || strings.HasPrefix(value, "-") || strings.HasSuffix(value, "-") {
		return ""
	}
	for _, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
			continue
		}
		return ""
	}
	return value
}

func normalizeSignupUsername(username string) (string, error) {
	username = strings.ToLower(strings.TrimSpace(username))
	username = strings.ReplaceAll(username, "_", "-")
	if username == "" {
		return "", fmt.Errorf("username is required")
	}
	if len(username) > 63 {
		return "", fmt.Errorf("username must be 63 characters or fewer")
	}
	if strings.HasPrefix(username, "-") || strings.HasSuffix(username, "-") {
		return "", fmt.Errorf("username must not start or end with '-'")
	}
	for _, r := range username {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
			continue
		}
		return "", fmt.Errorf("username may contain only letters, numbers, '-' or '_'")
	}
	return username, nil
}

func sliceID(account, slice string) string { return "slice_" + account + "_" + slice }

func sliceRefKey(ref *corev1.SliceRef) string {
	if ref == nil {
		return ""
	}
	return normalizeSlug(ref.Account) + "/" + normalizeSlug(ref.Slice)
}

func sameSliceRef(a, b *corev1.SliceRef) bool { return sliceRefKey(a) == sliceRefKey(b) }

func accountFromPath(p string) string {
	trimmed := strings.Trim(p, "/")
	if trimmed == "" {
		return ""
	}
	return strings.Split(trimmed, "/")[0]
}

func gitImportKey(source, mountPath, sliceID, targetRef, mode string) string {
	return source + "\x00" + mountPath + "\x00" + sliceID + "\x00" + targetRef + "\x00" + mode
}

func pathContains(prefix, p string) bool {
	if prefix == "/" {
		return strings.HasPrefix(p, "/")
	}
	prefix = strings.TrimRight(prefix, "/")
	p = strings.TrimRight(p, "/")
	return p == prefix || strings.HasPrefix(p, prefix+"/")
}

func pathInAnyPrefix(prefixes []string, p string) bool {
	for _, prefix := range prefixes {
		if pathContains(prefix, p) {
			return true
		}
	}
	return false
}

func patchsetRequirementKey(changesetID, patchsetID string) string {
	return changesetID + "\x00" + patchsetID
}

func (b *backend) hydrateStackLocked(stack *corev1.ChangesetStack) *corev1.ChangesetStack {
	out := cloneStack(stack)
	sort.Slice(out.Entries, func(i, j int) bool {
		if out.Entries[i].DisplayOrder == out.Entries[j].DisplayOrder {
			return out.Entries[i].ChangesetId < out.Entries[j].ChangesetId
		}
		return out.Entries[i].DisplayOrder < out.Entries[j].DisplayOrder
	})
	for _, entry := range out.Entries {
		if cs := b.changesets[entry.ChangesetId]; cs != nil {
			entry.Changeset = cloneChangeset(cs)
			storage.PopulateChangesetHandles(entry.Changeset)
		}
	}
	return out
}

func (b *backend) attachChangesetToStackLocked(cs *corev1.Changeset, stackID, parentChangesetID, parentPatchsetID string) error {
	stack := b.stacks[strings.TrimSpace(stackID)]
	if stack == nil {
		return storage.ErrNotFound
	}
	if !sameSliceRef(stack.AuthoringSlice, cs.AuthoringSlice) || stack.TargetRef != cs.TargetRef {
		return storage.ErrInvalid
	}
	parentChangesetID = strings.TrimSpace(parentChangesetID)
	parentPatchsetID = strings.TrimSpace(parentPatchsetID)

	var parentEntry *corev1.ChangesetStackEntry
	if parentChangesetID != "" {
		parent := b.changesets[parentChangesetID]
		if parent == nil || parent.StackId != stack.Id {
			return storage.ErrInvalid
		}
		parentEntry = stackEntryByChangeset(stack, parentChangesetID)
		if parentEntry == nil {
			return storage.ErrInvalid
		}
		if parentPatchsetID == "" {
			parentPatchsetID = parent.CurrentPatchsetId
		}
		if parentPatchsetID == "" || parentPatchsetID != parent.CurrentPatchsetId {
			return storage.ErrConflict
		}
	} else if stack.RootEntryId != "" {
		return storage.ErrConflict
	}

	displayOrder := int64(1)
	siblingOrder := int64(1)
	for _, entry := range stack.Entries {
		if entry.DisplayOrder >= displayOrder {
			displayOrder = entry.DisplayOrder + 1
		}
		if entry.ParentChangesetId == parentChangesetID && entry.SiblingOrder >= siblingOrder {
			siblingOrder = entry.SiblingOrder + 1
		}
	}
	depth := int64(0)
	if parentEntry != nil {
		depth = parentEntry.Depth + 1
	}
	entry := &corev1.ChangesetStackEntry{
		StackId:           stack.Id,
		ChangesetId:       cs.Id,
		ParentChangesetId: parentChangesetID,
		ParentPatchsetId:  parentPatchsetID,
		SiblingOrder:      siblingOrder,
		DisplayOrder:      displayOrder,
		Depth:             depth,
		State:             "draft",
	}
	cs.StackId = stack.Id
	cs.StackOrder = displayOrder
	cs.StackDepth = depth
	cs.SiblingOrder = siblingOrder
	cs.ParentChangesetId = parentChangesetID
	cs.ParentPatchsetId = parentPatchsetID
	if parentChangesetID == "" {
		cs.BaseKind = "commit"
		stack.RootEntryId = cs.Id
	} else {
		cs.BaseKind = "patchset"
	}
	stack.ActiveEntryId = cs.Id
	stack.Entries = append(stack.Entries, cloneStackEntry(entry))
	stack.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	return nil
}

func stackEntryByChangeset(stack *corev1.ChangesetStack, changesetID string) *corev1.ChangesetStackEntry {
	if stack == nil {
		return nil
	}
	for _, entry := range stack.Entries {
		if entry.ChangesetId == changesetID {
			return entry
		}
	}
	return nil
}

func (b *backend) markChildrenStaleLocked(stackID, parentChangesetID, newParentPatchsetID string) {
	stack := b.stacks[stackID]
	if stack == nil {
		return
	}
	for _, entry := range stack.Entries {
		if entry.ParentChangesetId == parentChangesetID && entry.ParentPatchsetId != "" && entry.ParentPatchsetId != newParentPatchsetID {
			entry.State = "needs_restack"
		}
	}
}

func (b *backend) markSubtreeStateLocked(stack *corev1.ChangesetStack, rootChangesetID, state string) {
	descendants := map[string]struct{}{rootChangesetID: {}}
	changed := true
	for changed {
		changed = false
		for _, entry := range stack.Entries {
			if entry == nil {
				continue
			}
			if _, ok := descendants[entry.ChangesetId]; ok {
				continue
			}
			if _, ok := descendants[entry.ParentChangesetId]; ok {
				descendants[entry.ChangesetId] = struct{}{}
				changed = true
			}
		}
	}
	for _, entry := range stack.Entries {
		if _, ok := descendants[entry.ChangesetId]; ok {
			entry.State = state
		}
	}
}

func (b *backend) stackSubtreeIDsLocked(stack *corev1.ChangesetStack, rootChangesetID string) map[string]struct{} {
	descendants := map[string]struct{}{rootChangesetID: {}}
	changed := true
	for changed {
		changed = false
		for _, entry := range stack.Entries {
			if entry == nil {
				continue
			}
			if _, ok := descendants[entry.ChangesetId]; ok {
				continue
			}
			if _, ok := descendants[entry.ParentChangesetId]; ok {
				descendants[entry.ChangesetId] = struct{}{}
				changed = true
			}
		}
	}
	return descendants
}

func (b *backend) refreshStackStatusLocked(stackID string) {
	if stackID == "" {
		return
	}
	stack := b.stacks[stackID]
	if stack == nil || len(stack.Entries) == 0 {
		return
	}
	for _, entry := range stack.Entries {
		if entry == nil {
			continue
		}
		cs := b.changesets[entry.ChangesetId]
		if cs == nil || (cs.Status != "submitted" && cs.Status != "abandoned") {
			return
		}
	}
	stack.Status = "closed"
	stack.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
}

func (b *backend) stackEntryHasAncestorLocked(stack *corev1.ChangesetStack, changesetID, ancestorID string) bool {
	for changesetID != "" {
		if changesetID == ancestorID {
			return true
		}
		entry := stackEntryByChangeset(stack, changesetID)
		if entry == nil {
			return false
		}
		changesetID = entry.ParentChangesetId
	}
	return false
}

func (b *backend) reorderStackSiblingLocked(stack *corev1.ChangesetStack, moved *corev1.ChangesetStackEntry, requestedOrder int64) {
	if moved == nil {
		return
	}
	siblings := make([]*corev1.ChangesetStackEntry, 0)
	for _, entry := range stack.Entries {
		if entry == nil || entry.ChangesetId == moved.ChangesetId || entry.ParentChangesetId != moved.ParentChangesetId {
			continue
		}
		siblings = append(siblings, entry)
	}
	sort.Slice(siblings, func(i, j int) bool {
		if siblings[i].SiblingOrder == siblings[j].SiblingOrder {
			return siblings[i].ChangesetId < siblings[j].ChangesetId
		}
		return siblings[i].SiblingOrder < siblings[j].SiblingOrder
	})
	if requestedOrder <= 0 {
		requestedOrder = int64(len(siblings) + 1)
	}
	index := int(requestedOrder - 1)
	if index < 0 {
		index = 0
	}
	if index > len(siblings) {
		index = len(siblings)
	}
	siblings = append(siblings, nil)
	copy(siblings[index+1:], siblings[index:])
	siblings[index] = moved
	for i, entry := range siblings {
		entry.SiblingOrder = int64(i + 1)
		if cs := b.changesets[entry.ChangesetId]; cs != nil {
			cs.SiblingOrder = entry.SiblingOrder
		}
	}
}

func (b *backend) recomputeStackDisplayLocked(stack *corev1.ChangesetStack) {
	children := map[string][]*corev1.ChangesetStackEntry{}
	var roots []*corev1.ChangesetStackEntry
	for _, entry := range stack.Entries {
		if entry == nil {
			continue
		}
		if entry.ParentChangesetId == "" {
			roots = append(roots, entry)
		} else {
			children[entry.ParentChangesetId] = append(children[entry.ParentChangesetId], entry)
		}
	}
	sortStackEntriesBySibling(roots)
	for parent := range children {
		sortStackEntriesBySibling(children[parent])
	}
	var order int64
	var walk func(entries []*corev1.ChangesetStackEntry, depth int64)
	walk = func(entries []*corev1.ChangesetStackEntry, depth int64) {
		for _, entry := range entries {
			order++
			entry.DisplayOrder = order
			entry.Depth = depth
			if cs := b.changesets[entry.ChangesetId]; cs != nil {
				cs.StackOrder = order
				cs.StackDepth = depth
				cs.ParentChangesetId = entry.ParentChangesetId
				cs.ParentPatchsetId = entry.ParentPatchsetId
			}
			walk(children[entry.ChangesetId], depth+1)
		}
	}
	walk(roots, 0)
}

func sortStackEntriesBySibling(entries []*corev1.ChangesetStackEntry) {
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].SiblingOrder == entries[j].SiblingOrder {
			return entries[i].ChangesetId < entries[j].ChangesetId
		}
		return entries[i].SiblingOrder < entries[j].SiblingOrder
	})
}

func (b *backend) baseTreeForPatchsetLocked(patchset *corev1.Patchset) (string, error) {
	if patchset.BaseTreeId != "" {
		return patchset.BaseTreeId, nil
	}
	if patchset.BaseKind == "patchset" {
		basePatchset := b.patchsetByIDLocked(patchset.BasePatchsetId)
		if basePatchset == nil || basePatchset.ResultTreeId == "" {
			return "", storage.ErrConflict
		}
		return basePatchset.ResultTreeId, nil
	}
	commit := b.commits[patchset.BaseCommitId]
	if commit == nil {
		return "", storage.ErrNotFound
	}
	return commit.RootTreeId, nil
}

func (b *backend) patchsetByIDLocked(patchsetID string) *corev1.Patchset {
	for _, cs := range b.changesets {
		for _, patchset := range cs.Patchsets {
			if patchset.Id == patchsetID {
				return patchset
			}
		}
	}
	return nil
}

func (b *backend) previewTreeForPatchsetLocked(baseTreeID string, edits []*corev1.FileEdit) (string, error) {
	files, dirs, ok := b.filesAndDirsForRootTreeLocked(baseTreeID)
	if !ok {
		return "", storage.ErrNotFound
	}
	for _, edit := range edits {
		if edit == nil {
			continue
		}
		switch edit.Op {
		case "delete":
			deleteMemoryPath(files, dirs, edit.Path)
		case "rename":
			renameMemoryPath(files, dirs, edit.OldPath, edit.Path)
		case "mkdir":
			ensureMemoryDir(dirs, edit.Path)
		default:
			blob := b.blobs[edit.BlobId]
			contentHash := edit.ContentHash
			size := int64(0)
			if blob != nil {
				contentHash = blob.ContentHash
				size = blob.Size
			}
			if contentHash == "" {
				return "", storage.ErrNotFound
			}
			ensureMemoryParentDirs(dirs, edit.Path)
			files[edit.Path] = storage.FileEntry{Path: edit.Path, BlobID: edit.BlobId, ContentHash: contentHash, Mode: edit.Mode, Size: size}
		}
	}
	if len(edits) == 0 {
		return baseTreeID, nil
	}
	treeID := b.nextIDLocked("mem_tree_preview")
	b.previewFiles[treeID] = files
	b.previewDirs[treeID] = dirs
	return treeID, nil
}

func (b *backend) filesAndDirsForRootTreeLocked(rootTreeID string) (map[string]storage.FileEntry, map[string]struct{}, bool) {
	if files, ok := b.previewFiles[rootTreeID]; ok {
		return cloneFileMap(files), cloneDirSet(b.previewDirs[rootTreeID]), true
	}
	for commitID, commit := range b.commits {
		if commit != nil && commit.RootTreeId == rootTreeID {
			return cloneFileMap(b.commitFiles[commitID]), cloneDirSet(b.commitDirs[commitID]), true
		}
	}
	return nil, nil, false
}

func deleteMemoryPath(files map[string]storage.FileEntry, dirs map[string]struct{}, p string) {
	delete(files, p)
	delete(dirs, p)
	for filePath := range files {
		if pathContains(p, filePath) {
			delete(files, filePath)
		}
	}
	for dirPath := range dirs {
		if pathContains(p, dirPath) {
			delete(dirs, dirPath)
		}
	}
}

func renameMemoryPath(files map[string]storage.FileEntry, dirs map[string]struct{}, oldPath, newPath string) {
	if file, ok := files[oldPath]; ok {
		delete(files, oldPath)
		file.Path = newPath
		files[newPath] = file
		ensureMemoryParentDirs(dirs, newPath)
	}
	if _, ok := dirs[oldPath]; ok {
		delete(dirs, oldPath)
		dirs[newPath] = struct{}{}
	}
	for filePath, file := range cloneFileMap(files) {
		if !pathContains(oldPath, filePath) || filePath == oldPath {
			continue
		}
		delete(files, filePath)
		renamed := strings.TrimRight(newPath, "/") + strings.TrimPrefix(filePath, strings.TrimRight(oldPath, "/"))
		file.Path = renamed
		files[renamed] = file
	}
	for dirPath := range cloneDirSet(dirs) {
		if !pathContains(oldPath, dirPath) || dirPath == oldPath {
			continue
		}
		delete(dirs, dirPath)
		renamed := strings.TrimRight(newPath, "/") + strings.TrimPrefix(dirPath, strings.TrimRight(oldPath, "/"))
		dirs[renamed] = struct{}{}
	}
	ensureMemoryParentDirs(dirs, newPath)
}

func ensureMemoryDir(dirs map[string]struct{}, p string) {
	if p == "" || p == "/" || p == "." {
		return
	}
	dirs[p] = struct{}{}
	ensureMemoryParentDirs(dirs, p)
}

func ensureMemoryParentDirs(dirs map[string]struct{}, p string) {
	for parent := path.Dir(p); parent != "" && parent != "/" && parent != "."; parent = path.Dir(parent) {
		dirs[parent] = struct{}{}
	}
}

func treeEntryFromMemorySnapshot(files map[string]storage.FileEntry, dirs map[string]struct{}, p string) (*storage.TreeEntry, error) {
	if file, ok := files[p]; ok {
		return &storage.TreeEntry{Path: file.Path, Name: path.Base(file.Path), Kind: "file", Mode: file.Mode, BlobID: file.BlobID, ContentHash: file.ContentHash, Size: file.Size}, nil
	}
	if p == "/" || hasDescendant(files, p) || hasDescendantDir(dirs, p) {
		return &storage.TreeEntry{Path: p, Name: path.Base(p), Kind: "directory", TreeID: "mem_tree_" + strings.Trim(p, "/")}, nil
	}
	if _, ok := dirs[p]; ok {
		return &storage.TreeEntry{Path: p, Name: path.Base(p), Kind: "directory", TreeID: "mem_tree_" + strings.Trim(p, "/")}, nil
	}
	return nil, storage.ErrNotFound
}

func submitRequirementsForMemorySlice(slice *corev1.Slice) *corev1.SubmitRequirements {
	req := &corev1.SubmitRequirements{}
	if slice == nil {
		return req
	}
	req.SourceSliceDefinitionHash = slice.DefinitionHash
	if slice.Definition != nil {
		req.RequiredApprovals = slice.Definition.RequiredApprovals
		req.RequiredChecks = append([]string(nil), slice.Definition.RequiredChecks...)
	}
	return req
}

func (b *backend) blockSubmitLocked(cs *corev1.Changeset, reason string) error {
	cs.SubmitBlockedReason = reason
	return fmt.Errorf("%w: %s", storage.ErrConflict, reason)
}

func normalizePathPrefixes(prefixes []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(prefixes))
	for _, prefix := range prefixes {
		prefix = strings.TrimRight(strings.TrimSpace(prefix), "/")
		if prefix == "" {
			prefix = "/"
		}
		if _, ok := seen[prefix]; ok {
			continue
		}
		seen[prefix] = struct{}{}
		out = append(out, prefix)
	}
	sort.Strings(out)
	return out
}

func commitMatchesResolveFilter(b *backend, commit *corev1.Commit, prefixes []string, entityRefs map[string]struct{}, includePrefixesWithEntities bool) bool {
	if len(prefixes) == 0 && len(entityRefs) == 0 {
		return true
	}
	if includePrefixesWithEntities || len(entityRefs) == 0 {
		for _, changed := range commit.ChangedPaths {
			for _, prefix := range prefixes {
				if pathContains(prefix, changed) {
					return true
				}
			}
		}
	}
	for _, changed := range commit.ChangedPaths {
		for _, ref := range b.entityChanges[changed] {
			if _, ok := entityRefs[ref.AccountID+"\x00"+ref.EntityID]; ok {
				return true
			}
		}
	}
	return false
}

func hasDescendant(files map[string]storage.FileEntry, prefix string) bool {
	for p := range files {
		if pathContains(prefix, p) {
			return true
		}
	}
	return false
}

func hasDescendantDir(dirs map[string]struct{}, prefix string) bool {
	prefix = strings.TrimRight(prefix, "/")
	if prefix == "" {
		prefix = "/"
	}
	if prefix == "/" {
		return len(dirs) > 0
	}
	for dir := range dirs {
		if strings.HasPrefix(dir, prefix+"/") {
			return true
		}
	}
	return false
}

func directoryEntries(prefix string, files map[string]storage.FileEntry, dirs map[string]struct{}) []storage.TreeEntry {
	prefix = strings.TrimRight(prefix, "/")
	if prefix == "" {
		prefix = "/"
	}
	byPath := map[string]storage.TreeEntry{}
	for _, file := range files {
		rel, ok := relativeDirectoryPath(prefix, file.Path)
		if !ok {
			continue
		}
		if rel == "" {
			byPath[file.Path] = storage.TreeEntry{Path: file.Path, Name: path.Base(file.Path), Kind: "file", Mode: file.Mode, BlobID: file.BlobID, ContentHash: file.ContentHash, Size: file.Size}
			continue
		}
		parts := strings.Split(rel, "/")
		child := strings.TrimRight(prefix, "/") + "/" + parts[0]
		if prefix == "/" {
			child = "/" + parts[0]
		}
		if len(parts) == 1 {
			byPath[child] = storage.TreeEntry{Path: file.Path, Name: path.Base(file.Path), Kind: "file", Mode: file.Mode, BlobID: file.BlobID, ContentHash: file.ContentHash, Size: file.Size}
		} else if _, ok := byPath[child]; !ok {
			byPath[child] = storage.TreeEntry{Path: child, Name: parts[0], Kind: "directory", TreeID: "mem_tree_" + strings.Trim(child, "/")}
		}
	}
	for dir := range dirs {
		rel, ok := relativeDirectoryPath(prefix, dir)
		if !ok || rel == "" {
			continue
		}
		parts := strings.Split(rel, "/")
		child := strings.TrimRight(prefix, "/") + "/" + parts[0]
		if prefix == "/" {
			child = "/" + parts[0]
		}
		if _, ok := byPath[child]; !ok {
			byPath[child] = storage.TreeEntry{Path: child, Name: parts[0], Kind: "directory", TreeID: "mem_tree_" + strings.Trim(child, "/")}
		}
	}
	paths := make([]string, 0, len(byPath))
	for p := range byPath {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	out := make([]storage.TreeEntry, 0, len(paths))
	for _, p := range paths {
		out = append(out, byPath[p])
	}
	return out
}

func relativeDirectoryPath(prefix, filePath string) (string, bool) {
	if prefix == "/" {
		if !strings.HasPrefix(filePath, "/") {
			return "", false
		}
		return strings.TrimPrefix(filePath, "/"), true
	}
	if filePath == prefix {
		return "", true
	}
	if !strings.HasPrefix(filePath, prefix+"/") {
		return "", false
	}
	return strings.TrimPrefix(filePath, prefix+"/"), true
}

func sortedChangesets(changesets map[string]*corev1.Changeset) []*corev1.Changeset {
	out := make([]*corev1.Changeset, 0, len(changesets))
	for _, cs := range changesets {
		out = append(out, cs)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Id < out[j].Id })
	return out
}

func (b *backend) pendingChangesetsInSequenceLocked() []*corev1.Changeset {
	out := make([]*corev1.Changeset, 0, len(b.changesets))
	for _, cs := range b.changesets {
		if cs != nil && cs.Status == "pending_publish" {
			out = append(out, cs)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		leftSeq := b.pendingSequence[out[i].Id]
		rightSeq := b.pendingSequence[out[j].Id]
		if leftSeq == rightSeq {
			leftAt := b.pendingAcceptedAt[out[i].Id]
			rightAt := b.pendingAcceptedAt[out[j].Id]
			if leftAt.Equal(rightAt) {
				return out[i].Id < out[j].Id
			}
			return leftAt.Before(rightAt)
		}
		return leftSeq < rightSeq
	})
	return out
}

func currentPatchset(cs *corev1.Changeset) *corev1.Patchset {
	for _, patchset := range cs.Patchsets {
		if patchset.Id == cs.CurrentPatchsetId {
			return patchset
		}
	}
	if len(cs.Patchsets) == 0 {
		return nil
	}
	return cs.Patchsets[len(cs.Patchsets)-1]
}

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
	out := *in
	return &out
}

func cloneRef(in *corev1.Ref) *corev1.Ref {
	if in == nil {
		return nil
	}
	out := *in
	return &out
}

func cloneCommit(in *corev1.Commit) *corev1.Commit {
	if in == nil {
		return nil
	}
	out := *in
	out.ParentIds = append([]string(nil), in.ParentIds...)
	out.ChangedPaths = append([]string(nil), in.ChangedPaths...)
	return &out
}

func cloneSliceRef(in *corev1.SliceRef) *corev1.SliceRef {
	if in == nil {
		return nil
	}
	out := *in
	return &out
}

func cloneSlice(in *corev1.Slice) *corev1.Slice {
	if in == nil {
		return nil
	}
	out := *in
	out.Ref = cloneSliceRef(in.Ref)
	if in.Definition != nil {
		def := *in.Definition
		def.IncludedPaths = append([]string(nil), in.Definition.IncludedPaths...)
		def.RequiredChecks = append([]string(nil), in.Definition.RequiredChecks...)
		out.Definition = &def
	}
	return &out
}

func cloneSliceDefinitionVersion(in *corev1.SliceDefinitionVersion) *corev1.SliceDefinitionVersion {
	if in == nil {
		return nil
	}
	out := *in
	out.IncludedPaths = append([]string(nil), in.IncludedPaths...)
	out.RequiredChecks = append([]string(nil), in.RequiredChecks...)
	return &out
}

func clonePatchset(in *corev1.Patchset) *corev1.Patchset {
	if in == nil {
		return nil
	}
	out := *in
	out.ChangedPaths = append([]string(nil), in.ChangedPaths...)
	out.FileEdits = append([]*corev1.FileEdit(nil), in.FileEdits...)
	out.Coverage = append([]*corev1.PathCoverage(nil), in.Coverage...)
	out.PathBases = append([]*corev1.PathBase(nil), in.PathBases...)
	out.ReadSet = append([]*corev1.PathSetEntry(nil), in.ReadSet...)
	out.WriteSet = append([]*corev1.PathSetEntry(nil), in.WriteSet...)
	out.Conflicts = append([]*corev1.PatchsetConflict(nil), in.Conflicts...)
	if in.SubmitRequirements != nil {
		req := *in.SubmitRequirements
		req.RequiredChecks = append([]string(nil), in.SubmitRequirements.RequiredChecks...)
		req.PathLockIds = append([]string(nil), in.SubmitRequirements.PathLockIds...)
		out.SubmitRequirements = &req
	}
	return &out
}

func cloneStackEntry(in *corev1.ChangesetStackEntry) *corev1.ChangesetStackEntry {
	if in == nil {
		return nil
	}
	out := *in
	out.Changeset = cloneChangeset(in.Changeset)
	return &out
}

func cloneStack(in *corev1.ChangesetStack) *corev1.ChangesetStack {
	if in == nil {
		return nil
	}
	out := *in
	out.AuthoringSlice = cloneSliceRef(in.AuthoringSlice)
	out.Entries = make([]*corev1.ChangesetStackEntry, 0, len(in.Entries))
	for _, entry := range in.Entries {
		out.Entries = append(out.Entries, cloneStackEntry(entry))
	}
	return &out
}

func cloneChangeset(in *corev1.Changeset) *corev1.Changeset {
	if in == nil {
		return nil
	}
	out := *in
	out.AuthoringSlice = cloneSliceRef(in.AuthoringSlice)
	out.Patchsets = make([]*corev1.Patchset, 0, len(in.Patchsets))
	for _, patchset := range in.Patchsets {
		out.Patchsets = append(out.Patchsets, clonePatchset(patchset))
	}
	if in.SubmitRequirements != nil {
		req := *in.SubmitRequirements
		req.RequiredChecks = append([]string(nil), in.SubmitRequirements.RequiredChecks...)
		req.PathLockIds = append([]string(nil), in.SubmitRequirements.PathLockIds...)
		out.SubmitRequirements = &req
	}
	return &out
}

func cloneGitImport(in *storage.GitImportRecord) *storage.GitImportRecord {
	if in == nil {
		return nil
	}
	out := *in
	return &out
}

func sortCommits(commits []*corev1.Commit) {
	sort.Slice(commits, func(i, j int) bool {
		if commits[i].CreatedAt == commits[j].CreatedAt {
			return commits[i].Id > commits[j].Id
		}
		return commits[i].CreatedAt > commits[j].CreatedAt
	})
}
