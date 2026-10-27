import { type SliceRef } from "./common";

// Where a secret applies: every slice of its account, or the slices listed
// (design/33_secrets.md).
export interface SecretScope {
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

// A secret that reaches one slice, and where it comes from.
export interface SliceSecret {
  name?: string;
  secretId?: string;
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
