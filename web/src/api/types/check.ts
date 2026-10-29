import { type Int64String } from "./common";

export interface CheckRun {
  id?: string;
  changesetId?: string;
  patchsetId?: string;
  checkName?: string;
  daemonId?: string;
  // The name daemonId is shown by.
  daemonName?: string;
  // Whether the daemon assigned to this run currently has a live connection.
  daemonOnline?: boolean;
  // When that daemon last connected, for explaining queued offline runs.
  daemonLastSeenAt?: string;
  provenance?: string;
  attempt?: number;
  status?: string;
  exitCode?: number;
  summary?: string;
  startedAt?: string;
  finishedAt?: string;
  durationMs?: Int64String;
  createdAt?: string;
  // Set when a trigger reports the check (design/34_triggers.md).
  triggerRunId?: string;
  detailsUrl?: string;
  annotations?: CheckAnnotation[];
}

export interface CheckAnnotation {
  path?: string;
  startLine?: number;
  endLine?: number;
  // notice, warning or failure.
  level?: string;
  message?: string;
}

export interface CheckRunLog {
  runId?: string;
  seq?: Int64String;
  stream?: string;
  chunk?: string;
  createdAt?: string;
}

export interface ListCheckRunsRequest {
  changesetId?: string;
  patchsetId?: string;
}

export interface ListCheckRunsResponse {
  runs?: CheckRun[];
}

export interface GetCheckRunRequest {
  runId?: string;
}

export interface RerunCheckRequest {
  runId?: string;
}

export interface StreamCheckRunRequest {
  runId?: string;
  afterSeq?: Int64String | number;
}
