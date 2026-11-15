package service

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"log/slog"
	"path"
	"strings"

	"gitslice.io/gitslice/internal/avatar"
	"gitslice.io/gitslice/internal/storage"
	corev1 "gitslice.io/gitslice/proto/core/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// avatarKeyPrefix is where uploaded pictures live in the object store:
// avatars/{account}/{sha256 of the stored PNG}.png.
const avatarKeyPrefix = "avatars/"

// SetAccountAvatar stores an uploaded picture for the account, or with no
// image removes it. The same people as UpdateAccountProfile may call it.
func (s *AuthService) SetAccountAvatar(ctx context.Context, req *corev1.SetAccountAvatarRequest) (*corev1.AccountProfile, error) {
	subjectID, err := requireSubject(ctx)
	if err != nil {
		return nil, err
	}
	account := strings.TrimSpace(req.GetAccount())
	if err := s.authorizeProfileChange(ctx, subjectID, account); err != nil {
		return nil, err
	}
	key := ""
	if len(req.GetImage()) > 0 {
		if s.ObjectStore == nil {
			return nil, status.Error(codes.Unavailable, "this server cannot store pictures")
		}
		stored, err := avatar.Normalize(req.GetImage())
		if err != nil {
			return nil, status.Error(codes.InvalidArgument, err.Error())
		}
		sum := sha256.Sum256(stored)
		key = avatarKeyPrefix + account + "/" + hex.EncodeToString(sum[:]) + ".png"
		if err := s.ObjectStore.Put(ctx, key, bytes.NewReader(stored)); err != nil {
			return nil, grpcError(err)
		}
	}
	previous, err := s.Auth.SetAccountAvatar(ctx, account, key)
	if err != nil {
		return nil, accountLookupError(err, account)
	}
	if previous != "" && previous != key && s.ObjectStore != nil {
		// The account no longer refers to it; a failure only leaves an
		// unreferenced object behind.
		if err := s.ObjectStore.Delete(ctx, previous); err != nil {
			slog.Warn("delete replaced avatar", "account", account, "key", previous, "error", err)
		}
	}
	profile, err := s.Auth.GetAccountProfile(ctx, account)
	if err != nil {
		return nil, accountLookupError(err, account)
	}
	return accountProfileProto(profile), nil
}

// AccountAvatar returns the PNG an account shows and its avatar version: the
// uploaded picture, or the one generated from the name ("" version) when
// there is none, the account does not exist, or the upload cannot be read.
// Anyone may read it, signed in or not, as on GitHub.
func (s *AuthService) AccountAvatar(ctx context.Context, account string) ([]byte, string) {
	account = strings.TrimSpace(account)
	profile, err := s.Auth.GetAccountProfile(ctx, account)
	if err != nil {
		if !errors.Is(err, storage.ErrNotFound) {
			slog.Warn("read account for avatar", "account", account, "error", err)
		}
		return avatar.Pixel(account), ""
	}
	if profile.AvatarKey == "" || s.ObjectStore == nil {
		return avatar.Pixel(account), ""
	}
	reader, err := s.ObjectStore.Get(ctx, profile.AvatarKey, 0, 0)
	if err != nil {
		slog.Warn("read uploaded avatar", "account", account, "key", profile.AvatarKey, "error", err)
		return avatar.Pixel(account), ""
	}
	defer reader.Close()
	data, err := io.ReadAll(io.LimitReader(reader, avatar.MaxUploadBytes))
	if err != nil {
		slog.Warn("read uploaded avatar", "account", account, "key", profile.AvatarKey, "error", err)
		return avatar.Pixel(account), ""
	}
	return data, avatarVersion(profile.AvatarKey)
}

// avatarVersion names an uploaded picture by the start of its hash, which its
// object key ends with; "" when there is none.
func avatarVersion(key string) string {
	if key == "" {
		return ""
	}
	name := strings.TrimSuffix(path.Base(key), ".png")
	if len(name) > 16 {
		name = name[:16]
	}
	return name
}
