package cli

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net/url"
	"strings"

	"github.com/spf13/cobra"
	"gitslice.io/gitslice/proto/core/v1"
	"google.golang.org/grpc/codes"
	grpcstatus "google.golang.org/grpc/status"
)

type webhookOutput struct {
	ID           string                 `json:"id"`
	Slice        string                 `json:"slice"`
	URL          string                 `json:"url"`
	Events       []string               `json:"events"`
	Active       bool                   `json:"active"`
	HasSecret    bool                   `json:"has_secret"`
	CreatedBy    string                 `json:"created_by,omitempty"`
	CreatedAt    string                 `json:"created_at,omitempty"`
	UpdatedAt    string                 `json:"updated_at,omitempty"`
	LastDelivery *webhookDeliveryOutput `json:"last_delivery,omitempty"`
}

type webhookDeliveryOutput struct {
	ID             string `json:"id"`
	WebhookID      string `json:"webhook_id"`
	Event          string `json:"event"`
	EventID        string `json:"event_id"`
	Status         string `json:"status"`
	Attempts       int32  `json:"attempts"`
	ResponseStatus int32  `json:"response_status,omitempty"`
	Error          string `json:"error,omitempty"`
	CreatedAt      string `json:"created_at,omitempty"`
	DeliveredAt    string `json:"delivered_at,omitempty"`
	NextAttemptAt  string `json:"next_attempt_at,omitempty"`
	DurationMs     int64  `json:"duration_ms,omitempty"`
	RequestBody    string `json:"request_body,omitempty"`
	ResponseBody   string `json:"response_body,omitempty"`
}

const webhookEventsHelp = "push, tag.created, changeset.created, changeset.updated, changeset.approved, changeset.submitted, changeset.abandoned, check_run.completed, or * for all"

// webhookCommand manages slice webhooks: HTTPS endpoints Gitslice POSTs
// events to (design/24_webhooks.md).
func (r Runner) webhookCommand(opts *commandOptions) *cobra.Command {
	webhookCmd := &cobra.Command{
		Use:   "webhook",
		Short: "Send a slice's events to an HTTPS endpoint",
		Long: "Webhooks POST a JSON event to a URL when something happens in a slice: " + webhookEventsHelp + ".\n" +
			"With a secret, each delivery carries X-Gitslice-Signature-256: sha256=<HMAC-SHA256 of the body>.\n" +
			"Only the slice's owners and admins manage its webhooks.",
		RunE: requireSubcommand("webhook"),
	}

	var createSlice, createURL string
	var createEvents []string
	var createSecretStdin, createInactive bool
	createCmd := &cobra.Command{
		Use:   "create --url <https-url> --event <name> [--event <name>...]",
		Short: "Add a webhook to a slice",
		Args:  noArgs("gs webhook create [--slice account/slice] --url <https-url> --event <name>... [--secret-stdin] [--inactive]"),
		RunE: func(cmd *cobra.Command, args []string) error {
			secret, err := r.webhookSecret(createSecretStdin)
			if err != nil {
				return err
			}
			return r.runWebhookCreate(cmd.Context(), *opts, createSlice, createURL, createEvents, secret, !createInactive)
		},
	}
	createCmd.Flags().StringVar(&createSlice, "slice", "", "slice; defaults to the workspace slice")
	createCmd.Flags().StringVar(&createURL, "url", "", "the endpoint (https)")
	createCmd.Flags().StringSliceVar(&createEvents, "event", nil, "event to send ("+webhookEventsHelp+"); repeat or separate with commas")
	createCmd.Flags().BoolVar(&createSecretStdin, "secret-stdin", false, "read a signing secret from stdin")
	createCmd.Flags().BoolVar(&createInactive, "inactive", false, "create it switched off")

	var listSlice string
	listCmd := &cobra.Command{
		Use:   "list",
		Short: "List a slice's webhooks and how their last delivery went",
		Args:  noArgs("gs webhook list [--slice account/slice]"),
		RunE: func(cmd *cobra.Command, args []string) error {
			return r.runWebhookList(cmd.Context(), *opts, listSlice)
		},
	}
	listCmd.Flags().StringVar(&listSlice, "slice", "", "slice; defaults to the workspace slice")

	var updateURL, updateActive string
	var updateEvents []string
	var updateSecretStdin, updateClearSecret bool
	updateCmd := &cobra.Command{
		Use:   "update <webhook-id>",
		Short: "Change a webhook's URL, events, secret or whether it is active",
		Args:  exactArgs(1, "gs webhook update <webhook-id> [--url <https-url>] [--event <name>...] [--active true|false] [--secret-stdin | --clear-secret]"),
		RunE: func(cmd *cobra.Command, args []string) error {
			req := &corev1.UpdateWebhookRequest{WebhookId: args[0]}
			if cmd.Flags().Changed("url") {
				req.Url = &updateURL
			}
			if cmd.Flags().Changed("event") {
				req.UpdateEvents = true
				req.Events = updateEvents
			}
			if cmd.Flags().Changed("active") {
				active, err := parseBoolFlag("--active", updateActive)
				if err != nil {
					return err
				}
				req.Active = &active
			}
			if updateSecretStdin && updateClearSecret {
				return userError("invalid_argument", "use --secret-stdin or --clear-secret, not both", "")
			}
			if updateSecretStdin {
				secret, err := r.webhookSecret(true)
				if err != nil {
					return err
				}
				req.Secret = &secret
			}
			req.ClearSecret = updateClearSecret
			return r.runWebhookUpdate(cmd.Context(), *opts, req)
		},
	}
	updateCmd.Flags().StringVar(&updateURL, "url", "", "new endpoint (https)")
	updateCmd.Flags().StringSliceVar(&updateEvents, "event", nil, "replace the events ("+webhookEventsHelp+")")
	updateCmd.Flags().StringVar(&updateActive, "active", "", "true or false")
	updateCmd.Flags().BoolVar(&updateSecretStdin, "secret-stdin", false, "read a new signing secret from stdin")
	updateCmd.Flags().BoolVar(&updateClearSecret, "clear-secret", false, "stop signing deliveries")

	var deleteYes bool
	deleteCmd := &cobra.Command{
		Use:   "delete <webhook-id> --yes",
		Short: "Delete a webhook and its delivery history",
		Args:  exactArgs(1, "gs webhook delete <webhook-id> --yes"),
		RunE: func(cmd *cobra.Command, args []string) error {
			if !deleteYes {
				return userError("confirmation_required", "deleting a webhook needs --yes", "Run gs webhook delete "+args[0]+" --yes.")
			}
			return r.runWebhookDelete(cmd.Context(), *opts, args[0])
		},
	}
	deleteCmd.Flags().BoolVar(&deleteYes, "yes", false, "confirm deleting the webhook")

	pingCmd := &cobra.Command{
		Use:   "ping <webhook-id>",
		Short: "Send a ping event now and show the response",
		Args:  exactArgs(1, "gs webhook ping <webhook-id>"),
		RunE: func(cmd *cobra.Command, args []string) error {
			return r.runWebhookPing(cmd.Context(), *opts, args[0])
		},
	}

	var deliveriesLimit int32
	deliveriesCmd := &cobra.Command{
		Use:   "deliveries <webhook-id>",
		Short: "List a webhook's recent deliveries, newest first",
		Args:  exactArgs(1, "gs webhook deliveries <webhook-id> [--limit n]"),
		RunE: func(cmd *cobra.Command, args []string) error {
			return r.runWebhookDeliveries(cmd.Context(), *opts, args[0], deliveriesLimit)
		},
	}
	deliveriesCmd.Flags().Int32Var(&deliveriesLimit, "limit", 20, "how many (at most 100)")

	redeliverCmd := &cobra.Command{
		Use:   "redeliver <delivery-id>",
		Short: "Send a delivery's event again, now",
		Args:  exactArgs(1, "gs webhook redeliver <delivery-id>"),
		RunE: func(cmd *cobra.Command, args []string) error {
			return r.runWebhookRedeliver(cmd.Context(), *opts, args[0])
		},
	}

	webhookCmd.AddCommand(createCmd, listCmd, updateCmd, deleteCmd, pingCmd, deliveriesCmd, redeliverCmd)
	return webhookCmd
}

func parseBoolFlag(name, value string) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "true", "yes", "on", "1":
		return true, nil
	case "false", "no", "off", "0":
		return false, nil
	}
	return false, userError("invalid_argument", fmt.Sprintf("%s takes true or false, not %q", name, value), "")
}

// webhookSecret reads a secret from the first line of stdin when asked.
func (r Runner) webhookSecret(fromStdin bool) (string, error) {
	if !fromStdin {
		return "", nil
	}
	if r.Stdin == nil {
		return "", userError("secret_required", "--secret-stdin needs the secret on stdin", "")
	}
	line, err := bufio.NewReader(r.Stdin).ReadString('\n')
	if err != nil && err != io.EOF {
		return "", err
	}
	secret := strings.TrimRight(line, "\r\n")
	if secret == "" {
		return "", userError("secret_required", "--secret-stdin read an empty secret", "Pipe the secret in, for example: printf %s \"$SECRET\" | gs webhook create ...")
	}
	return secret, nil
}

func (r Runner) runWebhookCreate(ctx context.Context, opts commandOptions, slice, url string, events []string, secret string, active bool) error {
	cfg, conn, callCtx, err := r.authenticatedConn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	ref, err := r.tagSliceRef(callCtx, cfg, conn, slice)
	if err != nil {
		return err
	}
	hook, err := corev1.NewWebhookServiceClient(conn).CreateWebhook(callCtx, &corev1.CreateWebhookRequest{
		Slice:  ref,
		Url:    strings.TrimSpace(url),
		Events: events,
		Secret: secret,
		Active: &active,
	})
	if err != nil {
		return webhookError(err)
	}
	out := webhookToOutput(hook)
	if opts.jsonOutput() {
		return r.writeJSONOutput(opts, out)
	}
	if !opts.Quiet {
		fmt.Fprintf(r.Stdout, "created webhook %s for %s: %s on %s\n", out.ID, out.Slice, displayWebhookURL(out.URL), strings.Join(out.Events, ", "))
		fmt.Fprintf(r.Stdout, "test it: gs webhook ping %s\n", out.ID)
	}
	return nil
}

func (r Runner) runWebhookList(ctx context.Context, opts commandOptions, slice string) error {
	cfg, conn, callCtx, err := r.authenticatedConn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	ref, err := r.tagSliceRef(callCtx, cfg, conn, slice)
	if err != nil {
		return err
	}
	resp, err := corev1.NewWebhookServiceClient(conn).ListWebhooks(callCtx, &corev1.ListWebhooksRequest{Slice: ref})
	if err != nil {
		return webhookError(err)
	}
	out := make([]webhookOutput, 0, len(resp.Webhooks))
	for _, hook := range resp.Webhooks {
		out = append(out, webhookToOutput(hook))
	}
	if opts.jsonOutput() {
		return r.writeJSONOutput(opts, map[string]any{"webhooks": out})
	}
	if len(out) == 0 && !opts.Quiet {
		fmt.Fprintf(r.Stdout, "no webhooks on %s/%s\n", ref.Account, ref.Slice)
		return nil
	}
	for _, hook := range out {
		state := "active"
		if !hook.Active {
			state = "inactive"
		}
		if hook.HasSecret {
			state += ",signed"
		}
		last := "never delivered"
		if d := hook.LastDelivery; d != nil {
			last = deliverySummary(*d)
		}
		fmt.Fprintf(r.Stdout, "%s\t%s\t%s\t%s\t%s\n", hook.ID, displayWebhookURL(hook.URL), strings.Join(hook.Events, ","), state, last)
	}
	return nil
}

func (r Runner) runWebhookUpdate(ctx context.Context, opts commandOptions, req *corev1.UpdateWebhookRequest) error {
	_, conn, callCtx, err := r.authenticatedConn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	hook, err := corev1.NewWebhookServiceClient(conn).UpdateWebhook(callCtx, req)
	if err != nil {
		return webhookError(err)
	}
	out := webhookToOutput(hook)
	if opts.jsonOutput() {
		return r.writeJSONOutput(opts, out)
	}
	if !opts.Quiet {
		state := "active"
		if !out.Active {
			state = "inactive"
		}
		fmt.Fprintf(r.Stdout, "updated webhook %s: %s on %s (%s)\n", out.ID, displayWebhookURL(out.URL), strings.Join(out.Events, ", "), state)
	}
	return nil
}

func (r Runner) runWebhookDelete(ctx context.Context, opts commandOptions, id string) error {
	_, conn, callCtx, err := r.authenticatedConn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	if _, err := corev1.NewWebhookServiceClient(conn).DeleteWebhook(callCtx, &corev1.DeleteWebhookRequest{WebhookId: id}); err != nil {
		return webhookError(err)
	}
	if opts.jsonOutput() {
		return r.writeJSONOutput(opts, map[string]any{"deleted": id})
	}
	if !opts.Quiet {
		fmt.Fprintf(r.Stdout, "deleted webhook %s\n", id)
	}
	return nil
}

func (r Runner) runWebhookPing(ctx context.Context, opts commandOptions, id string) error {
	_, conn, callCtx, err := r.authenticatedConn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	delivery, err := corev1.NewWebhookServiceClient(conn).PingWebhook(callCtx, &corev1.PingWebhookRequest{WebhookId: id})
	if err != nil {
		return webhookError(err)
	}
	return r.writeDelivery(opts, delivery)
}

func (r Runner) runWebhookRedeliver(ctx context.Context, opts commandOptions, id string) error {
	_, conn, callCtx, err := r.authenticatedConn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	delivery, err := corev1.NewWebhookServiceClient(conn).RedeliverWebhookDelivery(callCtx, &corev1.RedeliverWebhookDeliveryRequest{DeliveryId: id})
	if err != nil {
		return webhookError(err)
	}
	return r.writeDelivery(opts, delivery)
}

// writeDelivery reports one attempt made now (ping or redeliver).
func (r Runner) writeDelivery(opts commandOptions, delivery *corev1.WebhookDelivery) error {
	out := deliveryToOutput(delivery)
	if opts.jsonOutput() {
		return r.writeJSONOutput(opts, out)
	}
	if !opts.Quiet {
		fmt.Fprintf(r.Stdout, "%s %s: %s\n", out.Event, out.ID, deliverySummary(out))
		if body := strings.TrimSpace(out.ResponseBody); body != "" {
			fmt.Fprintf(r.Stdout, "response: %s\n", body)
		}
	}
	if out.Status != "succeeded" {
		return userError("delivery_failed", "the delivery did not succeed: "+firstNonEmpty(out.Error, out.Status), "Check the endpoint, then gs webhook redeliver "+out.ID+".")
	}
	return nil
}

func (r Runner) runWebhookDeliveries(ctx context.Context, opts commandOptions, id string, limit int32) error {
	_, conn, callCtx, err := r.authenticatedConn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	resp, err := corev1.NewWebhookServiceClient(conn).ListWebhookDeliveries(callCtx, &corev1.ListWebhookDeliveriesRequest{WebhookId: id, Limit: limit})
	if err != nil {
		return webhookError(err)
	}
	out := make([]webhookDeliveryOutput, 0, len(resp.Deliveries))
	for _, d := range resp.Deliveries {
		out = append(out, deliveryToOutput(d))
	}
	if opts.jsonOutput() {
		return r.writeJSONOutput(opts, map[string]any{"deliveries": out})
	}
	if len(out) == 0 && !opts.Quiet {
		fmt.Fprintln(r.Stdout, "no deliveries yet")
		return nil
	}
	for _, d := range out {
		fmt.Fprintf(r.Stdout, "%s\t%s\t%s\t%s\n", d.ID, d.CreatedAt, d.Event, deliverySummary(d))
	}
	return nil
}

// displayWebhookURL hides query values, which often carry a token (--json
// prints the whole URL).
func displayWebhookURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.RawQuery == "" {
		return raw
	}
	var parts []string
	for _, pair := range strings.Split(u.RawQuery, "&") {
		name, _, _ := strings.Cut(pair, "=")
		parts = append(parts, name+"=...")
	}
	u.RawQuery = strings.Join(parts, "&")
	return u.String()
}

func deliverySummary(d webhookDeliveryOutput) string {
	var parts []string
	parts = append(parts, d.Status)
	if d.ResponseStatus != 0 {
		parts = append(parts, fmt.Sprintf("HTTP %d", d.ResponseStatus))
	}
	if d.DurationMs > 0 {
		parts = append(parts, fmt.Sprintf("%dms", d.DurationMs))
	}
	if d.Attempts > 1 {
		parts = append(parts, fmt.Sprintf("%d attempts", d.Attempts))
	}
	if d.Status == "pending" && d.NextAttemptAt != "" {
		parts = append(parts, "next try "+d.NextAttemptAt)
	}
	if d.Error != "" && d.Status != "succeeded" {
		parts = append(parts, d.Error)
	}
	return strings.Join(parts, ", ")
}

func webhookToOutput(hook *corev1.Webhook) webhookOutput {
	out := webhookOutput{
		ID:        hook.GetId(),
		URL:       hook.GetUrl(),
		Events:    hook.GetEvents(),
		Active:    hook.GetActive(),
		HasSecret: hook.GetHasSecret(),
		CreatedBy: hook.GetCreatedBy(),
		CreatedAt: hook.GetCreatedAt(),
		UpdatedAt: hook.GetUpdatedAt(),
	}
	if hook.GetSlice() != nil {
		out.Slice = hook.Slice.Account + "/" + hook.Slice.Slice
	}
	if out.Events == nil {
		out.Events = []string{}
	}
	if hook.GetLastDelivery() != nil {
		last := deliveryToOutput(hook.LastDelivery)
		out.LastDelivery = &last
	}
	return out
}

func deliveryToOutput(d *corev1.WebhookDelivery) webhookDeliveryOutput {
	return webhookDeliveryOutput{
		ID:             d.GetId(),
		WebhookID:      d.GetWebhookId(),
		Event:          d.GetEvent(),
		EventID:        d.GetEventId(),
		Status:         d.GetStatus(),
		Attempts:       d.GetAttempts(),
		ResponseStatus: d.GetResponseStatus(),
		Error:          d.GetError(),
		CreatedAt:      d.GetCreatedAt(),
		DeliveredAt:    d.GetDeliveredAt(),
		NextAttemptAt:  d.GetNextAttemptAt(),
		DurationMs:     d.GetDurationMs(),
		RequestBody:    d.GetRequestBody(),
		ResponseBody:   d.GetResponseBody(),
	}
}

func webhookError(err error) error {
	message := grpcstatus.Convert(err).Message()
	switch grpcstatus.Code(err) {
	case codes.InvalidArgument:
		return userError("invalid_argument", message, "")
	case codes.FailedPrecondition:
		return userError("failed_precondition", message, "")
	case codes.PermissionDenied:
		return userError("permission_denied", message, "Webhooks are managed by the slice's owners and admins.")
	case codes.NotFound:
		return userError("not_found", message, "")
	case codes.Unimplemented:
		return userError("unsupported", "this server does not support webhooks", "Upgrade the server.")
	}
	return err
}
