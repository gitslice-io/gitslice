import { useState } from "react";
import { useNavigate } from "react-router-dom";
import { login, setAuth } from "../api";

export function LoginPage() {
  const [devUser, setDevUser] = useState("alice");
  const [error, setError] = useState("");
  const navigate = useNavigate();

  const handleLogin = async () => {
    setError("");
    try {
      const res = await login({ dev_user: devUser || "alice" });
      setAuth(res.token, res.subject_id);
      navigate("/");
    } catch (e) {
      setError(String(e));
    }
  };

  return (
    <div className="box" style={{ maxWidth: 400 }}>
      <div className="box-header">gitslice dev login</div>
      <div className="box-body">
        <div className="input-row">
          <label>dev user</label>
          <input
            value={devUser}
            onChange={(e) => setDevUser(e.target.value)}
            onKeyDown={(e) => e.key === "Enter" && handleLogin()}
            autoFocus
          />
        </div>
        {error && <div className="error-msg">{error}</div>}
        <div className="actions">
          <button onClick={handleLogin}>login</button>
        </div>
      </div>
    </div>
  );
}
