import {
  type SliceRef,
  type TreeEntry,
  type Int64String,
  type Base64String
} from "./common";

export interface Ref {
  name?: string;
  commitId?: string;
  updatedAt?: string;
  updatedBy?: string;
}

export interface Commit {
  id?: string;
  parentIds?: string[];
  rootTreeId?: string;
  author?: string;
  message?: string;
  createdAt?: string;
  changedPaths?: string[];
  // The changeset that landed the commit, when one did.
  changesetId?: string;
}

export interface ResolvePathRequest {
  commitId?: string;
  path?: string;
  rootTreeId?: string;
  slice?: SliceRef;
}

export interface ResolvePathResponse {
  entry?: TreeEntry;
}

export interface ListDirectoryRequest {
  commitId?: string;
  path?: string;
  cursor?: string;
  pageSize?: number;
  slice?: SliceRef;
  rootTreeId?: string;
  // recursive lists everything under path (files and directories, in path
  // order); the response's recursive flag says whether the server honored it.
  recursive?: boolean;
}

export interface ListDirectoryResponse {
  entries?: TreeEntry[];
  nextCursor?: string;
  recursive?: boolean;
}

export interface ReadFileRequest {
  commitId?: string;
  path?: string;
  offset?: Int64String;
  length?: Int64String;
  rootTreeId?: string;
  slice?: SliceRef;
}

export interface ReadFileResponse {
  data?: Base64String;
  offset?: Int64String;
  contentHash?: string;
}

export interface GetCommitRequest {
  commitId?: string;
}

export interface ListCommitsRequest {
  refName?: string;
  limit?: number;
  path?: string;
  slice?: SliceRef;
  pageToken?: string;
  followMoves?: boolean;
}

export interface ListCommitsResponse {
  commits?: Commit[];
  nextPageToken?: string;
}

export interface GetRefRequest {
  refName?: string;
}

export interface GetBlobStatusRequest {
  contentHashes?: string[];
  slice?: SliceRef;
}

export interface BlobRecord {
  id?: string;
  contentHash?: string;
  size?: Int64String;
  storageLocation?: string;
  state?: string;
}

export interface GetBlobStatusResponse {
  blobs?: BlobRecord[];
}

export interface UploadBlobRequest {
  contentHash?: string;
  data?: Base64String;
  slice?: SliceRef;
}

export interface UploadBlobResponse {
  blobId?: string;
  contentHash?: string;
  size?: Int64String;
}
