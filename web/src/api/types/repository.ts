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
}

export interface ListDirectoryResponse {
  entries?: TreeEntry[];
  nextCursor?: string;
}

export interface ReadFileRequest {
  commitId?: string;
  path?: string;
  offset?: Int64String;
  length?: Int64String;
  rootTreeId?: string;
  slice?: SliceRef;
}

export interface RenderFileRequest {
  commitId?: string;
  path?: string;
  slice?: SliceRef;
}

// RenderFileResponse is a Markdown file as safe HTML, rendered and sanitized by
// the server; repository images carry data-repo-image (data-repo-srcset in a
// <picture>) instead of src.
export interface RenderFileResponse {
  html?: string;
  contentHash?: string;
}

export interface ReadFileResponse {
  data?: Base64String;
  offset?: Int64String;
  contentHash?: string;
}

export interface BlameFileRequest {
  // The file, as a repository path such as /acme/payment/api.go.
  path?: string;
  slice?: SliceRef;
  // The target ref; empty means refs/global/main.
  refName?: string;
}

// A run of lines last changed by one commit (start_line is 1-based).
export interface BlameRange {
  startLine?: number;
  lineCount?: number;
  commitId?: string;
}

export interface BlameFileResponse {
  // The head commit the file was read at.
  commitId?: string;
  path?: string;
  lineCount?: number;
  // In line order, covering every line.
  ranges?: BlameRange[];
  // Each commit the ranges name, with its author, message and changeset.
  commits?: Commit[];
  // The walk stopped before the file's first commit, so the oldest commit
  // named may only be where it ended.
  truncated?: boolean;
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
