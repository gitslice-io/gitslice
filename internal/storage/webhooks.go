package storage

import (
	"context"
	"time"
)

// Webhook events (design/24_webhooks.md). A webhook subscribes to some of
// them, or to "*" for all; ping is always delivered.
const (
	WebhookEventPing               = "ping"
	WebhookEventPush               = "push"
	WebhookEventTagCreated         = "tag.created"
	WebhookEventChangesetCreated   = "changeset.created"
	WebhookEventChangesetUpdated   = "changeset.updated"
	WebhookEventChangesetApproved  = "changeset.approved"
	WebhookEventChangesetSubmitted = "changeset.submitted"
	WebhookEventChangesetAbandoned = "changeset.abandoned"
	WebhookEventCheckRunCompleted  = "check_run.completed"
	WebhookEventsAll               = "*"
)

// WebhookEventNames lists the events a webhook can subscribe to.
var WebhookEventNames = []string{
	WebhookEventPush,
	WebhookEventTagCreated,
	WebhookEventChangesetCreated,
	WebhookEventChangesetUpdated,
	WebhookEventChangesetApproved,
	WebhookEventChangesetSubmitted,
	WebhookEventChangesetAbandoned,
	WebhookEventCheckRunCompleted,
}

// Webhook delivery states.
const (
	WebhookDeliveryPending   = "pending"
	WebhookDeliverySucceeded = "succeeded"
	WebhookDeliveryFailed    = "failed"
)

// Webhook is an endpoint subscribed to a slice's events.
type Webhook struct {
	ID      string
	SliceID string
	URL     string
	Events  []string
	Active  bool
	// SealedSecret is the signing secret as stored (secretbox), or "".
	SealedSecret string
	CreatedBy    string
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// WebhookEvent is something that happened. It holds references; the
// dispatcher reads the current objects when it builds the payload.
type WebhookEvent struct {
	ID   string
	Kind string
	// The slice the event belongs to, when the producer knows it. Otherwise
	// it is the changeset's authoring slice.
	SliceID        string
	ActorSubjectID string
	ChangesetID    string
	PatchsetID     string
	CommitID       string
	TargetRef      string
	TagName        string
	CheckRunID     string
	ChangedPaths   []string
	// Data holds event details that have no object to read later, as JSON
	// (for example a reported check's name and status).
	Data      string
	CreatedAt time.Time
}

// WebhookDelivery is one event sent (or to be sent) to one webhook.
type WebhookDelivery struct {
	ID             string
	WebhookID      string
	EventID        string
	Event          string
	Status         string
	Attempts       int
	ResponseStatus int
	Error          string
	RequestBody    []byte
	ResponseBody   string
	DurationMs     int64
	CreatedAt      time.Time
	DeliveredAt    time.Time
	NextAttemptAt  time.Time
}

// WebhookStore keeps webhooks, the events waiting to be fanned out to them,
// and their deliveries.
type WebhookStore interface {
	CreateWebhook(ctx context.Context, webhook Webhook) (*Webhook, error)
	GetWebhook(ctx context.Context, id string) (*Webhook, error)
	ListWebhooks(ctx context.Context, sliceID string) ([]Webhook, error)
	// ListActiveWebhooks lists every active webhook, for fanning events out.
	ListActiveWebhooks(ctx context.Context) ([]Webhook, error)
	UpdateWebhook(ctx context.Context, webhook Webhook) (*Webhook, error)
	// DeleteWebhook removes a webhook and its deliveries.
	DeleteWebhook(ctx context.Context, id string) error

	// AppendWebhookEvent records an event; it gets an id and time if unset.
	AppendWebhookEvent(ctx context.Context, event WebhookEvent) (*WebhookEvent, error)
	// ClaimWebhookEvents leases up to limit events that are not fanned out
	// yet, oldest first; another claimer skips them until the lease ends.
	ClaimWebhookEvents(ctx context.Context, limit int, lease time.Duration) ([]WebhookEvent, error)
	// FanOutWebhookEvent stores the event's deliveries and marks it fanned
	// out, in one transaction.
	FanOutWebhookEvent(ctx context.Context, eventID string, deliveries []WebhookDelivery) error

	// CreateWebhookDelivery stores one delivery (a ping or a redelivery).
	CreateWebhookDelivery(ctx context.Context, delivery WebhookDelivery) (*WebhookDelivery, error)
	// ClaimDueWebhookDeliveries leases up to limit pending deliveries whose
	// next attempt is due.
	ClaimDueWebhookDeliveries(ctx context.Context, limit int, lease time.Duration) ([]WebhookDelivery, error)
	// ClaimWebhookDelivery leases one pending delivery, or returns ErrConflict
	// when another claimer holds it.
	ClaimWebhookDelivery(ctx context.Context, id string, lease time.Duration) (*WebhookDelivery, error)
	// RecordWebhookAttempt saves an attempt's outcome: status, attempts,
	// response, next attempt, and ends the lease.
	RecordWebhookAttempt(ctx context.Context, delivery WebhookDelivery) error
	GetWebhookDelivery(ctx context.Context, id string) (*WebhookDelivery, error)
	ListWebhookDeliveries(ctx context.Context, webhookID string, limit int) ([]WebhookDelivery, error)
	// LastWebhookDeliveries returns each webhook's newest delivery.
	LastWebhookDeliveries(ctx context.Context, webhookIDs []string) (map[string]WebhookDelivery, error)
	// PruneWebhookHistory deletes fanned-out events and finished deliveries
	// older than before.
	PruneWebhookHistory(ctx context.Context, before time.Time) error
}
