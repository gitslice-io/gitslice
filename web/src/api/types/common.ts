export type Int64String = string;
export type Base64String = string;

export interface Empty {}

export interface SliceRef {
  account?: string;
  slice?: string;
}

export type EntryKind =
  | "ENTRY_KIND_UNSPECIFIED"
  | "ENTRY_KIND_FILE"
  | "ENTRY_KIND_DIRECTORY"
  | "ENTRY_KIND_SYMLINK";

export interface TreeEntry {
  path?: string;
  name?: string;
  kind?: EntryKind;
  mode?: number;
  treeId?: string;
  blobId?: string;
  symlinkTarget?: string;
  size?: Int64String;
  contentHash?: string;
}

export interface ActorAttribution {
  actingSubjectId?: string;
  actingUsername?: string;
  controllerSubjectId?: string;
  controllerUsername?: string;
  keyId?: string;
  daemonId?: string;
  daemonName?: string;
  conversationId?: string;
}
