import { useState, useEffect } from "react";
import { useParams, useNavigate } from "react-router-dom";
import { getSlice, updateSliceDefinition, Slice } from "../api";

export function SliceSettingsPage() {
  const { id } = useParams<{ id: string }>();
  const navigate = useNavigate();
  const [slice, setSlice] = useState<Slice | null>(null);
  const [visibility, setVisibility] = useState("account");
  const [paths, setPaths] = useState<string[]>([""]);
  const [error, setError] = useState("");
  const [success, setSuccess] = useState("");
  const [saving, setSaving] = useState(false);

  useEffect(() => {
    if (!id) return;
    getSlice({ slice_id: id }).then((s) => {
      setSlice(s);
      setVisibility(s.definition?.visibility || "account");
      setPaths(s.definition?.included_paths || [""]);
    }).catch((e) => setError(String(e)));
  }, [id]);

  const handleSave = async () => {
    if (!slice || !id) return;
    setSaving(true);
    setError("");
    setSuccess("");
    try {
      const filtered = paths.filter((p) => p.trim());
      await updateSliceDefinition({
        slice_id: id,
        expected_definition_hash: slice.definition_hash,
        definition: {
          slice_id: id,
          version: slice.definition?.version || "0",
          visibility,
          included_paths: filtered,
        },
      });
      setSuccess("definition saved");
      navigate(`/slices/${id}`);
    } catch (e) {
      setError(String(e));
    } finally {
      setSaving(false);
    }
  };

  if (!slice) return <div className="muted">loading...</div>;

  return (
    <div>
      <div className="page-title">
        slice settings: {slice.ref?.account}/{slice.ref?.slice}
      </div>
      <div className="muted mono">current hash: {slice.definition_hash}</div>

      <div className="box" style={{ marginTop: 12 }}>
        <div className="box-header">visibility</div>
        <div className="box-body">
          {["private", "account", "public"].map((v) => (
            <label key={v} style={{ display: "block", cursor: "pointer" }}>
              <input
                type="radio"
                name="visibility"
                value={v}
                checked={visibility === v}
                onChange={() => setVisibility(v)}
              />{" "}
              {v}
            </label>
          ))}
        </div>
      </div>

      <div className="box" style={{ marginTop: 12 }}>
        <div className="box-header">included paths</div>
        <div className="box-body">
          {paths.map((p, i) => (
            <div key={i} className="input-row">
              <input
                value={p}
                onChange={(e) => {
                  const next = [...paths];
                  next[i] = e.target.value;
                  setPaths(next);
                }}
                placeholder="/account/slice/path"
              />
              <button
                className="danger"
                onClick={() => setPaths(paths.filter((_, j) => j !== i))}
              >
                x
              </button>
            </div>
          ))}
          <button
            onClick={() => setPaths([...paths, ""])}
            style={{ marginTop: 4 }}
          >
            + add path
          </button>
        </div>
      </div>

      {error && <div className="error-msg">{error}</div>}
      {success && <div className="success-msg">{success}</div>}

      <div className="actions">
        <button onClick={handleSave} disabled={saving}>
          {saving ? "saving..." : "save definition"}
        </button>
      </div>
    </div>
  );
}
