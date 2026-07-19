package analytics

const (
	EventSliceViewed        = "slice_viewed"
	EventSliceCreated       = "slice_created"
	EventChangesetSubmitted = "changeset_submitted"
	EventChangesetMerged    = "changeset_merged"
	EventPatchsetPushed     = "patchset_pushed"
	EventAuthLogin          = "auth_login"
	EventCLILoginCompleted  = "cli_login_completed"
)

const (
	PropSliceID     = "slice_id"
	PropChangesetID = "changeset_id"
	PropPatchsetID  = "patchset_id"
	PropMethod      = "method"
	// PropEnvironment tags every event with its deployment environment
	// (e.g. "production" / "staging") so a single PostHog project can serve
	// multiple environments. Injected by the client, not per call site.
	PropEnvironment = "environment"
)
