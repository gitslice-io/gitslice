import type * as AgentProto from "../gen/proto/core/v1/agent_pb";
import type * as AuthProto from "../gen/proto/core/v1/auth_pb";
import type * as BlobProto from "../gen/proto/core/v1/blob_pb";
import type * as ChangesetProto from "../gen/proto/core/v1/changeset_pb";
import type * as CheckProto from "../gen/proto/core/v1/check_pb";
import type * as CommonProto from "../gen/proto/core/v1/common_pb";
import type * as RepositoryProto from "../gen/proto/core/v1/repository_pb";
import type * as SecretProto from "../gen/proto/core/v1/secret_pb";
import type * as SliceProto from "../gen/proto/core/v1/slice_pb";
import type * as AutomationProto from "../gen/proto/core/v1/automation_pb";
import type { NormalizedMessage, NormalizedRequest } from "./generated";

// Proto-derived API types for the web app (type-only imports keep the protobuf
// descriptors out of page chunks until ./lazyClient loads ./client).

export type Empty = NormalizedMessage<CommonProto.Empty>;
export type SliceRef = NormalizedMessage<CommonProto.SliceRef>;
export type TreeEntry = NormalizedMessage<CommonProto.TreeEntry>;
export type ActorAttribution = NormalizedMessage<CommonProto.ActorAttribution>;
export type EntryKind =
  | "ENTRY_KIND_UNSPECIFIED"
  | "ENTRY_KIND_FILE"
  | "ENTRY_KIND_DIRECTORY"
  | "ENTRY_KIND_SYMLINK";

// Auth & accounts
export type AccountMembership = NormalizedMessage<AuthProto.AccountMembership>;
export type AccountMember = NormalizedMessage<AuthProto.AccountMember>;
export type AccountInvitation = NormalizedMessage<AuthProto.AccountInvitation>;
export type AccountProfile = NormalizedMessage<AuthProto.AccountProfile>;
export type PendingClaim = NormalizedMessage<AuthProto.PendingClaim>;
export type OwnedAgent = NormalizedMessage<AuthProto.OwnedAgent>;
export type GetAuthStatusRequest = NormalizedRequest<AuthProto.GetAuthStatusRequest>;
export type GetAuthStatusResponse = NormalizedMessage<AuthProto.GetAuthStatusResponse>;
export type CheckUsernameAvailableRequest = NormalizedRequest<AuthProto.CheckUsernameAvailableRequest>;
export type CheckUsernameAvailableResponse = NormalizedMessage<AuthProto.CheckUsernameAvailableResponse>;
export type ChooseUsernameRequest = NormalizedRequest<AuthProto.ChooseUsernameRequest>;
export type ChooseUsernameResponse = NormalizedMessage<AuthProto.ChooseUsernameResponse>;
export type GetAccountProfileRequest = NormalizedRequest<AuthProto.GetAccountProfileRequest>;
export type UpdateAccountProfileRequest = NormalizedRequest<AuthProto.UpdateAccountProfileRequest>;
export type SetAccountAvatarRequest = NormalizedRequest<AuthProto.SetAccountAvatarRequest>;
export type ListAccountMembersRequest = NormalizedRequest<AuthProto.ListAccountMembersRequest>;
export type ListAccountMembersResponse = NormalizedMessage<AuthProto.ListAccountMembersResponse>;
export type ListPendingClaimsResponse = NormalizedMessage<AuthProto.ListPendingClaimsResponse>;
export type ListOwnedAgentsResponse = NormalizedMessage<AuthProto.ListOwnedAgentsResponse>;

// Repository & blobs
export type Ref = NormalizedMessage<RepositoryProto.Ref>;
export type Commit = NormalizedMessage<RepositoryProto.Commit>;
export type BlameRange = NormalizedMessage<RepositoryProto.BlameRange>;
export type BlobRecord = NormalizedMessage<BlobProto.BlobRecord>;
export type ResolvePathRequest = NormalizedRequest<RepositoryProto.ResolvePathRequest>;
export type ResolvePathResponse = NormalizedMessage<RepositoryProto.ResolvePathResponse>;
export type ListDirectoryRequest = NormalizedRequest<RepositoryProto.ListDirectoryRequest>;
export type ListDirectoryResponse = NormalizedMessage<RepositoryProto.ListDirectoryResponse>;
export type ReadFileRequest = NormalizedRequest<RepositoryProto.ReadFileRequest>;
export type ReadFileResponse = NormalizedMessage<RepositoryProto.ReadFileResponse>;
export type RenderFileRequest = NormalizedRequest<RepositoryProto.RenderFileRequest>;
export type RenderFileResponse = NormalizedMessage<RepositoryProto.RenderFileResponse>;
export type BlameFileRequest = NormalizedRequest<RepositoryProto.BlameFileRequest>;
export type BlameFileResponse = NormalizedMessage<RepositoryProto.BlameFileResponse>;
export type ListCommitsRequest = NormalizedRequest<RepositoryProto.ListCommitsRequest>;
export type ListCommitsResponse = NormalizedMessage<RepositoryProto.ListCommitsResponse>;

// Slices
export type Slice = NormalizedMessage<SliceProto.Slice>;
export type SliceDefinition = NormalizedMessage<SliceProto.SliceDefinition>;
export type SliceGitMirror = NormalizedMessage<SliceProto.SliceGitMirror>;
export type SliceMember = NormalizedMessage<SliceProto.SliceMember>;
export type SliceGroupMember = NormalizedMessage<SliceProto.SliceGroupMember>;
export type SharedSlice = NormalizedMessage<SliceProto.SharedSlice>;
export type SliceSecret = NormalizedMessage<SliceProto.SliceSecret>;
export type Tag = NormalizedMessage<SliceProto.Tag>;
export type ResolveSliceRequest = NormalizedRequest<SliceProto.ResolveSliceRequest>;
export type GetSliceRequest = NormalizedRequest<SliceProto.GetSliceRequest>;
export type ListSlicesRequest = NormalizedRequest<SliceProto.ListSlicesRequest>;
export type ListSlicesResponse = NormalizedMessage<SliceProto.ListSlicesResponse>;
export type CreateSliceRequest = NormalizedRequest<SliceProto.CreateSliceRequest>;
export type UpdateSliceDefinitionRequest = NormalizedRequest<SliceProto.UpdateSliceDefinitionRequest>;
export type SetSliceCIDaemonRequest = NormalizedRequest<SliceProto.SetSliceCIDaemonRequest>;
export type SetSliceDescriptionRequest = NormalizedRequest<SliceProto.SetSliceDescriptionRequest>;
export type SetSliceMergeOnSubmitRequest = NormalizedRequest<SliceProto.SetSliceMergeOnSubmitRequest>;
export type SetSliceGitMirrorRequest = NormalizedRequest<SliceProto.SetSliceGitMirrorRequest>;
export type ListSliceMembersResponse = NormalizedMessage<SliceProto.ListSliceMembersResponse>;
export type ListSharedSlicesResponse = NormalizedMessage<SliceProto.ListSharedSlicesResponse>;
export type ListSliceSecretsRequest = NormalizedRequest<SliceProto.ListSliceSecretsRequest>;
export type ListSliceSecretsResponse = NormalizedMessage<SliceProto.ListSliceSecretsResponse>;

// Changesets & stacks
export type SubmitRequirements = NormalizedMessage<ChangesetProto.SubmitRequirements>;
export type FileEdit = NormalizedMessage<ChangesetProto.FileEdit>;
export type PathCoverage = NormalizedMessage<ChangesetProto.PathCoverage>;
export type PathBase = NormalizedMessage<ChangesetProto.PathBase>;
export type PathSetEntry = NormalizedMessage<ChangesetProto.PathSetEntry>;
export type PatchsetConflict = NormalizedMessage<ChangesetProto.PatchsetConflict>;
export type Patchset = NormalizedMessage<ChangesetProto.Patchset>;
export type ChangesetApproval = NormalizedMessage<ChangesetProto.ChangesetApproval>;
export type ApprovalRequirement = NormalizedMessage<ChangesetProto.ApprovalRequirement>;
export type ApprovalStatus = NormalizedMessage<ChangesetProto.ApprovalStatus>;
export type Changeset = NormalizedMessage<ChangesetProto.Changeset>;
export type ChangesetStack = NormalizedMessage<ChangesetProto.ChangesetStack>;
export type ChangesetStackEntry = NormalizedMessage<ChangesetProto.ChangesetStackEntry>;
export type SubmitStackEntryResult = NormalizedMessage<ChangesetProto.SubmitStackEntryResult>;
export type PathIntent = NormalizedMessage<ChangesetProto.PathIntent>;
export type CreateChangesetRequest = NormalizedRequest<ChangesetProto.CreateChangesetRequest>;
export type GetChangesetRequest = NormalizedRequest<ChangesetProto.GetChangesetRequest>;
export type ListChangesetsRequest = NormalizedRequest<ChangesetProto.ListChangesetsRequest>;
export type ListChangesetsResponse = NormalizedMessage<ChangesetProto.ListChangesetsResponse>;
export type DiffChangesetRequest = NormalizedRequest<ChangesetProto.DiffChangesetRequest>;
export type DiffChangesetResponse = NormalizedMessage<ChangesetProto.DiffChangesetResponse>;
export type UpdateChangesetRequest = NormalizedRequest<ChangesetProto.UpdateChangesetRequest>;
export type UpdateChangesetInfoRequest = NormalizedRequest<ChangesetProto.UpdateChangesetInfoRequest>;
export type ApproveChangesetRequest = NormalizedRequest<ChangesetProto.ApproveChangesetRequest>;
export type ApproveChangesetResponse = NormalizedMessage<ChangesetProto.ApproveChangesetResponse>;
export type SubmitChangesetRequest = NormalizedRequest<ChangesetProto.SubmitChangesetRequest>;
export type SubmitChangesetResponse = NormalizedMessage<ChangesetProto.SubmitChangesetResponse>;
export type AbandonChangesetRequest = NormalizedRequest<ChangesetProto.AbandonChangesetRequest>;
export type CopyPathRequest = NormalizedRequest<ChangesetProto.CopyPathRequest>;
export type MovePathRequest = NormalizedRequest<ChangesetProto.MovePathRequest>;
export type PathOperationResponse = NormalizedMessage<ChangesetProto.PathOperationResponse>;
export type ListStacksRequest = NormalizedRequest<ChangesetProto.ListStacksRequest>;
export type ListStacksResponse = NormalizedMessage<ChangesetProto.ListStacksResponse>;

// Checks
export type CheckRun = NormalizedMessage<CheckProto.CheckRun>;
export type CheckAnnotation = NormalizedMessage<CheckProto.CheckAnnotation>;
export type CheckRunLog = NormalizedMessage<CheckProto.CheckRunLog>;
export type ListCheckRunsRequest = NormalizedRequest<CheckProto.ListCheckRunsRequest>;
export type ListCheckRunsResponse = NormalizedMessage<CheckProto.ListCheckRunsResponse>;
export type StreamCheckRunRequest = NormalizedRequest<CheckProto.StreamCheckRunRequest>;

// Agents & conversations
export type AgentDaemon = NormalizedMessage<AgentProto.AgentDaemon>;
export type Conversation = NormalizedMessage<AgentProto.Conversation>;
export type ConversationEvent = NormalizedMessage<AgentProto.ConversationEvent>;
export type ListDaemonsRequest = NormalizedRequest<AgentProto.ListDaemonsRequest>;
export type ListDaemonsResponse = NormalizedMessage<AgentProto.ListDaemonsResponse>;
export type CreateConversationRequest = NormalizedRequest<AgentProto.CreateConversationRequest>;
export type ListConversationsRequest = NormalizedRequest<AgentProto.ListConversationsRequest>;
export type ListConversationsResponse = NormalizedMessage<AgentProto.ListConversationsResponse>;
export type GetConversationEventsRequest = NormalizedRequest<AgentProto.GetConversationEventsRequest>;
export type GetConversationEventsResponse = NormalizedMessage<AgentProto.GetConversationEventsResponse>;
export type StreamConversationRequest = NormalizedRequest<AgentProto.StreamConversationRequest>;

// Secrets
export type SecretScope = NormalizedMessage<SecretProto.SecretScope>;
export type Secret = NormalizedMessage<SecretProto.Secret>;
export type ListSecretsRequest = NormalizedRequest<SecretProto.ListSecretsRequest>;
export type ListSecretsResponse = NormalizedMessage<SecretProto.ListSecretsResponse>;
export type CreateSecretRequest = NormalizedRequest<SecretProto.CreateSecretRequest>;
export type UpdateSecretRequest = NormalizedRequest<SecretProto.UpdateSecretRequest>;

// Triggers & folder policy
export type AutomationDefinition = NormalizedMessage<AutomationProto.AutomationDefinition>;
export type AutomationFileProblem = NormalizedMessage<AutomationProto.AutomationFileProblem>;
export type AutomationRun = NormalizedMessage<AutomationProto.AutomationRun>;
export type AutomationRunLog = NormalizedMessage<AutomationProto.AutomationRunLog>;
export type FolderOwnerRule = NormalizedMessage<AutomationProto.FolderOwnerRule>;
export type FolderCheckDefinition = NormalizedMessage<AutomationProto.FolderCheckDefinition>;
export type FolderSecretEntry = NormalizedMessage<AutomationProto.FolderSecretEntry>;
export type FolderPolicyProblem = NormalizedMessage<AutomationProto.FolderPolicyProblem>;
export type GetFolderPolicyRequest = NormalizedRequest<AutomationProto.GetFolderPolicyRequest>;
export type FolderPolicyResponse = NormalizedMessage<AutomationProto.FolderPolicyResponse>;
export type ListAutomationsRequest = NormalizedRequest<AutomationProto.ListAutomationsRequest>;
export type ListAutomationsResponse = NormalizedMessage<AutomationProto.ListAutomationsResponse>;
export type ListAutomationRunsRequest = NormalizedRequest<AutomationProto.ListAutomationRunsRequest>;
export type ListAutomationRunsResponse = NormalizedMessage<AutomationProto.ListAutomationRunsResponse>;
export type RunAutomationRequest = NormalizedRequest<AutomationProto.RunAutomationRequest>;
export type ListAutomationRunLogsRequest = NormalizedRequest<AutomationProto.ListAutomationRunLogsRequest>;
export type ListAutomationRunLogsResponse = NormalizedMessage<AutomationProto.ListAutomationRunLogsResponse>;
