import { useState, useEffect } from "react";
import { useSearchParams, Link } from "react-router-dom";
import { listSlices, Slice } from "../api";

export function SliceListPage() {
  const [searchParams] = useSearchParams();
  const account = searchParams.get("account") || "";
  const [slices, setSlices] = useState<Slice[]>([]);
  const [error, setError] = useState("");
  const [loading, setLoading] = useState(false);

  useEffect(() => {
    if (!account) return;
    setLoading(true);
    setError("");
    listSlices({ account })
      .then((res) => setSlices(res.slices || []))
      .catch((e) => setError(String(e)))
      .finally(() => setLoading(false));
  }, [account]);

  return (
    <div>
      <div className="page-title">slices for {account || "(no account)"}</div>

      {!account && <div className="muted">set ?account= in URL</div>}

      {error && <div className="error-msg">{error}</div>}
      {loading && <div className="muted">loading...</div>}

      {slices.length > 0 && (
        <table>
          <thead>
            <tr>
              <th>slice</th>
              <th>visibility</th>
              <th>version</th>
              <th>included paths</th>
            </tr>
          </thead>
          <tbody>
            {slices.map((s) => (
              <tr key={s.id}>
                <td>
                  <Link to={`/slices/${s.id}`}>
                    {s.ref?.account}/{s.ref?.slice}
                  </Link>
                </td>
                <td className="muted">{s.definition?.visibility}</td>
                <td className="mono">{s.definition?.version}</td>
                <td className="mono">
                  {(s.definition?.included_paths || []).join(", ")}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      )}

      {!loading && slices.length === 0 && account && (
        <div className="muted">no slices found</div>
      )}
    </div>
  );
}
