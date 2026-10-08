package service

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"gitslice.io/gitslice/internal/authz"
	"gitslice.io/gitslice/internal/storage"
	"gitslice.io/gitslice/internal/webhooks"
	corev1 "gitslice.io/gitslice/proto/core/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const (
	maxWebhooksPerSlice   = 20
	maxWebhookSecretBytes = 256
	defaultDeliveryLimit  = 30
	maxDeliveryLimit      = 100
	// Request bodies in delivery lists are cut here; a push that touches
	// many paths can be large.
	maxListedRequestBody = 64 << 10
)

// WebhookService manages slice webhooks (design/24_webhooks.md). Only a
// slice's admins see or change them: a webhook's URL can carry a token.
type WebhookService struct {
	Auth       storage.AuthStore
	Slices     storage.SliceStore
	Webhooks   storage.WebhookStore
	Dispatcher *webhooks.Dispatcher
}

func (s *WebhookService) available() error {
	if s == nil || s.Webhooks == nil || s.Dispatcher == nil {
		return status.Error(codes.Unimplemented, "webhooks are not available on this server")
	}
	return nil
}

// adminSlice resolves a slice the caller administers.
func (s *WebhookService) adminSlice(ctx context.Context, subjectID string, ref *corev1.SliceRef) (*corev1.Slice, error) {
	ref, err := normalizeServiceSliceRef(ref)
	if err != nil {
		return nil, err
	}
	return resolveAuthorizedSlice(ctx, s.Auth, s.Slices, subjectID, ref, authz.ActionAdmin)
}

// adminWebhook loads a webhook whose slice the caller administers.
func (s *WebhookService) adminWebhook(ctx context.Context, subjectID, webhookID string) (*storage.Webhook, *corev1.Slice, error) {
	webhookID = strings.TrimSpace(webhookID)
	if webhookID == "" {
		return nil, nil, status.Error(codes.InvalidArgument, "webhook_id is required")
	}
	hook, err := s.Webhooks.GetWebhook(ctx, webhookID)
	if err != nil {
		return nil, nil, grpcError(err)
	}
	slice, err := s.Slices.Get(ctx, hook.SliceID)
	if err != nil {
		return nil, nil, grpcError(err)
	}
	if err := authorize(ctx, s.Auth, subjectID, slice, authz.ActionAdmin); err != nil {
		return nil, nil, err
	}
	return hook, slice, nil
}

// normalizeWebhookEvents checks event names and removes duplicates.
func normalizeWebhookEvents(events []string) ([]string, error) {
	var out []string
	for _, event := range events {
		for _, name := range strings.Split(event, ",") {
			name = strings.ToLower(strings.TrimSpace(name))
			if name == "" || slices.Contains(out, name) {
				continue
			}
			if name != storage.WebhookEventsAll && !slices.Contains(storage.WebhookEventNames, name) {
				return nil, status.Errorf(codes.InvalidArgument, "unknown event %q: use * or some of %s", name, strings.Join(storage.WebhookEventNames, ", "))
			}
			out = append(out, name)
		}
	}
	if len(out) == 0 {
		return nil, status.Errorf(codes.InvalidArgument, "choose at least one event: * or some of %s", strings.Join(storage.WebhookEventNames, ", "))
	}
	if slices.Contains(out, storage.WebhookEventsAll) {
		return []string{storage.WebhookEventsAll}, nil
	}
	return out, nil
}

func (s *WebhookService) validateURL(raw string) (string, error) {
	cleaned, err := s.Dispatcher.ValidateURL(raw)
	if err != nil {
		return "", status.Errorf(codes.InvalidArgument, "invalid webhook URL: %v", err)
	}
	return cleaned, nil
}

func (s *WebhookService) sealSecret(secret string) (string, error) {
	if len(secret) > maxWebhookSecretBytes {
		return "", status.Errorf(codes.InvalidArgument, "the secret is longer than %d bytes", maxWebhookSecretBytes)
	}
	sealed, err := s.Dispatcher.SealSecret(secret)
	if err != nil {
		return "", grpcError(fmt.Errorf("seal webhook secret: %w", err))
	}
	return sealed, nil
}

func (s *WebhookService) CreateWebhook(ctx context.Context, req *corev1.CreateWebhookRequest) (*corev1.Webhook, error) {
	if err := s.available(); err != nil {
		return nil, err
	}
	subjectID, err := requireSubject(ctx)
	if err != nil {
		return nil, err
	}
	slice, err := s.adminSlice(ctx, subjectID, req.GetSlice())
	if err != nil {
		return nil, err
	}
	url, err := s.validateURL(req.GetUrl())
	if err != nil {
		return nil, err
	}
	events, err := normalizeWebhookEvents(req.GetEvents())
	if err != nil {
		return nil, err
	}
	sealed, err := s.sealSecret(req.GetSecret())
	if err != nil {
		return nil, err
	}
	existing, err := s.Webhooks.ListWebhooks(ctx, slice.Id)
	if err != nil {
		return nil, grpcError(err)
	}
	if len(existing) >= maxWebhooksPerSlice {
		return nil, status.Errorf(codes.FailedPrecondition, "a slice can have at most %d webhooks", maxWebhooksPerSlice)
	}
	active := true
	if req.Active != nil {
		active = req.GetActive()
	}
	hook, err := s.Webhooks.CreateWebhook(ctx, storage.Webhook{
		SliceID:      slice.Id,
		URL:          url,
		Events:       events,
		Active:       active,
		SealedSecret: sealed,
		CreatedBy:    subjectID,
	})
	if err != nil {
		return nil, grpcError(err)
	}
	s.Dispatcher.WebhooksChanged()
	return s.webhookProto(ctx, slice, *hook), nil
}

func (s *WebhookService) ListWebhooks(ctx context.Context, req *corev1.ListWebhooksRequest) (*corev1.ListWebhooksResponse, error) {
	if err := s.available(); err != nil {
		return nil, err
	}
	subjectID, err := requireSubject(ctx)
	if err != nil {
		return nil, err
	}
	slice, err := s.adminSlice(ctx, subjectID, req.GetSlice())
	if err != nil {
		return nil, err
	}
	hooks, err := s.Webhooks.ListWebhooks(ctx, slice.Id)
	if err != nil {
		return nil, grpcError(err)
	}
	ids := make([]string, 0, len(hooks))
	creators := make([]string, 0, len(hooks))
	for _, hook := range hooks {
		ids = append(ids, hook.ID)
		creators = append(creators, hook.CreatedBy)
	}
	last, err := s.Webhooks.LastWebhookDeliveries(ctx, ids)
	if err != nil {
		return nil, grpcError(err)
	}
	names := s.usernames(ctx, creators)
	out := &corev1.ListWebhooksResponse{Webhooks: make([]*corev1.Webhook, 0, len(hooks))}
	for _, hook := range hooks {
		var lastDelivery *storage.WebhookDelivery
		if d, ok := last[hook.ID]; ok {
			lastDelivery = &d
		}
		out.Webhooks = append(out.Webhooks, webhookProto(slice, hook, lastDelivery, names))
	}
	return out, nil
}

func (s *WebhookService) UpdateWebhook(ctx context.Context, req *corev1.UpdateWebhookRequest) (*corev1.Webhook, error) {
	if err := s.available(); err != nil {
		return nil, err
	}
	subjectID, err := requireSubject(ctx)
	if err != nil {
		return nil, err
	}
	hook, slice, err := s.adminWebhook(ctx, subjectID, req.GetWebhookId())
	if err != nil {
		return nil, err
	}
	if req.Url != nil {
		if hook.URL, err = s.validateURL(req.GetUrl()); err != nil {
			return nil, err
		}
	}
	if req.GetUpdateEvents() {
		if hook.Events, err = normalizeWebhookEvents(req.GetEvents()); err != nil {
			return nil, err
		}
	}
	if req.Active != nil {
		hook.Active = req.GetActive()
	}
	switch {
	case req.GetClearSecret():
		hook.SealedSecret = ""
	case req.Secret != nil:
		if hook.SealedSecret, err = s.sealSecret(req.GetSecret()); err != nil {
			return nil, err
		}
	}
	updated, err := s.Webhooks.UpdateWebhook(ctx, *hook)
	if err != nil {
		return nil, grpcError(err)
	}
	s.Dispatcher.WebhooksChanged()
	return s.webhookProto(ctx, slice, *updated), nil
}

func (s *WebhookService) DeleteWebhook(ctx context.Context, req *corev1.DeleteWebhookRequest) (*corev1.Empty, error) {
	if err := s.available(); err != nil {
		return nil, err
	}
	subjectID, err := requireSubject(ctx)
	if err != nil {
		return nil, err
	}
	hook, _, err := s.adminWebhook(ctx, subjectID, req.GetWebhookId())
	if err != nil {
		return nil, err
	}
	if err := s.Webhooks.DeleteWebhook(ctx, hook.ID); err != nil {
		return nil, grpcError(err)
	}
	s.Dispatcher.WebhooksChanged()
	return &corev1.Empty{}, nil
}

func (s *WebhookService) PingWebhook(ctx context.Context, req *corev1.PingWebhookRequest) (*corev1.WebhookDelivery, error) {
	if err := s.available(); err != nil {
		return nil, err
	}
	subjectID, err := requireSubject(ctx)
	if err != nil {
		return nil, err
	}
	hook, _, err := s.adminWebhook(ctx, subjectID, req.GetWebhookId())
	if err != nil {
		return nil, err
	}
	delivery, err := s.Dispatcher.Ping(ctx, hook, subjectID)
	if err != nil {
		return nil, grpcError(err)
	}
	return deliveryProto(*delivery, true), nil
}

func (s *WebhookService) ListWebhookDeliveries(ctx context.Context, req *corev1.ListWebhookDeliveriesRequest) (*corev1.ListWebhookDeliveriesResponse, error) {
	if err := s.available(); err != nil {
		return nil, err
	}
	subjectID, err := requireSubject(ctx)
	if err != nil {
		return nil, err
	}
	hook, _, err := s.adminWebhook(ctx, subjectID, req.GetWebhookId())
	if err != nil {
		return nil, err
	}
	limit := int(req.GetLimit())
	if limit <= 0 {
		limit = defaultDeliveryLimit
	}
	limit = min(limit, maxDeliveryLimit)
	deliveries, err := s.Webhooks.ListWebhookDeliveries(ctx, hook.ID, limit)
	if err != nil {
		return nil, grpcError(err)
	}
	out := &corev1.ListWebhookDeliveriesResponse{Deliveries: make([]*corev1.WebhookDelivery, 0, len(deliveries))}
	for _, d := range deliveries {
		out.Deliveries = append(out.Deliveries, deliveryProto(d, false))
	}
	return out, nil
}

func (s *WebhookService) RedeliverWebhookDelivery(ctx context.Context, req *corev1.RedeliverWebhookDeliveryRequest) (*corev1.WebhookDelivery, error) {
	if err := s.available(); err != nil {
		return nil, err
	}
	subjectID, err := requireSubject(ctx)
	if err != nil {
		return nil, err
	}
	deliveryID := strings.TrimSpace(req.GetDeliveryId())
	if deliveryID == "" {
		return nil, status.Error(codes.InvalidArgument, "delivery_id is required")
	}
	delivery, err := s.Webhooks.GetWebhookDelivery(ctx, deliveryID)
	if err != nil {
		return nil, grpcError(err)
	}
	if _, _, err := s.adminWebhook(ctx, subjectID, delivery.WebhookID); err != nil {
		return nil, err
	}
	again, err := s.Dispatcher.Redeliver(ctx, delivery)
	if err != nil {
		return nil, grpcError(err)
	}
	return deliveryProto(*again, true), nil
}

func (s *WebhookService) usernames(ctx context.Context, subjects []string) map[string]string {
	if len(subjects) == 0 || s.Auth == nil {
		return nil
	}
	names, err := s.Auth.UsernamesForSubjects(ctx, subjects)
	if err != nil {
		return nil
	}
	return names
}

func (s *WebhookService) webhookProto(ctx context.Context, slice *corev1.Slice, hook storage.Webhook) *corev1.Webhook {
	return webhookProto(slice, hook, nil, s.usernames(ctx, []string{hook.CreatedBy}))
}

func webhookProto(slice *corev1.Slice, hook storage.Webhook, last *storage.WebhookDelivery, names map[string]string) *corev1.Webhook {
	out := &corev1.Webhook{
		Id:        hook.ID,
		Slice:     slice.GetRef(),
		Url:       hook.URL,
		Events:    hook.Events,
		Active:    hook.Active,
		HasSecret: hook.SealedSecret != "",
		CreatedBy: names[hook.CreatedBy],
		CreatedAt: webhookTime(hook.CreatedAt),
		UpdatedAt: webhookTime(hook.UpdatedAt),
	}
	if last != nil {
		out.LastDelivery = deliveryProto(*last, false)
		out.LastDelivery.RequestBody = ""
		out.LastDelivery.ResponseBody = ""
	}
	return out
}

func deliveryProto(d storage.WebhookDelivery, full bool) *corev1.WebhookDelivery {
	body := d.RequestBody
	if !full && len(body) > maxListedRequestBody {
		body = body[:maxListedRequestBody]
	}
	out := &corev1.WebhookDelivery{
		Id:             d.ID,
		WebhookId:      d.WebhookID,
		Event:          d.Event,
		EventId:        d.EventID,
		Status:         d.Status,
		Attempts:       int32(d.Attempts),
		ResponseStatus: int32(d.ResponseStatus),
		Error:          d.Error,
		CreatedAt:      webhookTime(d.CreatedAt),
		DeliveredAt:    webhookTime(d.DeliveredAt),
		DurationMs:     d.DurationMs,
		RequestBody:    strings.ToValidUTF8(string(body), "?"),
		ResponseBody:   d.ResponseBody,
	}
	if d.Status == storage.WebhookDeliveryPending {
		out.NextAttemptAt = webhookTime(d.NextAttemptAt)
	}
	return out
}

// webhookTime formats a time, or "" for none (stores may return the epoch).
func webhookTime(t time.Time) string {
	if t.Unix() <= 0 {
		return ""
	}
	return formatTime(t)
}
