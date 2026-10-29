import { type SliceRef } from "./common";

// Where a secret applies: the folders in paths and everything below them
// (design/33_secrets.md). allSlices and slices are the scope before folders:
// accepted on writes, and read only from servers older than folders.
export interface SecretScope {
  paths?: string[];
  allSlices?: boolean;
  slices?: SliceRef[];
}

// An account's secret. Its value is never returned.
export interface Secret {
  id?: string;
  account?: string;
  name?: string;
  scope?: SecretScope;
  createdBy?: string;
  updatedBy?: string;
  createdAt?: string;
  updatedAt?: string;
}

export interface ListSecretsRequest {
  account?: string;
}

export interface ListSecretsResponse {
  secrets?: Secret[];
}

export interface CreateSecretRequest {
  account?: string;
  name?: string;
  value?: string;
  scope?: SecretScope;
}

// Leaving value or scope out keeps it.
export interface UpdateSecretRequest {
  id?: string;
  value?: string;
  scope?: SecretScope;
}

export interface SecretIdRequest {
  id?: string;
}

// A secret that applies somewhere in a slice, and its folders. allSlices and
// sharedWith come only from servers older than folders.
export interface SliceSecret {
  name?: string;
  secretId?: string;
  paths?: string[];
  allSlices?: boolean;
  sharedWith?: SliceRef[];
}

export interface ListSliceSecretsRequest {
  slice?: SliceRef;
}

export interface ListSliceSecretsResponse {
  names?: string[];
  // Filled only for the account's owners and admins.
  secrets?: SliceSecret[];
}
