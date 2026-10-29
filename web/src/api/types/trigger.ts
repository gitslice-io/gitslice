import type { Int64String } from "./common";

// Triggers (design/34_triggers.md): runs of .gitslice/triggers.yaml entries.

export interface TriggerDefinition {
  id?: string;
  name?: string;
  folder?: string;
  on?: string[];
  paths?: string[];
  changes?: string[];
  refs?: string[];
  secrets?: string[];
  network?: string[];
  checks?: string[];
  concurrency?: string;
  timeoutMs?: Int64String;
  retry?: number;
  action?: string;
  httpUrl?: string;
  worker?: string;
}

export interface TriggerFileProblem {
  folder?: string;
  file?: string;
  message?: string;
}

export interface ListTriggersRequest {
  path?: string;
  ref?: string;
}

export interface ListTriggersResponse {
  triggers?: TriggerDefinition[];
  problems?: TriggerFileProblem[];
  commitId?: string;
}

export interface TriggerRun {
  id?: string;
  account?: string;
  triggerId?: string;
  folder?: string;
  action?: string;
  eventId?: string;
  eventKind?: string;
  changesetId?: string;
  patchsetId?: string;
  commitId?: string;
  targetRef?: string;
  definitionCommitId?: string;
  // queued, running, waiting, succeeded, failed, errored, canceled, timed_out.
  status?: string;
  attempt?: number;
  maxAttempts?: number;
  error?: string;
  responseStatus?: number;
  createdAt?: string;
  startedAt?: string;
  finishedAt?: string;
  deadline?: string;
}

export interface ListTriggerRunsRequest {
  account?: string;
  triggerId?: string;
  changesetId?: string;
  limit?: number;
}

export interface ListTriggerRunsResponse {
  runs?: TriggerRun[];
}

export interface RunTriggerRequest {
  account?: string;
  triggerId?: string;
  ref?: string;
  changesetId?: string;
  inputs?: Record<string, string>;
}

export interface TriggerRunIdRequest {
  runId?: string;
}

export interface ListTriggerRunLogsRequest {
  runId?: string;
  afterSeq?: Int64String;
}

export interface TriggerRunLog {
  seq?: Int64String;
  stream?: string;
  chunk?: string;
  createdAt?: string;
}

export interface ListTriggerRunLogsResponse {
  logs?: TriggerRunLog[];
}
