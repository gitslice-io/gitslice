import { useState, useEffect, useCallback } from "react";
import { useParams, useNavigate } from "react-router-dom";
import {
  getChangeset,
  submitChangeset,
  abandonChangeset,
  Changeset,
  Patchset,
} from "../api";

const STATUS_COLORS: Record<string, string> = {
  draft: "status-draft",
  open: "status-open",
  merged: "status-merged",
  abandoned: "status-abandoned",
  pending_publish: "status-draft",
};

export function ChangesetDetailPage() {
  const { id } = useParams<{ id: string }>();
  const navigate = useNavigate();
  const [cs, setCs] = useState<Changeset | null>(null);
  const [error, setError] = useState("");
  const [loading, setLoading] = useState(false);
  const [abandonReason, setAbandonReason] = useState("");

  const load = useCallback(async () => {
    if (!id) return;
    setLoading(true);
    setError("");
    try {
      const res = await getChangeset({ changeset_id: id });
      setCs(res);
    } catch (e) {
      setError(String(e));
    } finally {
      setLoading(false);
    }
  }, [id]);

  useEffect(() => {
    load();
  }, [load]);

  useEffect(() => {
    if (!cs || cs.status !== "pending_publish") return;
    const timer = setInterval(load, 3000);
    return () => clearInterval(timer);
  }, [cs?.status, load]);

  const handleSubmit = async () => {
    if (!cs) return;
    setError("");
    try {
      await submitChangeset({
        changeset_id: cs.id,
        expected_current_patchset_id: cs.current_patchset_id,
      });
      load();
    } catch (e) {
      setError(String(e));
    }
  };

  const handleAbandon = async () => {
    if (!cs) return;
    setError("");
    try {
      await abandonChangeset({
        changeset_id: cs.id,
        reason: abandonReason,
      });
      load();
    } catch (e) {
      setError(String(e));
    }
  };

  if (loading && !cs) return <div className="muted">loading...</div>;
  if (error && !cs) return <div className="error-msg">{error}</div>;
  if (!cs) return <div className="muted">changeset not found</div>;

  const currentPatchset = cs.patchsets?.find(
    (p) => p.id === cs.current_patchset_id
  );
  const statusClass = STATUS_COLORS[cs.status] || "";

  return (
    <div>
      <div className="page-title">
        <span className="mono">{cs.id}</span> {cs.title}
        <span className={`mono ${statusClass}`} style={{ marginLeft: 8 }}>
          [{cs.status}]
        </span>
      </div>

      <div className="box">
        <div className="box-header">details</div>
        <div className="box-body">
          <div className="input-row">
            <label>author</label>
            <span className="mono">{cs.author}</span>
          </div>
          <div className="input-row">
            <label>slice</label>
            <span>
              {cs.authoring_slice?.account}/{cs.authoring_slice?.slice}
            </span>
          </div>
          <div className="input-row">
            <label>target</label>
            <span className="mono">{cs.target_ref}</span>
          </div>
          <div className="input-row">
            <label>base</label>
            <span className="mono">{cs.base_commit_id || "-"}</span>
          </div>
          {cs.description && (
            <div className="input-row">
              <label>description</label>
              <span>{cs.description}</span>
            </div>
          )}
          {cs.commit_id && (
            <div className="input-row">
              <label>commit</label>
              <span className="mono">{cs.commit_id}</span>
            </div>
          )}
          {cs.pending_publish_id && (
            <div className="input-row">
              <label>publishing</label>
              <span className="mono status-draft">{cs.pending_publish_id}</span>
            </div>
          )}
        </div>
      </div>

      {cs.patchsets?.map((ps: Patchset) => (
        <div key={ps.id} className="box" style={{ marginTop: 12 }}>
          <div className="box-header">
            PS{ps.number}{" "}
            {ps.id === cs.current_patchset_id ? "[current]" : ""}
            <span className="muted" style={{ marginLeft: 8 }}>
              base {ps.base_commit_id?.slice(0, 12)} by {ps.author}{" "}
              {ps.created_at}
            </span>
          </div>
          <div className="box-body">
            {ps.file_edits && ps.file_edits.length > 0 && (
              <>
                <div className="section-title">file edits</div>
                <table>
                  <thead>
                    <tr>
                      <th>op</th>
                      <th>path</th>
                      <th>blob</th>
                      <th>hash</th>
                    </tr>
                  </thead>
                  <tbody>
                    {ps.file_edits.map((fe, i) => (
                      <tr key={i}>
                        <td className="mono">{fe.op}</td>
                        <td className="mono">{fe.path}</td>
                        <td className="mono muted">
                          {fe.blob_id ? fe.blob_id.slice(0, 12) : "-"}
                        </td>
                        <td className="mono muted">
                          {fe.content_hash ? fe.content_hash.slice(0, 16) : "-"}
                        </td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </>
            )}

            {ps.coverage && ps.coverage.length > 0 && (
              <>
                <div className="section-title">coverage</div>
                <table>
                  <thead>
                    <tr>
                      <th>path</th>
                      <th>covering slices</th>
                    </tr>
                  </thead>
                  <tbody>
                    {ps.coverage.map((c, i) => (
                      <tr key={i}>
                        <td className="mono">{c.path}</td>
                        <td className="mono muted">
                          {c.covering_slice_ids?.join(", ")}
                        </td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </>
            )}

            {ps.path_bases && ps.path_bases.length > 0 && (
              <>
                <div className="section-title">path bases</div>
                <table>
                  <thead>
                    <tr>
                      <th>path</th>
                      <th>base commit</th>
                      <th>check</th>
                    </tr>
                  </thead>
                  <tbody>
                    {ps.path_bases.map((pb, i) => (
                      <tr key={i}>
                        <td className="mono">{pb.path}</td>
                        <td className="mono muted">
                          {pb.base_commit_id?.slice(0, 12)}
                        </td>
                        <td className="mono muted">{pb.check}</td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </>
            )}

            {ps.submit_requirements && (
              <>
                <div className="section-title">submit requirements</div>
                <div className="mono muted">
                  approvals:{" "}
                  {ps.submit_requirements.required_approvals?.length || "none"}
                  {" | "}
                  checks:{" "}
                  {ps.submit_requirements.required_checks?.length || "none"}
                  {" | "}
                  path locks:{" "}
                  {ps.submit_requirements.path_lock_ids?.length || "none"}
                </div>
              </>
            )}
          </div>
        </div>
      ))}

      {error && <div className="error-msg">{error}</div>}

      <div className="actions" style={{ marginTop: 12 }}>
        {cs.status === "draft" || cs.status === "open" ? (
          <>
            <button onClick={handleSubmit}>submit</button>
            <div className="input-row">
              <input
                value={abandonReason}
                onChange={(e) => setAbandonReason(e.target.value)}
                placeholder="reason..."
                style={{ width: 200 }}
              />
              <button className="danger" onClick={handleAbandon}>
                abandon
              </button>
            </div>
          </>
        ) : null}
        <button onClick={() => navigate("/changesets/new")}>
          add patchset
        </button>
      </div>
    </div>
  );
}
