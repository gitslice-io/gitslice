const TOKEN_KEY = "gitslice_token";
const SUBJECT_KEY = "gitslice_subject";

export function getToken(): string | null {
  return sessionStorage.getItem(TOKEN_KEY);
}

export function getSubject(): string | null {
  return sessionStorage.getItem(SUBJECT_KEY);
}

export function setAuth(token: string, subjectId: string) {
  sessionStorage.setItem(TOKEN_KEY, token);
  sessionStorage.setItem(SUBJECT_KEY, subjectId);
}

export function clearAuth() {
  sessionStorage.removeItem(TOKEN_KEY);
  sessionStorage.removeItem(SUBJECT_KEY);
}

export function isLoggedIn(): boolean {
  return getToken() !== null;
}

async function rpc<T>(serviceMethod: string, body: unknown): Promise<T> {
  const token = getToken();
  const headers: Record<string, string> = {
    "Content-Type": "application/json",
  };
  if (token) {
    headers["Authorization"] = `Bearer ${token}`;
  }

  const res = await fetch(`/${serviceMethod}`, {
    method: "POST",
    headers,
    body: JSON.stringify(body),
  });

  if (!res.ok) {
    const text = await res.text();
    throw new Error(`${serviceMethod}: ${res.status} ${text}`);
  }

  return res.json();
}

export interface LoginRequest {
  dev_user: string;
}

export interface LoginResponse {
  token: string;
  subject_id: string;
}

export function login(req: LoginRequest): Promise<LoginResponse> {
  return rpc<LoginResponse>("gitslice.core.v1.FakeAccountService/Login", req);
}

export interface SliceRef {
  account: string;
  slice: string;
}

export interface SliceDefinition {
  slice_id: string;
  version: string;
  included_paths: string[];
  visibility: string;
}

export interface Slice {
  id: string;
  ref: SliceRef;
  definition: SliceDefinition;
  definition_hash: string;
}

export interface ListSlicesRequest {
  account: string;
  cursor?: string;
  page_size?: number;
}

export interface ListSlicesResponse {
  slices: Slice[];
  next_cursor: string;
}

export function listSlices(req: ListSlicesRequest): Promise<ListSlicesResponse> {
  return rpc<ListSlicesResponse>("gitslice.core.v1.SliceService/ListSlices", req);
}

export interface GetSliceRequest {
  slice_id: string;
}

export function getSlice(req: GetSliceRequest): Promise<Slice> {
  return rpc<Slice>("gitslice.core.v1.SliceService/GetSlice", req);
}

export interface UpdateSliceDefinitionRequest {
  slice_id: string;
  expected_definition_hash: string;
  definition: SliceDefinition;
}

export function updateSliceDefinition(
  req: UpdateSliceDefinitionRequest
): Promise<SliceDefinition> {
  return rpc<SliceDefinition>(
    "gitslice.core.v1.SliceService/UpdateSliceDefinition",
    req
  );
}

export interface TreeEntry {
  path: string;
  name: string;
  kind: string;
  mode: number;
  tree_id: string;
  blob_id: string;
  symlink_target: string;
  size: string;
  content_hash: string;
}

export interface GetRefRequest {
  ref_name: string;
}

export interface Ref {
  name: string;
  commit_id: string;
  updated_at: string;
  updated_by: string;
}

export function getRef(req: GetRefRequest): Promise<Ref> {
  return rpc<Ref>("gitslice.core.v1.RepositoryService/GetRef", req);
}

export interface Commit {
  id: string;
  parent_ids: string[];
  root_tree_id: string;
  author: string;
  message: string;
  created_at: string;
  changed_paths: string[];
}

export interface GetCommitRequest {
  commit_id: string;
}

export function getCommit(req: GetCommitRequest): Promise<Commit> {
  return rpc<Commit>("gitslice.core.v1.RepositoryService/GetCommit", req);
}

export interface ResolvePathRequest {
  commit_id: string;
  path: string;
}

export interface ResolvePathResponse {
  entry: TreeEntry;
}

export function resolvePath(
  req: ResolvePathRequest
): Promise<ResolvePathResponse> {
  return rpc<ResolvePathResponse>(
    "gitslice.core.v1.RepositoryService/ResolvePath",
    req
  );
}

export interface ListDirectoryRequest {
  commit_id: string;
  path: string;
  cursor?: string;
  page_size?: number;
}

export interface ListDirectoryResponse {
  entries: TreeEntry[];
  next_cursor: string;
}

export function listDirectory(
  req: ListDirectoryRequest
): Promise<ListDirectoryResponse> {
  return rpc<ListDirectoryResponse>(
    "gitslice.core.v1.RepositoryService/ListDirectory",
    req
  );
}

export interface ReadFileRequest {
  commit_id: string;
  path: string;
  offset?: string;
  length?: string;
}

export interface ReadFileResponse {
  data: string;
  offset: string;
  content_hash: string;
}

export function readFile(req: ReadFileRequest): Promise<ReadFileResponse> {
  return rpc<ReadFileResponse>(
    "gitslice.core.v1.RepositoryService/ReadFile",
    req
  );
}

export interface FileEdit {
  op: string;
  path: string;
  old_path: string;
  blob_id: string;
  content_hash: string;
  mode: number;
}

export interface Patchset {
  id: string;
  changeset_id: string;
  number: string;
  base_commit_id: string;
  author: string;
  created_at: string;
  changed_paths: string[];
  file_edits: FileEdit[];
  coverage: PathCoverage[];
  submit_requirements: SubmitRequirements;
  path_bases: PathBase[];
  read_set: PathSetEntry[];
  write_set: PathSetEntry[];
}

export interface PathCoverage {
  path: string;
  covering_slice_ids: string[];
}

export interface PathBase {
  path: string;
  base_commit_id: string;
  exists: boolean;
  entry_kind: string;
  mode: number;
  blob_id: string;
  content_hash: string;
  tree_id: string;
  symlink_target: string;
  entry_fingerprint: string;
  check: string;
}

export interface PathSetEntry {
  path: string;
  recursive: boolean;
}

export interface SubmitRequirements {
  required_approvals: string[];
  required_checks: string[];
  path_lock_ids: string[];
  source_slice_definition_hash: string;
  source_path_lock_set_hash: string;
}

export interface Changeset {
  id: string;
  authoring_slice: SliceRef;
  author: string;
  target_ref: string;
  base_commit_id: string;
  title: string;
  description: string;
  patchsets: Patchset[];
  current_patchset_id: string;
  current_patchset_number: string;
  status: string;
  affected_paths: string[];
  submit_requirements: SubmitRequirements;
  commit_id: string;
  pending_publish_id: string;
}

export interface CreateChangesetRequest {
  authoring_slice: SliceRef;
  target_ref: string;
  base_commit_id: string;
  title: string;
  description: string;
}

export function createChangeset(
  req: CreateChangesetRequest
): Promise<Changeset> {
  return rpc<Changeset>(
    "gitslice.core.v1.ChangesetService/CreateChangeset",
    req
  );
}

export interface GetChangesetRequest {
  changeset_id: string;
}

export function getChangeset(req: GetChangesetRequest): Promise<Changeset> {
  return rpc<Changeset>(
    "gitslice.core.v1.ChangesetService/GetChangeset",
    req
  );
}

export interface UpdateChangesetRequest {
  changeset_id: string;
  expected_current_patchset_id: string;
  base_commit_id: string;
  file_edits: FileEdit[];
}

export function updateChangeset(
  req: UpdateChangesetRequest
): Promise<Patchset> {
  return rpc<Patchset>(
    "gitslice.core.v1.ChangesetService/UpdateChangeset",
    req
  );
}

export interface SubmitChangesetRequest {
  changeset_id: string;
  expected_current_patchset_id: string;
}

export interface SubmitChangesetResponse {
  commit_id: string;
  target_ref: string;
  new_ref_commit_id: string;
  status: string;
  pending_publish_id: string;
}

export function submitChangeset(
  req: SubmitChangesetRequest
): Promise<SubmitChangesetResponse> {
  return rpc<SubmitChangesetResponse>(
    "gitslice.core.v1.ChangesetService/SubmitChangeset",
    req
  );
}

export interface AbandonChangesetRequest {
  changeset_id: string;
  reason: string;
}

export function abandonChangeset(
  req: AbandonChangesetRequest
): Promise<Record<string, never>> {
  return rpc(
    "gitslice.core.v1.ChangesetService/AbandonChangeset",
    req
  );
}

export interface UploadBlobRequest {
  content_hash: string;
  data: string;
}

export interface UploadBlobResponse {
  blob_id: string;
  content_hash: string;
  size: string;
}

export function uploadBlob(
  req: UploadBlobRequest
): Promise<UploadBlobResponse> {
  return rpc<UploadBlobResponse>(
    "gitslice.core.v1.BlobService/UploadBlob",
    req
  );
}
