// Minimal Connect-JSON client for the Gitslice API (gitslice.core.v1).
// The Worker talks to Gitslice exactly like the web app does: unary POSTs with
// a JSON body, bytes as base64, and a bearer API key.

export interface SliceRef {
  account: string;
  slice: string;
}

export interface FileEdit {
  op: "upsert" | "delete";
  path: string;
  blobId?: string;
  contentHash?: string;
  mode?: number;
}

export interface Changeset {
  id: string;
  handle?: string;
  status?: string;
  title?: string;
  description?: string;
  currentPatchsetId?: string;
  currentPatchsetNumber?: string;
  commitId?: string;
  submitBlockedReason?: string;
  affectedPaths?: string[];
}

export interface Patchset {
  id: string;
  number?: string;
}

export interface TreeEntry {
  path: string;
  name: string;
  kind?: "ENTRY_KIND_FILE" | "ENTRY_KIND_DIRECTORY" | "ENTRY_KIND_SYMLINK";
  mode?: number;
  treeId?: string;
  blobId?: string;
  contentHash?: string;
}

export interface Commit {
  id: string;
  message?: string;
  createdAt?: string;
}

export interface SubmitResult {
  status?: string;
  commitId?: string;
  newRefCommitId?: string;
}

export const TARGET_REF = "refs/global/main";

export class GitsliceError extends Error {
  constructor(
    readonly httpStatus: number,
    readonly code: string,
    message: string,
  ) {
    super(message);
  }

  get conflict(): boolean {
    // Stale path bases and same-path races surface as failed_precondition or
    // aborted with "conflict" in the message.
    return (
      (this.code === "failed_precondition" || this.code === "aborted") &&
      /conflict|stale|changed since|path base/i.test(this.message)
    );
  }

  get missingApproval(): boolean {
    return /required approvals/i.test(this.message);
  }
}

export class Gitslice {
  constructor(
    private readonly api: string,
    private readonly token: string,
  ) {}

  async call<T>(service: string, method: string, body: unknown): Promise<T> {
    let lastError: unknown;
    for (let attempt = 0; attempt < 4; attempt++) {
      const res = await fetch(`${this.api}/gitslice.core.v1.${service}/${method}`, {
        method: "POST",
        headers: {
          "content-type": "application/json",
          authorization: `Bearer ${this.token}`,
        },
        body: JSON.stringify(body),
      });
      const text = await res.text();
      if (res.ok) {
        return (text ? JSON.parse(text) : {}) as T;
      }
      let code = "unknown";
      let message = text;
      try {
        const parsed = JSON.parse(text) as { code?: string; message?: string };
        code = parsed.code ?? code;
        message = parsed.message ?? message;
      } catch {
        // not a Connect error body
      }
      lastError = new GitsliceError(res.status, code, message);
      // Cloud Run cold starts and overload: retry transient failures only.
      if (res.status !== 503 && res.status !== 502 && code !== "unavailable") {
        throw lastError;
      }
      await sleep(250 * 2 ** attempt);
    }
    throw lastError;
  }

  uploadBlob(slice: SliceRef, data: Uint8Array): Promise<{ blobId: string; contentHash: string }> {
    return this.call("BlobService", "UploadBlob", { slice, data: toBase64(data) });
  }

  createChangeset(slice: SliceRef, baseCommitId: string, title: string, description: string): Promise<Changeset> {
    return this.call("ChangesetService", "CreateChangeset", {
      authoringSlice: slice,
      targetRef: TARGET_REF,
      baseCommitId,
      title,
      description,
    });
  }

  updateChangeset(changesetId: string, baseCommitId: string, fileEdits: FileEdit[], expectedCurrentPatchsetId?: string): Promise<Patchset> {
    return this.call("ChangesetService", "UpdateChangeset", {
      changesetId,
      baseCommitId,
      fileEdits,
      ...(expectedCurrentPatchsetId ? { expectedCurrentPatchsetId } : {}),
    });
  }

  abandon(changesetId: string): Promise<unknown> {
    return this.call("ChangesetService", "AbandonChangeset", { changesetId });
  }

  approve(changesetId: string): Promise<unknown> {
    return this.call("ChangesetService", "ApproveChangeset", { changesetId });
  }

  submit(changesetId: string, expectedCurrentPatchsetId: string): Promise<SubmitResult> {
    return this.call("ChangesetService", "SubmitChangeset", { changesetId, expectedCurrentPatchsetId });
  }

  getChangeset(changesetId: string): Promise<Changeset> {
    return this.call<{ changeset?: Changeset } & Changeset>("ChangesetService", "GetChangeset", { changesetId }).then(
      (res) => res.changeset ?? res,
    );
  }

  async headCommit(): Promise<string> {
    const res = await this.call<{ ref?: { commitId?: string }; commitId?: string }>("RepositoryService", "GetRef", {
      refName: TARGET_REF,
    });
    const id = res.ref?.commitId ?? res.commitId;
    if (!id) throw new Error("GetRef returned no commit");
    return id;
  }

  async listDirectory(commitId: string, path: string, slice: SliceRef): Promise<TreeEntry[]> {
    const out: TreeEntry[] = [];
    let cursor = "";
    do {
      const res = await this.call<{ entries?: TreeEntry[]; nextCursor?: string }>("RepositoryService", "ListDirectory", {
        commitId,
        path,
        slice,
        cursor,
        pageSize: 500,
      });
      out.push(...(res.entries ?? []));
      cursor = res.nextCursor ?? "";
    } while (cursor);
    return out;
  }

  // listCommits returns the commits visible through the slice, newest first.
  async listCommits(slice: SliceRef): Promise<Commit[]> {
    const out: Commit[] = [];
    let pageToken = "";
    do {
      const res = await this.call<{ commits?: Commit[]; nextPageToken?: string }>("RepositoryService", "ListCommits", {
        refName: TARGET_REF,
        slice,
        limit: 200,
        pageToken,
      });
      out.push(...(res.commits ?? []));
      pageToken = res.nextPageToken ?? "";
    } while (pageToken);
    return out;
  }

  // readFile returns null when the path does not exist at that commit.
  async readFile(commitId: string, path: string, slice: SliceRef): Promise<Uint8Array | null> {
    try {
      const res = await this.call<{ data?: string }>("RepositoryService", "ReadFile", { commitId, path, slice });
      return fromBase64(res.data ?? "");
    } catch (err) {
      if (err instanceof GitsliceError && (err.code === "not_found" || err.httpStatus === 404)) {
        return null;
      }
      throw err;
    }
  }
}

export function toBase64(data: Uint8Array): string {
  let binary = "";
  const chunk = 0x8000;
  for (let i = 0; i < data.length; i += chunk) {
    binary += String.fromCharCode(...data.subarray(i, i + chunk));
  }
  return btoa(binary);
}

export function fromBase64(text: string): Uint8Array {
  const binary = atob(text);
  const out = new Uint8Array(binary.length);
  for (let i = 0; i < binary.length; i++) out[i] = binary.charCodeAt(i);
  return out;
}

export function sleep(ms: number): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, ms));
}
