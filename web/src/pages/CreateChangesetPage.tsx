import { useState } from "react";
import { useNavigate } from "react-router-dom";
import {
  createChangeset,
  updateChangeset,
  uploadBlob,
  SliceRef,
  FileEdit,
} from "../api";

interface EditRow {
  op: string;
  path: string;
  oldPath: string;
  content: string;
}

async function sha256(message: string): Promise<string> {
  const msgBuffer = new TextEncoder().encode(message);
  const hashBuffer = await crypto.subtle.digest("SHA-256", msgBuffer);
  const hashArray = Array.from(new Uint8Array(hashBuffer));
  return hashArray.map((b) => b.toString(16).padStart(2, "0")).join("");
}

export function CreateChangesetPage() {
  const navigate = useNavigate();
  const [account, setAccount] = useState(
    () => sessionStorage.getItem("gitslice_account") || ""
  );
  const [slice, setSlice] = useState("");
  const [targetRef, setTargetRef] = useState("main");
  const [title, setTitle] = useState("");
  const [description, setDescription] = useState("");
  const [edits, setEdits] = useState<EditRow[]>([
    { op: "modify", path: "", oldPath: "", content: "" },
  ]);
  const [error, setError] = useState("");
  const [creating, setCreating] = useState(false);

  const handleCreate = async () => {
    setError("");
    setCreating(true);
    try {
      const authoringSlice: SliceRef = {
        account,
        slice,
      };

      const cs = await createChangeset({
        authoring_slice: authoringSlice,
        target_ref: targetRef,
        base_commit_id: "",
        title,
        description,
      });

      const fileEdits: FileEdit[] = [];
      for (const edit of edits) {
        if (!edit.path.trim()) continue;
        let blobId = "";
        let contentHash = "";

        if (edit.op !== "delete" && edit.content) {
          const hash = await sha256(edit.content);
          contentHash = `sha256:${hash}`;
          const b64 = btoa(edit.content);
          const blob = await uploadBlob({
            content_hash: contentHash,
            data: b64,
          });
          blobId = blob.blob_id;
        }

        fileEdits.push({
          op: edit.op,
          path: edit.path,
          old_path: edit.oldPath,
          blob_id: blobId,
          content_hash: contentHash,
          mode: 0,
        });
      }

      if (fileEdits.length > 0) {
        await updateChangeset({
          changeset_id: cs.id,
          expected_current_patchset_id: cs.current_patchset_id,
          base_commit_id: cs.base_commit_id,
          file_edits: fileEdits,
        });
      }

      navigate(`/changesets/${cs.id}`);
    } catch (e) {
      setError(String(e));
    } finally {
      setCreating(false);
    }
  };

  return (
    <div>
      <div className="page-title">new changeset</div>

      <div className="box" style={{ maxWidth: 600 }}>
        <div className="box-header">details</div>
        <div className="box-body">
          <div className="input-row">
            <label>account</label>
            <input
              value={account}
              onChange={(e) => {
                setAccount(e.target.value);
                sessionStorage.setItem("gitslice_account", e.target.value);
              }}
              placeholder="e.g. acme"
            />
          </div>
          <div className="input-row">
            <label>slice</label>
            <input
              value={slice}
              onChange={(e) => setSlice(e.target.value)}
              placeholder="e.g. payment"
            />
          </div>
          <div className="input-row">
            <label>target ref</label>
            <input
              value={targetRef}
              onChange={(e) => setTargetRef(e.target.value)}
            />
          </div>
          <div className="input-row">
            <label>title</label>
            <input
              value={title}
              onChange={(e) => setTitle(e.target.value)}
            />
          </div>
          <div className="input-row">
            <label>description</label>
            <textarea
              value={description}
              onChange={(e) => setDescription(e.target.value)}
            />
          </div>
        </div>
      </div>

      <div className="box" style={{ maxWidth: 600, marginTop: 12 }}>
        <div className="box-header">file edits</div>
        <div className="box-body">
          {edits.map((edit, i) => (
            <div
              key={i}
              className="box"
              style={{
                marginBottom: 8,
                borderColor: "var(--border-dim)",
              }}
            >
              <div className="box-body">
                <div className="input-row">
                  <label>op</label>
                  <select
                    value={edit.op}
                    onChange={(e) => {
                      const next = [...edits];
                      next[i] = { ...next[i], op: e.target.value };
                      setEdits(next);
                    }}
                  >
                    <option value="add">add</option>
                    <option value="modify">modify</option>
                    <option value="delete">delete</option>
                    <option value="rename">rename</option>
                  </select>
                </div>
                <div className="input-row">
                  <label>path</label>
                  <input
                    value={edit.path}
                    onChange={(e) => {
                      const next = [...edits];
                      next[i] = { ...next[i], path: e.target.value };
                      setEdits(next);
                    }}
                    placeholder="/account/slice/file.go"
                  />
                </div>
                {edit.op === "rename" && (
                  <div className="input-row">
                    <label>old path</label>
                    <input
                      value={edit.oldPath}
                      onChange={(e) => {
                        const next = [...edits];
                        next[i] = { ...next[i], oldPath: e.target.value };
                        setEdits(next);
                      }}
                    />
                  </div>
                )}
                {edit.op !== "delete" && (
                  <div>
                    <div className="muted" style={{ marginBottom: 4 }}>
                      content:
                    </div>
                    <textarea
                      value={edit.content}
                      onChange={(e) => {
                        const next = [...edits];
                        next[i] = { ...next[i], content: e.target.value };
                        setEdits(next);
                      }}
                      rows={4}
                      style={{ width: "100%" }}
                    />
                  </div>
                )}
                <button
                  className="danger"
                  onClick={() =>
                    setEdits(edits.filter((_, j) => j !== i))
                  }
                  style={{ marginTop: 4 }}
                >
                  remove edit
                </button>
              </div>
            </div>
          ))}
          <button
            onClick={() =>
              setEdits([
                ...edits,
                { op: "modify", path: "", oldPath: "", content: "" },
              ])
            }
          >
            + add edit
          </button>
        </div>
      </div>

      {error && <div className="error-msg">{error}</div>}

      <div className="actions">
        <button onClick={handleCreate} disabled={creating}>
          {creating ? "creating..." : "create draft"}
        </button>
      </div>
    </div>
  );
}
