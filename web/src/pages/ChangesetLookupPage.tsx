import { useState } from "react";
import { useNavigate } from "react-router-dom";
import { getChangeset } from "../api";

export function ChangesetLookupPage() {
  const [csId, setCsId] = useState("");
  const [error, setError] = useState("");
  const navigate = useNavigate();

  const handleOpen = async () => {
    setError("");
    try {
      await getChangeset({ changeset_id: csId });
      navigate(`/changesets/${csId}`);
    } catch (e) {
      setError(String(e));
    }
  };

  return (
    <div>
      <div className="page-title">open changeset</div>

      <div className="box" style={{ maxWidth: 500 }}>
        <div className="box-header">lookup</div>
        <div className="box-body">
          <div className="input-row">
            <label>changeset id</label>
            <input
              value={csId}
              onChange={(e) => setCsId(e.target.value)}
              placeholder="cs_..."
              onKeyDown={(e) => e.key === "Enter" && handleOpen()}
            />
            <button onClick={handleOpen}>open</button>
          </div>
        </div>
      </div>

      {error && <div className="error-msg">{error}</div>}

      <div style={{ marginTop: 12 }}>
        <button onClick={() => navigate("/changesets/new")}>
          create changeset
        </button>
      </div>
    </div>
  );
}
