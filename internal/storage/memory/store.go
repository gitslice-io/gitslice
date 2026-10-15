package memory

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path"
	"strings"
	"sync"
	"time"

	"gitslice.io/gitslice/internal/storage"
	corev1 "gitslice.io/gitslice/proto/core/v1"
)

type Stores struct {
	Auth       *AuthStore
	Blobs      *BlobStore
	Changesets *ChangesetStore
	Repository *RepositoryStore
	Slices     *SliceStore
	Objects    *ObjectStore
	Agents     *AgentStore
	Checks     *CheckStore
	Webhooks   *WebhookStore
	Feedback   *FeedbackStore
	Blames     *BlameStore

	backend *backend
}

type backend struct {
	mu   sync.Mutex
	next int64

	subjects         map[string]storage.Subject
	accountMembers   map[string]map[string]string
	personalAccounts map[string]string
	webhooks         map[string]storage.Webhook
	// sliceMembers maps a slice id to its members' roles, by subject.
	sliceMembers map[string]map[string]memSliceMember
	feedback     map[string]storage.Feedback
	// blameSnapshots maps a commit id, then a path, to a blame snapshot.
	blameSnapshots    map[string]map[string]storage.BlameSnapshot
	webhookEvents     []*memWebhookEvent
	webhookDeliveries map[string]*memWebhookDelivery
	// orgAccounts records which account slugs are organizations.
	orgAccounts map[string]bool
	// orgCreators records who created each organization.
	orgCreators map[string]string
	// invitations holds pending invitations, by account slug then subject.
	invitations map[string]map[string]storage.AccountInvitation
	// profiles holds account profiles by slug (only those set).
	profiles         map[string]storage.AccountProfile
	sessions         map[string]string
	cliLoginSessions map[string]cliLoginSession
	// apiKeys maps a hashed API key to its owning subject.
	apiKeys map[string]string
	// agentRegistrations maps an agent subject id to its registration.
	agentRegistrations map[string]agentRegistration
	// externalIdentities maps a subject id to its identity provider record.
	externalIdentities map[string]externalIdentity
	// verifiedEmails maps subject id -> email -> source.
	verifiedEmails map[string]map[string]string

	blobs      map[string]*corev1.BlobRecord
	blobSlices map[string]map[string]struct{}
	gitBlobIDs map[string]string // content hash → Git blob id
	objects    map[string][]byte

	refs        map[string]*corev1.Ref
	commits     map[string]*corev1.Commit
	commitFiles map[string]map[string]storage.FileEntry
	commitDirs  map[string]map[string]struct{}

	slices                  map[string]*corev1.Slice
	sliceRefs               map[string]string
	sliceDefinitionVersions map[string][]*corev1.SliceDefinitionVersion
	sliceTags               map[string]map[string]storage.SliceTag
	sliceSecrets            map[string]map[string]string

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

	checkRuns            map[string]*corev1.CheckRun
	checkRunSupersededBy map[string]string
	checkRunLogs         map[string][]*corev1.CheckRunLog
}

type cliLoginSession struct {
	status       string
	subjectID    string
	sessionToken string
	expiresAt    time.Time
}

type agentRegistration struct {
	account    string
	ownerEmail string
	createdAt  time.Time
	claimedBy  string
	claimedAt  time.Time
	// registrationTokenHash identifies a retryable registration; empty if the
	// client sent no token.
	registrationTokenHash string
}

type externalIdentity struct {
	provider   string
	externalID string
}

type AuthStore struct{ b *backend }

type BlobStore struct{ b *backend }

type ChangesetStore struct{ b *backend }

type RepositoryStore struct{ b *backend }

type SliceStore struct{ b *backend }

type ObjectStore struct{ b *backend }

type CheckStore struct{ b *backend }

func New() *Stores {
	b := &backend{
		subjects:                map[string]storage.Subject{},
		accountMembers:          map[string]map[string]string{},
		personalAccounts:        map[string]string{},
		orgAccounts:             map[string]bool{},
		webhooks:                map[string]storage.Webhook{},
		sliceMembers:            map[string]map[string]memSliceMember{},
		feedback:                map[string]storage.Feedback{},
		blameSnapshots:          map[string]map[string]storage.BlameSnapshot{},
		webhookDeliveries:       map[string]*memWebhookDelivery{},
		orgCreators:             map[string]string{},
		invitations:             map[string]map[string]storage.AccountInvitation{},
		profiles:                map[string]storage.AccountProfile{},
		sessions:                map[string]string{},
		cliLoginSessions:        map[string]cliLoginSession{},
		apiKeys:                 map[string]string{},
		agentRegistrations:      map[string]agentRegistration{},
		externalIdentities:      map[string]externalIdentity{},
		verifiedEmails:          map[string]map[string]string{},
		blobs:                   map[string]*corev1.BlobRecord{},
		blobSlices:              map[string]map[string]struct{}{},
		gitBlobIDs:              map[string]string{},
		objects:                 map[string][]byte{},
		refs:                    map[string]*corev1.Ref{},
		commits:                 map[string]*corev1.Commit{},
		commitFiles:             map[string]map[string]storage.FileEntry{},
		commitDirs:              map[string]map[string]struct{}{},
		slices:                  map[string]*corev1.Slice{},
		sliceRefs:               map[string]string{},
		sliceDefinitionVersions: map[string][]*corev1.SliceDefinitionVersion{},
		sliceTags:               map[string]map[string]storage.SliceTag{},
		sliceSecrets:            map[string]map[string]string{},
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
		checkRuns:               map[string]*corev1.CheckRun{},
		checkRunSupersededBy:    map[string]string{},
		checkRunLogs:            map[string][]*corev1.CheckRunLog{},
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
		Checks:     &CheckStore{b: b},
		Webhooks:   &WebhookStore{b: b},
		Feedback:   &FeedbackStore{b: b},
		Blames:     &BlameStore{b: b},
		backend:    b,
	}
}

func (s *Stores) AddAccount(subjectID, accountSlug string) {
	s.backend.mu.Lock()
	defer s.backend.mu.Unlock()
	s.backend.addAccountLocked(subjectID, accountSlug)
}

// AddPersonalAccount gives the subject its own account, as choosing a
// username does.
func (s *Stores) AddPersonalAccount(subjectID, username string) {
	s.backend.mu.Lock()
	defer s.backend.mu.Unlock()
	s.backend.provisionPersonalAccountLocked(subjectID, username, username)
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
	if prefix, ok := storage.ChangesetIDLookupPrefix(selector); ok {
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
		if match != "" {
			return match
		}
	}
	// Not a changeset: maybe the id of the commit a changeset landed as.
	if prefix, ok := storage.CommitIDLookupPrefix(selector); ok {
		var match string
		for id, cs := range b.changesets {
			if cs == nil || cs.CommitId == "" || !strings.HasPrefix(cs.CommitId, prefix) {
				continue
			}
			if match != "" {
				return ""
			}
			match = id
		}
		return match
	}
	return ""
}
