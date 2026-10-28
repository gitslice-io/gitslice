export interface GetAuthStatusRequest {}

export interface GetAuthStatusResponse {
  subjectId?: string;
  // Account slugs the subject belongs to; the first is their personal account.
  accounts?: string[];
  needsUsername?: boolean;
  // The same accounts with their kind and the subject's role in each.
  memberships?: AccountMembership[];
}

export type AccountKind = "personal" | "organization" | "agent";

export type AccountRole = "owner" | "admin" | "writer" | "member" | "reader";

export interface AccountMembership {
  account?: string;
  kind?: string;
  role?: string;
}

export interface AccountMember {
  username?: string;
  subjectId?: string;
  role?: string;
  /** The display name on the member's own account profile, when set. */
  displayName?: string;
  /** person or agent. */
  kind?: string;
}

export interface ListAccountMembersRequest {
  account?: string;
}

export interface ListAccountMembersResponse {
  account?: string;
  // personal or organization.
  kind?: string;
  members?: AccountMember[];
}

export interface SetAccountMemberRequest {
  account?: string;
  username?: string;
  role?: string;
}

export interface SetAccountMemberResponse {
  member?: AccountMember;
}

export interface RemoveAccountMemberRequest {
  account?: string;
  username?: string;
}

export type RemoveAccountMemberResponse = Record<string, never>;

export interface AccountInvitation {
  account?: string;
  username?: string;
  role?: string;
  invitedBy?: string;
  createdAt?: string;
}

export interface InviteAccountMemberRequest {
  account?: string;
  username?: string;
  role?: string;
}

export interface InviteAccountMemberResponse {
  invitation?: AccountInvitation;
}

export interface ListAccountInvitationsRequest {
  account?: string;
}

export interface ListInvitationsResponse {
  invitations?: AccountInvitation[];
}

export type ListMyInvitationsRequest = Record<string, never>;

export interface RespondToInvitationRequest {
  account?: string;
  accept?: boolean;
}

export interface RespondToInvitationResponse {
  membership?: AccountMembership;
}

export interface CancelAccountInvitationRequest {
  account?: string;
  username?: string;
}

export interface AccountProfile {
  account?: string;
  kind?: string;
  displayName?: string;
  description?: string;
  website?: string;
  createdAt?: string;
  /** Names the uploaded picture; empty when the generated one is shown. */
  avatarVersion?: string;
}

export interface GetAccountProfileRequest {
  account?: string;
}

export interface SetAccountAvatarRequest {
  account?: string;
  /** The picture file; empty or absent removes the uploaded picture. */
  image?: Uint8Array;
}

export interface UpdateAccountProfileRequest {
  account?: string;
  displayName?: string;
  description?: string;
  website?: string;
}

export interface CreateOrganizationRequest {
  slug?: string;
}

export interface CreateOrganizationResponse {
  account?: string;
  members?: AccountMember[];
}

export interface CheckUsernameAvailableRequest {
  username?: string;
}

export interface CheckUsernameAvailableResponse {
  available?: boolean;
  normalized?: string;
  reason?: string;
}

export interface ChooseUsernameRequest {
  username?: string;
}

export interface ChooseUsernameResponse {
  subjectId?: string;
  account?: string;
}

export interface PendingClaim {
  agentSubjectId?: string;
  agentDisplayName?: string;
  account?: string;
  ownerEmail?: string;
  createdAt?: string;
}

export type ListPendingClaimsRequest = Record<string, never>;

export interface ListPendingClaimsResponse {
  claims?: PendingClaim[];
  // The caller's verified emails the claims were matched against.
  verifiedEmails?: string[];
}

export interface AcceptClaimRequest {
  agentSubjectId?: string;
}

export interface AcceptClaimResponse {
  account?: string;
}

export interface OwnedAgent {
  agentSubjectId?: string;
  agentDisplayName?: string;
  account?: string;
  registeredAt?: string;
  claimedAt?: string;
  lastActiveAt?: string;
}

export type ListOwnedAgentsRequest = Record<string, never>;

export interface ListOwnedAgentsResponse {
  agents?: OwnedAgent[];
}

export interface CompleteCliLoginRequest {
  code?: string;
}

export interface CompleteCliLoginResponse {
  subjectId?: string;
}
