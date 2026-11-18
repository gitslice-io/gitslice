package memory

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"gitslice.io/gitslice/internal/authctx"
	"gitslice.io/gitslice/internal/objectid"
	"gitslice.io/gitslice/internal/storage"
)

func (s *AuthStore) StartCliLogin(ctx context.Context) (string, time.Time, error) {
	code, err := objectid.RandomID("cli")
	if err != nil {
		return "", time.Time{}, err
	}
	now := time.Now().UTC()
	expiresAt := now.Add(10 * time.Minute)

	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	for hash, existing := range s.b.cliLoginSessions {
		if !existing.expiresAt.After(now) {
			delete(s.b.cliLoginSessions, hash)
		}
	}
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
	session.status = "approved"
	session.subjectID = subjectID
	session.sessionToken = ""
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
		token := session.sessionToken
		if token == "" {
			var err error
			token, err = objectid.RandomID("clitok")
			if err != nil {
				return "", "", "", err
			}
			s.b.sessions[token] = session.subjectID
		}
		delete(s.b.cliLoginSessions, codeHash)
		return "approved", token, session.subjectID, nil
	}
	return "expired", "", "", nil
}

func (s *AuthStore) EnsureExternalSubject(ctx context.Context, provider, externalID, email string) (string, error) {
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
	if existing, ok := s.b.subjects[subjectID]; !ok {
		s.b.subjects[subjectID] = storage.Subject{
			ID:                  subjectID,
			Kind:                "user",
			ControllerSubjectID: subjectID,
			DisplayName:         displayName,
		}
	} else if existing.Kind == "" || (existing.Kind == "user" && existing.ControllerSubjectID == "") {
		if existing.Kind == "" {
			existing.Kind = "user"
		}
		if existing.Kind == "user" && existing.ControllerSubjectID == "" {
			existing.ControllerSubjectID = subjectID
		}
		s.b.subjects[subjectID] = existing
	}
	if _, ok := s.b.externalIdentities[subjectID]; !ok {
		s.b.externalIdentities[subjectID] = externalIdentity{provider: strings.TrimSpace(provider), externalID: externalID}
	}
	return subjectID, nil
}

func (s *AuthStore) ExternalIdentity(ctx context.Context, subjectID string) (string, string, error) {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	subjectID = strings.TrimSpace(subjectID)
	if _, ok := s.b.subjects[subjectID]; !ok {
		return "", "", storage.ErrNotFound
	}
	identity := s.b.externalIdentities[subjectID]
	return identity.provider, identity.externalID, nil
}

func (s *AuthStore) VerifiedEmails(ctx context.Context, subjectID string) ([]string, error) {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	var out []string
	for email := range s.b.verifiedEmails[strings.TrimSpace(subjectID)] {
		out = append(out, email)
	}
	sort.Strings(out)
	return out, nil
}

func (s *AuthStore) SetVerifiedEmails(ctx context.Context, subjectID, source string, emails []string) error {
	subjectID = strings.TrimSpace(subjectID)
	source = strings.TrimSpace(source)
	if subjectID == "" || source == "" {
		return fmt.Errorf("%w: subject and source are required", storage.ErrInvalid)
	}
	normalized := storage.NormalizeEmails(emails)
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	current := s.b.verifiedEmails[subjectID]
	if current == nil {
		current = map[string]string{}
		s.b.verifiedEmails[subjectID] = current
	}
	for email, emailSource := range current {
		if emailSource == source {
			delete(current, email)
		}
	}
	for _, email := range normalized {
		current[email] = source
	}
	return nil
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

func (s *AuthStore) SubjectsByIDs(ctx context.Context, subjectIDs []string) (map[string]storage.Subject, error) {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	out := make(map[string]storage.Subject, len(subjectIDs))
	for _, subjectID := range subjectIDs {
		subjectID = strings.TrimSpace(subjectID)
		if subjectID == "" {
			continue
		}
		if subj, ok := s.b.subjects[subjectID]; ok {
			out[subjectID] = subj
		}
	}
	return out, nil
}

func (s *AuthStore) SubjectIDForUsername(ctx context.Context, username string) (string, error) {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	username = strings.TrimSpace(username)
	var found []string
	for subjectID, slug := range s.b.personalAccounts {
		if slug == username {
			found = append(found, subjectID)
		}
	}
	if len(found) == 0 {
		return "", storage.ErrNotFound
	}
	sort.Strings(found)
	return found[0], nil
}

func (s *AuthStore) AccountKind(ctx context.Context, accountSlug string) (string, error) {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	accountSlug = strings.TrimSpace(accountSlug)
	if s.b.orgAccounts[accountSlug] {
		return storage.AccountKindOrganization, nil
	}
	if s.b.accountSlugTakenLocked(accountSlug) {
		return storage.AccountKindPersonal, nil
	}
	return "", storage.ErrNotFound
}

func (s *AuthStore) CreateOrganization(ctx context.Context, slug string, ownerSubjectIDs []string, createdBy string) error {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	slug = strings.TrimSpace(slug)
	if slug == "" || len(ownerSubjectIDs) == 0 {
		return storage.ErrInvalid
	}
	if s.b.orgAccounts[slug] || s.b.accountSlugTakenLocked(slug) {
		return fmt.Errorf("%w: account %q already exists", storage.ErrConflict, slug)
	}
	s.b.orgAccounts[slug] = true
	s.b.orgCreators[slug] = strings.TrimSpace(createdBy)
	for _, owner := range ownerSubjectIDs {
		s.b.addAccountRoleLocked(owner, slug, "owner")
	}
	return nil
}

func (s *AuthStore) ListAccountMembers(ctx context.Context, accountSlug string) ([]storage.AccountMember, error) {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	accountSlug = strings.TrimSpace(accountSlug)
	var members []storage.AccountMember
	for subjectID, memberships := range s.b.accountMembers {
		role, ok := memberships[accountSlug]
		if !ok {
			continue
		}
		username := s.b.personalAccounts[subjectID]
		kind := "user"
		if _, ok := s.b.agentRegistrations[subjectID]; ok {
			kind = "agent"
		}
		members = append(members, storage.AccountMember{
			SubjectID:   subjectID,
			Username:    username,
			Role:        role,
			DisplayName: s.b.profiles[username].DisplayName,
			Kind:        kind,
		})
	}
	sort.SliceStable(members, func(i, j int) bool {
		a, b := members[i], members[j]
		if ra, rb := storage.AccountRoleRank(a.Role), storage.AccountRoleRank(b.Role); ra != rb {
			return ra < rb
		}
		if a.Username != b.Username {
			return a.Username < b.Username
		}
		return a.SubjectID < b.SubjectID
	})
	return members, nil
}

func (s *AuthStore) SetAccountMemberRole(ctx context.Context, accountSlug, subjectID, role string) error {
	var err error
	role, err = storage.ValidateAccountMemberRole(role)
	if err != nil {
		return err
	}
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	if err := s.b.requireOrgLocked(accountSlug); err != nil {
		return err
	}
	previous, wasMember := s.b.accountMembers[subjectID][accountSlug]
	s.b.addAccountRoleLocked(subjectID, accountSlug, role)
	if s.b.ownerCountLocked(accountSlug) == 0 {
		if wasMember {
			s.b.accountMembers[subjectID][accountSlug] = previous
		}
		return fmt.Errorf("%w: %q must keep at least one owner", storage.ErrConflict, accountSlug)
	}
	return nil
}

func (s *AuthStore) RemoveAccountMember(ctx context.Context, accountSlug, subjectID string) error {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	if err := s.b.requireOrgLocked(accountSlug); err != nil {
		return err
	}
	role, ok := s.b.accountMembers[subjectID][accountSlug]
	if !ok {
		return fmt.Errorf("%w: subject is not a member of %q", storage.ErrNotFound, accountSlug)
	}
	delete(s.b.accountMembers[subjectID], accountSlug)
	if s.b.ownerCountLocked(accountSlug) == 0 {
		s.b.accountMembers[subjectID][accountSlug] = role
		return fmt.Errorf("%w: %q must keep at least one owner", storage.ErrConflict, accountSlug)
	}
	return nil
}

func (b *backend) requireOrgLocked(accountSlug string) error {
	accountSlug = strings.TrimSpace(accountSlug)
	if b.orgAccounts[accountSlug] {
		return nil
	}
	if b.accountSlugTakenLocked(accountSlug) {
		return fmt.Errorf("%w: members can only be managed on organization accounts", storage.ErrConflict)
	}
	return storage.ErrNotFound
}

func (b *backend) ownerCountLocked(accountSlug string) int {
	owners := 0
	for _, memberships := range b.accountMembers {
		if memberships[accountSlug] == "owner" {
			owners++
		}
	}
	return owners
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
		b.subjects[subjectID] = storage.Subject{
			ID:                  subjectID,
			Kind:                "user",
			ControllerSubjectID: subjectID,
			DisplayName:         displayName,
		}
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

func (s *AuthStore) AuthenticateToken(ctx context.Context, token string) (*authctx.Principal, error) {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	token = strings.TrimSpace(token)
	if strings.HasPrefix(token, storage.APIKeyPrefix) {
		hash := memoryTokenHash(token)
		subjectID := s.b.apiKeys[hash]
		if subjectID == "" {
			return nil, storage.ErrUnauthenticated
		}
		var leaf *memoryAPIKey
		for _, rec := range s.b.apiKeyRecords {
			if rec.hash == hash {
				leaf = rec
				break
			}
		}
		if leaf == nil {
			return nil, storage.ErrUnauthenticated
		}
		now := time.Now().UTC()
		if !s.b.apiKeyChainActiveLocked(leaf.key.ID, now) {
			return nil, storage.ErrUnauthenticated
		}
		leaf.key.LastUsedAt = now
		subject := s.b.subjects[leaf.key.SubjectID]
		return &authctx.Principal{
			SubjectID:           leaf.key.SubjectID,
			SubjectKind:         subject.Kind,
			ControllerSubjectID: subject.ControllerSubjectID,
			KeyID:               leaf.key.ID,
			HasSliceScope:       leaf.key.HasSliceScope,
			SliceIDs:            slices.Clone(leaf.key.SliceIDs),
			MaxRole:             leaf.key.MaxRole,
			Purpose:             leaf.key.Purpose,
			DaemonID:            leaf.key.DaemonID,
			ConversationID:      leaf.key.ConversationID,
		}, nil
	}
	subjectID := s.b.sessions[token]
	if subjectID == "" {
		return nil, storage.ErrUnauthenticated
	}
	subject := s.b.subjects[subjectID]
	return &authctx.Principal{
		SubjectID:           subjectID,
		SubjectKind:         subject.Kind,
		ControllerSubjectID: subject.ControllerSubjectID,
	}, nil
}

func (s *AuthStore) SubjectForToken(ctx context.Context, token string) (*storage.Subject, error) {
	principal, err := s.AuthenticateToken(ctx, token)
	if err != nil {
		return nil, err
	}
	subjects, err := s.SubjectsByIDs(ctx, []string{principal.SubjectID})
	if err != nil {
		return nil, err
	}
	if subj, ok := subjects[principal.SubjectID]; ok {
		return &subj, nil
	}
	return &storage.Subject{
		ID:                  principal.SubjectID,
		Kind:                principal.SubjectKind,
		ControllerSubjectID: principal.ControllerSubjectID,
	}, nil
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
