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
)
