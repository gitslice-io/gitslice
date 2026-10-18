import { type SliceRef, type Int64String } from "./common";

export interface AgentDaemon {
  id?: string;
  account?: string;
  name?: string;
  runtime?: string;
  version?: string;
  status?: string;
  lastSeenAt?: string;
  createdAt?: string;
  // What gs agent rename (or the Agent daemons list) set; show it in place of
  // name when present. name stays what the daemon registers with.
  displayName?: string;
  // The slices that use this daemon as their CI daemon, among those the viewer
  // can read.
  ciSlices?: SliceRef[];
  // True when the viewer registered the daemon. Only those can host the
  // viewer's conversations; the others are listed because the viewer administers
  // their account.
  mine?: boolean;
}

export interface RenameDaemonRequest {
  daemonId?: string;
  // "" shows the registered name again.
  displayName?: string;
}

export interface Conversation {
  id?: string;
  daemonId?: string;
  sliceId?: string;
  slice?: SliceRef;
  title?: string;
  status?: string;
  workspaceSubdir?: string;
  createdAt?: string;
  updatedAt?: string;
  // Read-time annotation from the server: true when this conversation's daemon
  // currently holds a live Connect stream. False/undefined means the agent is
  // offline and the composer should be disabled.
  daemonOnline?: boolean;
}

export interface ConversationEvent {
  id?: string;
  conversationId?: string;
  seq?: Int64String;
  role?: string;
  type?: string;
  text?: string;
  dataJson?: string;
  createdAt?: string;
  // Correlates streamed token deltas with the persisted final event for the
  // same runtime item. Present on replay when the daemon supplied it.
  itemId?: string;
}

export interface ListDaemonsRequest {}

export interface ListDaemonsResponse {
  daemons?: AgentDaemon[];
}

export interface CreateConversationRequest {
  daemonId?: string;
  slice?: SliceRef;
  title?: string;
}

export interface ListConversationsRequest {
  slice?: SliceRef;
  daemonId?: string;
}

export interface ListConversationsResponse {
  conversations?: Conversation[];
}

export interface GetConversationRequest {
  conversationId?: string;
}

export interface CloseConversationRequest {
  conversationId?: string;
}

export interface SendAgentMessageRequest {
  conversationId?: string;
  text?: string;
}

export interface SendAgentMessageResponse {
  event?: ConversationEvent;
}

export interface StreamConversationRequest {
  conversationId?: string;
  afterSeq?: Int64String | number;
}

export interface GetConversationEventsRequest {
  conversationId?: string;
  afterSeq?: Int64String | number;
  beforeSeq?: Int64String | number;
  limit?: Int64String | number;
}

export interface GetConversationEventsResponse {
  conversation?: Conversation;
  events?: ConversationEvent[];
}
