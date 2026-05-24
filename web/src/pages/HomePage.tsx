import { useState } from "react";
import { useNavigate, Link } from "react-router-dom";
import { isLoggedIn, getSubject } from "../api";

export function HomePage() {
  const [account, setAccount] = useState(
    () => sessionStorage.getItem("gitslice_account") || ""
  );
  const [csId, setCsId] = useState("");
  const navigate = useNavigate();
  const loggedIn = isLoggedIn();
  const subject = getSubject();

  const saveAccount = () => {
    sessionStorage.setItem("gitslice_account", account);
  };

  return (
    <div>
      <div className="page-title">gitslice console</div>

      <div className="box" style={{ maxWidth: 500, marginBottom: 12 }}>
        <div className="box-header">status</div>
        <div className="box-body">
          {loggedIn ? (
            <div>logged in as <span className="mono">{subject}</span></div>
          ) : (
            <div>
              not logged in.{" "}
              <Link to="/login">login</Link>
            </div>
          )}
        </div>
      </div>

      <div className="box" style={{ maxWidth: 500, marginBottom: 12 }}>
        <div className="box-header">account</div>
        <div className="box-body">
          <div className="input-row">
            <label>account</label>
            <input
              value={account}
              onChange={(e) => setAccount(e.target.value)}
              onBlur={saveAccount}
              placeholder="e.g. acme"
            />
          </div>
        </div>
      </div>

      <div style={{ display: "flex", gap: 8, marginBottom: 12 }}>
        <button
          onClick={() => {
            saveAccount();
            navigate(`/source/${account}`);
          }}
        >
          browse source
        </button>
        <button
          onClick={() => {
            saveAccount();
            navigate(`/slices?account=${account}`);
          }}
        >
          list slices
        </button>
        <button onClick={() => navigate("/changesets/new")}>
          new changeset
        </button>
      </div>

      <div className="box" style={{ maxWidth: 500 }}>
        <div className="box-header">open changeset</div>
        <div className="box-body">
          <div className="input-row">
            <label>id</label>
            <input
              value={csId}
              onChange={(e) => setCsId(e.target.value)}
              placeholder="cs_..."
              onKeyDown={(e) =>
                e.key === "Enter" && csId && navigate(`/changesets/${csId}`)
              }
            />
            <button
              onClick={() => csId && navigate(`/changesets/${csId}`)}
            >
              open
            </button>
          </div>
        </div>
      </div>
    </div>
  );
}
