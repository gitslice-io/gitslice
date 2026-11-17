package authz

import (
	"context"
	"errors"
	"testing"

	"gitslice.io/gitslice/internal/authctx"
	"gitslice.io/gitslice/internal/storage"
	"gitslice.io/gitslice/internal/storage/memory"
	corev1 "gitslice.io/gitslice/proto/core/v1"
)

func TestAuthorizerEnforcesPrincipalKeySliceScopeAndMaxRole(t *testing.T) {
	ctx := context.Background()
	mem := memory.New()
	owner := "user_owner"
	sliceAdmin := "user_slice_admin"

	if err := mem.Auth.CreateOrganization(ctx, "acme", []string{owner}, owner); err != nil {
		t.Fatal(err)
	}
	allowedSlice, err := mem.Slices.Create(ctx, owner, &corev1.SliceRef{Account: "acme", Slice: "allowed"}, []string{"/acme/allowed"}, "private", 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	deniedPrivateSlice, err := mem.Slices.Create(ctx, owner, &corev1.SliceRef{Account: "acme", Slice: "secret"}, []string{"/acme/secret"}, "private", 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	deniedPublicSlice, err := mem.Slices.Create(ctx, owner, &corev1.SliceRef{Account: "acme", Slice: "public"}, []string{"/acme/public"}, "public", 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mem.Slices.SetMember(ctx, allowedSlice.Id, sliceAdmin, "admin", owner); err != nil {
		t.Fatal(err)
	}

	az := New(mem.Auth)

	t.Run("slice scope blocks unauthorized and public slices plus account actions", func(t *testing.T) {
		scopedCtx := authctx.WithPrincipal(ctx, authctx.Principal{
			SubjectID:     owner,
			KeyID:         "key_scoped",
			HasSliceScope: true,
			SliceIDs:      []string{allowedSlice.Id},
		})
		if err := az.Authorize(scopedCtx, owner, allowedSlice, ActionAdmin); err != nil {
			t.Fatalf("allowed slice ActionAdmin = %v", err)
		}
		for _, target := range []*corev1.Slice{deniedPrivateSlice, deniedPublicSlice} {
			if err := az.Authorize(scopedCtx, owner, target, ActionRead); !errors.Is(err, storage.ErrUnauthorized) {
				t.Fatalf("out-of-scope slice %s ActionRead = %v, want ErrUnauthorized", target.Id, err)
			}
			if _, ok, err := az.EffectiveRole(scopedCtx, owner, target); err != nil || ok {
				t.Fatalf("out-of-scope slice %s EffectiveRole = (_, %v, %v), want false", target.Id, ok, err)
			}
		}
		if err := az.AuthorizeAccount(scopedCtx, owner, "acme", ActionRead); !errors.Is(err, storage.ErrUnauthorized) {
			t.Fatalf("AuthorizeAccount under slice-scoped key = %v, want ErrUnauthorized", err)
		}
		// Empty slice scope denies everything, including public slices.
		emptyCtx := authctx.WithPrincipal(ctx, authctx.Principal{
			SubjectID:     owner,
			KeyID:         "key_empty",
			HasSliceScope: true,
			SliceIDs:      nil,
		})
		if err := az.Authorize(emptyCtx, owner, deniedPublicSlice, ActionRead); !errors.Is(err, storage.ErrUnauthorized) {
			t.Fatalf("empty slice scope ActionRead = %v, want ErrUnauthorized", err)
		}
	})

	t.Run("max_role caps account owner and slice admin", func(t *testing.T) {
		readerCtx := authctx.WithPrincipal(ctx, authctx.Principal{
			SubjectID: owner,
			KeyID:     "key_reader",
			MaxRole:   "reader",
		})
		if err := az.Authorize(readerCtx, owner, allowedSlice, ActionRead); err != nil {
			t.Fatalf("reader ActionRead = %v", err)
		}
		for _, action := range []Action{ActionPropose, ActionWrite, ActionReview, ActionSubmit, ActionAdmin} {
			if err := az.Authorize(readerCtx, owner, allowedSlice, action); !errors.Is(err, storage.ErrUnauthorized) {
				t.Fatalf("reader %s = %v, want ErrUnauthorized", action, err)
			}
		}
		if role, ok, err := az.EffectiveRole(readerCtx, owner, allowedSlice); err != nil || !ok || role != "reader" {
			t.Fatalf("reader EffectiveRole = %q, %v, %v; want reader, true", role, ok, err)
		}

		contribCtx := authctx.WithPrincipal(ctx, authctx.Principal{
			SubjectID: owner,
			KeyID:     "key_contrib",
			MaxRole:   "contributor",
		})
		if err := az.Authorize(contribCtx, owner, allowedSlice, ActionPropose); err != nil {
			t.Fatalf("contributor ActionPropose = %v", err)
		}
		for _, action := range []Action{ActionWrite, ActionReview, ActionSubmit, ActionAdmin} {
			if err := az.Authorize(contribCtx, owner, allowedSlice, action); !errors.Is(err, storage.ErrUnauthorized) {
				t.Fatalf("contributor %s = %v, want ErrUnauthorized", action, err)
			}
		}
		if err := az.AuthorizeAccount(contribCtx, owner, "acme", ActionPropose); err != nil {
			t.Fatalf("contributor AuthorizeAccount ActionPropose = %v", err)
		}
		if err := az.AuthorizeAccount(contribCtx, owner, "acme", ActionWrite); !errors.Is(err, storage.ErrUnauthorized) {
			t.Fatalf("contributor AuthorizeAccount ActionWrite = %v, want ErrUnauthorized", err)
		}

		writerCtx := authctx.WithPrincipal(ctx, authctx.Principal{
			SubjectID: sliceAdmin,
			KeyID:     "key_writer",
			MaxRole:   "writer",
		})
		if err := az.Authorize(writerCtx, sliceAdmin, allowedSlice, ActionSubmit); err != nil {
			t.Fatalf("writer ActionSubmit = %v", err)
		}
		if err := az.Authorize(writerCtx, sliceAdmin, allowedSlice, ActionAdmin); !errors.Is(err, storage.ErrUnauthorized) {
			t.Fatalf("writer ActionAdmin = %v, want ErrUnauthorized", err)
		}
		if role, ok, err := az.EffectiveRole(writerCtx, sliceAdmin, allowedSlice); err != nil || !ok || role != "writer" {
			t.Fatalf("writer EffectiveRole = %q, %v, %v; want writer, true", role, ok, err)
		}
	})

	t.Run("delegation and subject switch boundaries", func(t *testing.T) {
		actor := "user_controller"
		agent := "agent_bot"
		if err := mem.Auth.CreateOrganization(ctx, "botorg", []string{agent}, agent); err != nil {
			t.Fatal(err)
		}
		botSlice, err := mem.Slices.Create(ctx, agent, &corev1.SliceRef{Account: "botorg", Slice: "work"}, []string{"/botorg/work"}, "private", 0, nil)
		if err != nil {
			t.Fatal(err)
		}

		readerControllerCtx := authctx.WithPrincipal(ctx, authctx.Principal{
			SubjectID: actor,
			KeyID:     "key_ctrl_reader",
			MaxRole:   "reader",
		})
		if err := az.AuthorizeDelegated(readerControllerCtx, actor, agent, botSlice, ActionRead); err != nil {
			t.Fatalf("delegated ActionRead = %v", err)
		}
		if err := az.AuthorizeDelegated(readerControllerCtx, actor, agent, botSlice, ActionWrite); !errors.Is(err, storage.ErrUnauthorized) {
			t.Fatalf("delegated ActionWrite with reader key = %v, want ErrUnauthorized", err)
		}

		wrongScopeCtx := authctx.WithPrincipal(ctx, authctx.Principal{
			SubjectID:     actor,
			KeyID:         "key_ctrl_scope",
			HasSliceScope: true,
			SliceIDs:      []string{allowedSlice.Id},
		})
		if err := az.AuthorizeDelegated(wrongScopeCtx, actor, agent, botSlice, ActionRead); !errors.Is(err, storage.ErrUnauthorized) {
			t.Fatalf("delegated ActionRead outside slice scope = %v, want ErrUnauthorized", err)
		}

		// Clearing / replacing SubjectID must not apply actor's key grant to agent.
		switchedCtx := authctx.WithSubjectID(readerControllerCtx, agent)
		if err := az.Authorize(switchedCtx, agent, botSlice, ActionAdmin); err != nil {
			t.Fatalf("switched subject ActionAdmin = %v", err)
		}
	})
}
