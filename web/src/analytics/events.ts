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
  // Deployment environment ("production" / "staging"), registered as a super
  // property so it rides on every event. Mirrors the server's PropEnvironment.
  environment: "environment",
} as const;
