import { useAuth } from "@clerk/clerk-react";
import { useCallback, useMemo } from "react";

import { callRpc, defaultApiBaseUrl, type RpcService } from "./client";
import type {
  AbandonChangesetRequest,
  ApproveChangesetRequest,
  ApproveChangesetResponse,
  Changeset,
  CheckUsernameAvailableRequest,
  CheckUsernameAvailableResponse,
  ChooseUsernameRequest,
  ChooseUsernameResponse,
  Commit,
  CompleteCliLoginRequest,
  CompleteCliLoginResponse,
  CreateChangesetRequest,
  CreateSliceRequest,
  DiffChangesetRequest,
  DiffChangesetResponse,
  Empty,
  GetAuthStatusRequest,
  GetAuthStatusResponse,
  GetBlobStatusRequest,
  GetBlobStatusResponse,
  GetChangesetRequest,
  GetCommitRequest,
  GetRefRequest,
  GetSliceRequest,
  ListCommitsRequest,
  ListCommitsResponse,
  ListChangesetsRequest,
  ListChangesetsResponse,
  ListDirectoryRequest,
  ListDirectoryResponse,
  ListSlicesRequest,
  ListSlicesResponse,
  Patchset,
  ReadFileRequest,
  ReadFileResponse,
  Ref,
  ResolvePathRequest,
  ResolvePathResponse,
  ResolveSliceRequest,
  Slice,
  SubmitChangesetRequest,
  SubmitChangesetResponse,
  UpdateChangesetRequest,
  UpdateSliceDefinitionRequest,
  UploadBlobRequest,
  UploadBlobResponse,
  SliceDefinition
} from "./types";

export interface ApiClient {
  getAuthStatus(
    request: GetAuthStatusRequest
  ): Promise<GetAuthStatusResponse>;
  checkUsernameAvailable(
    request: CheckUsernameAvailableRequest
  ): Promise<CheckUsernameAvailableResponse>;
  chooseUsername(
    request: ChooseUsernameRequest
  ): Promise<ChooseUsernameResponse>;
  completeCliLogin(
    request: CompleteCliLoginRequest
  ): Promise<CompleteCliLoginResponse>;
  resolvePath(request: ResolvePathRequest): Promise<ResolvePathResponse>;
  listDirectory(
    request: ListDirectoryRequest
  ): Promise<ListDirectoryResponse>;
  readFile(request: ReadFileRequest): Promise<ReadFileResponse>;
  getCommit(request: GetCommitRequest): Promise<Commit>;
  listCommits(request: ListCommitsRequest): Promise<ListCommitsResponse>;
  getRef(request: GetRefRequest): Promise<Ref>;
  getBlobStatus(
    request: GetBlobStatusRequest
  ): Promise<GetBlobStatusResponse>;
  uploadBlob(request: UploadBlobRequest): Promise<UploadBlobResponse>;
  resolveSlice(request: ResolveSliceRequest): Promise<Slice>;
  getSlice(request: GetSliceRequest): Promise<Slice>;
  listSlices(request: ListSlicesRequest): Promise<ListSlicesResponse>;
  createSlice(request: CreateSliceRequest): Promise<Slice>;
  updateSliceDefinition(
    request: UpdateSliceDefinitionRequest
  ): Promise<SliceDefinition>;
  createChangeset(request: CreateChangesetRequest): Promise<Changeset>;
  listChangesets(
    request: ListChangesetsRequest
  ): Promise<ListChangesetsResponse>;
  getChangeset(request: GetChangesetRequest): Promise<Changeset>;
  diffChangeset(
    request: DiffChangesetRequest
  ): Promise<DiffChangesetResponse>;
  updateChangeset(request: UpdateChangesetRequest): Promise<Patchset>;
  approveChangeset(
    request: ApproveChangesetRequest
  ): Promise<ApproveChangesetResponse>;
  submitChangeset(
    request: SubmitChangesetRequest
  ): Promise<SubmitChangesetResponse>;
  abandonChangeset(request: AbandonChangesetRequest): Promise<Empty>;
}

export function useApi(baseUrl = defaultApiBaseUrl): ApiClient {
  const { getToken } = useAuth();

  const invoke = useCallback(
    async <TRequest, TResponse>(
      service: RpcService,
      method: string,
      request: TRequest
    ) => {
      const token = await getToken();
      return callRpc<TRequest, TResponse>(
        service,
        method,
        request,
        token,
        baseUrl
      );
    },
    [baseUrl, getToken]
  );

  return useMemo(
    () => ({
      getAuthStatus: (request) =>
        invoke<GetAuthStatusRequest, GetAuthStatusResponse>(
          "AuthService",
          "GetAuthStatus",
          request
        ),
      checkUsernameAvailable: (request) =>
        invoke<CheckUsernameAvailableRequest, CheckUsernameAvailableResponse>(
          "AuthService",
          "CheckUsernameAvailable",
          request
        ),
      chooseUsername: (request) =>
        invoke<ChooseUsernameRequest, ChooseUsernameResponse>(
          "AuthService",
          "ChooseUsername",
          request
        ),
      completeCliLogin: (request) =>
        invoke<CompleteCliLoginRequest, CompleteCliLoginResponse>(
          "AuthService",
          "CompleteCliLogin",
          request
        ),
      resolvePath: (request) =>
        invoke<ResolvePathRequest, ResolvePathResponse>(
          "RepositoryService",
          "ResolvePath",
          request
        ),
      listDirectory: (request) =>
        invoke<ListDirectoryRequest, ListDirectoryResponse>(
          "RepositoryService",
          "ListDirectory",
          request
        ),
      readFile: (request) =>
        invoke<ReadFileRequest, ReadFileResponse>(
          "RepositoryService",
          "ReadFile",
          request
        ),
      getCommit: (request) =>
        invoke<GetCommitRequest, Commit>(
          "RepositoryService",
          "GetCommit",
          request
        ),
      listCommits: (request) =>
        invoke<ListCommitsRequest, ListCommitsResponse>(
          "RepositoryService",
          "ListCommits",
          request
        ),
      getRef: (request) =>
        invoke<GetRefRequest, Ref>("RepositoryService", "GetRef", request),
      getBlobStatus: (request) =>
        invoke<GetBlobStatusRequest, GetBlobStatusResponse>(
          "BlobService",
          "GetBlobStatus",
          request
        ),
      uploadBlob: (request) =>
        invoke<UploadBlobRequest, UploadBlobResponse>(
          "BlobService",
          "UploadBlob",
          request
        ),
      resolveSlice: (request) =>
        invoke<ResolveSliceRequest, Slice>(
          "SliceService",
          "ResolveSlice",
          request
        ),
      getSlice: (request) =>
        invoke<GetSliceRequest, Slice>("SliceService", "GetSlice", request),
      listSlices: (request) =>
        invoke<ListSlicesRequest, ListSlicesResponse>(
          "SliceService",
          "ListSlices",
          request
        ),
      createSlice: (request) =>
        invoke<CreateSliceRequest, Slice>(
          "SliceService",
          "CreateSlice",
          request
        ),
      updateSliceDefinition: (request) =>
        invoke<UpdateSliceDefinitionRequest, SliceDefinition>(
          "SliceService",
          "UpdateSliceDefinition",
          request
        ),
      createChangeset: (request) =>
        invoke<CreateChangesetRequest, Changeset>(
          "ChangesetService",
          "CreateChangeset",
          request
        ),
      listChangesets: (request) =>
        invoke<ListChangesetsRequest, ListChangesetsResponse>(
          "ChangesetService",
          "ListChangesets",
          request
        ),
      getChangeset: (request) =>
        invoke<GetChangesetRequest, Changeset>(
          "ChangesetService",
          "GetChangeset",
          request
        ),
      diffChangeset: (request) =>
        invoke<DiffChangesetRequest, DiffChangesetResponse>(
          "ChangesetService",
          "DiffChangeset",
          request
        ),
      updateChangeset: (request) =>
        invoke<UpdateChangesetRequest, Patchset>(
          "ChangesetService",
          "UpdateChangeset",
          request
        ),
      approveChangeset: (request) =>
        invoke<ApproveChangesetRequest, ApproveChangesetResponse>(
          "ChangesetService",
          "ApproveChangeset",
          request
        ),
      submitChangeset: (request) =>
        invoke<SubmitChangesetRequest, SubmitChangesetResponse>(
          "ChangesetService",
          "SubmitChangeset",
          request
        ),
      abandonChangeset: (request) =>
        invoke<AbandonChangesetRequest, Empty>(
          "ChangesetService",
          "AbandonChangeset",
          request
        )
    }),
    [invoke]
  );
}
