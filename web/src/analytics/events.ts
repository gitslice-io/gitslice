export const AnalyticsEvents = {
  sliceViewed: "slice_viewed",
  sliceCreated: "slice_created",
  changesetSubmitted: "changeset_submitted",
  changesetMerged: "changeset_merged",
  patchsetPushed: "patchset_pushed",
  authLogin: "auth_login",
  cliLoginCompleted: "cli_login_completed",
} as const;

export const AnalyticsProps = {
  sliceId: "slice_id",
  changesetId: "changeset_id",
  patchsetId: "patchset_id",
  method: "method",
} as const;
