import { useState, useEffect } from "react";
import { useParams, Link } from "react-router-dom";
import { getSlice, Slice } from "../api";

export function SliceDetailPage() {
  const { id } = useParams<{ id: string }>();
  const [slice, setSlice] = useState<Slice | null>(null);
  const [error, setError] = useState("");
  const [loading, setLoading] = useState(false);

  useEffect(() => {
    if (!id) return;
    setLoading(true);
    getSlice({ slice_id: id })
      .then(setSlice)
      .catch((e) => setError(String(e)))
      .finally(() => setLoading(false));
  }, [id]);

  if (loading) return <div className="muted">loading...</div>;
  if (error) return <div className="error-msg">{error}</div>;
  if (!slice) return <div className="muted">slice not found</div>;

  const ref = slice.ref;
  const def = slice.definition;

  return (
    <div>
      <div className="page-title">
        {ref?.account}/{ref?.slice}
      </div>

      <div className="box">
        <div className="box-header">definition</div>
        <div className="box-body">
          <div className="input-row">
            <label>visibility</label>
            <span>{def?.visibility}</span>
          </div>
          <div className="input-row">
            <label>version</label>
            <span className="mono">{def?.version}</span>
          </div>
          <div className="input-row">
            <label>hash</label>
            <span className="mono">{slice.definition_hash}</span>
          </div>
        </div>
      </div>

      <div className="box" style={{ marginTop: 12 }}>
        <div className="box-header">included paths</div>
        <div className="box-body">
          {(def?.included_paths || []).map((p) => (
            <div key={p}>
              <Link to={`/source/${ref?.account}${p}`}>{p}</Link>
            </div>
          ))}
        </div>
      </div>

      <div className="actions">
        <Link to={`/slices/${id}/settings`}>
          <button>edit settings</button>
        </Link>
      </div>
    </div>
  );
}
