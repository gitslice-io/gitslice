import { useState, useEffect } from "react";
import { useParams, useSearchParams, Link } from "react-router-dom";
import {
  getRef,
  resolvePath,
  listDirectory,
  readFile,
  listSlices,
  TreeEntry,
  Slice,
} from "../api";

function kindIcon(kind: string) {
  switch (kind) {
    case "ENTRY_KIND_DIRECTORY":
      return "dir";
    case "ENTRY_KIND_SYMLINK":
      return "lnk";
    default:
      return "   ";
  }
}

export function SourcePage() {
  const { "*": splat } = useParams();
  const [searchParams] = useSearchParams();
  const path = "/" + (splat || "");
  const account = path.split("/")[1] || "";
  const refName = searchParams.get("ref") || "main";
  const commitParam = searchParams.get("commit");

  const [commitId, setCommitId] = useState<string | null>(commitParam || null);
  const [entry, setEntry] = useState<TreeEntry | null>(null);
  const [entries, setEntries] = useState<TreeEntry[]>([]);
  const [fileContent, setFileContent] = useState("");
  const [slices, setSlices] = useState<Slice[]>([]);
  const [error, setError] = useState("");
  const [loading, setLoading] = useState(false);

  useEffect(() => {
    if (!account) return;
    let cancelled = false;

    async function load() {
      setLoading(true);
      setError("");
      try {
        let cid = commitId;
        if (!cid) {
          const ref = await getRef({ ref_name: refName });
          cid = ref.commit_id;
          if (!cancelled) setCommitId(cid);
        }
        if (!cid) return;

        const resolved = await resolvePath({ commit_id: cid, path });
        if (cancelled) return;
        setEntry(resolved.entry);

        if (resolved.entry.kind === "ENTRY_KIND_DIRECTORY") {
          const dir = await listDirectory({ commit_id: cid, path });
          if (cancelled) return;
          setEntries(dir.entries || []);
        } else if (resolved.entry.kind === "ENTRY_KIND_FILE") {
          const file = await readFile({ commit_id: cid, path });
          if (cancelled) return;
          const decoder = new TextDecoder();
          const bytes = Uint8Array.from(atob(file.data), (c) =>
            c.charCodeAt(0)
          );
          setFileContent(decoder.decode(bytes));
        }

        const sl = await listSlices({ account });
        if (cancelled) return;
        setSlices(sl.slices || []);
      } catch (e) {
        if (!cancelled) setError(String(e));
      } finally {
        if (!cancelled) setLoading(false);
      }
    }
    load();
    return () => {
      cancelled = true;
    };
  }, [path, commitId, refName, account]);

  const pathParts = path.split("/").filter(Boolean);
  const coveringSlices = slices.filter((s) =>
    (s.definition?.included_paths || []).some((ip: string) =>
      path.startsWith(ip)
    )
  );

  return (
    <div>
      <div className="page-title">source browser</div>

      <div className="breadcrumb">
        <Link to={`/source/${account}?ref=${refName}`}>{account}</Link>
        {pathParts.slice(1).map((part, i) => {
          const href = "/source/" + pathParts.slice(0, i + 2).join("/") + `?ref=${refName}`;
          return (
            <span key={i}>
              <span className="sep">/</span>
              <Link to={href}>{part}</Link>
            </span>
          );
        })}
        <span style={{ marginLeft: 12 }} className="muted">
          ref: {refName}
        </span>
      </div>

      {error && <div className="error-msg">{error}</div>}

      {loading && <div className="muted">loading...</div>}

      {entry && entry.kind === "ENTRY_KIND_DIRECTORY" && (
        <table>
          <thead>
            <tr>
              <th style={{ width: 40 }}></th>
              <th>name</th>
              <th>kind</th>
              <th>mode</th>
              <th className="muted">blob</th>
            </tr>
          </thead>
          <tbody>
            {pathParts.length > 1 && (
              <tr>
                <td>dir</td>
                <td colSpan={4}>
                  <Link
                    to={`/source/${pathParts.slice(0, -1).join("/")}?ref=${refName}`}
                  >
                    ..
                  </Link>
                </td>
              </tr>
            )}
            {entries.map((e) => (
              <tr key={e.path}>
                <td>{kindIcon(e.kind)}</td>
                <td>
                  {e.kind === "ENTRY_KIND_DIRECTORY" ? (
                    <Link to={`/source${e.path}?ref=${refName}`}>
                      {e.name}/
                    </Link>
                  ) : (
                    <Link to={`/source${e.path}?ref=${refName}`}>
                      {e.name}
                    </Link>
                  )}
                </td>
                <td className="muted">
                  {e.kind?.replace("ENTRY_KIND_", "").toLowerCase()}
                </td>
                <td className="muted mono">{e.mode?.toString(8)}</td>
                <td className="muted mono">
                  {e.blob_id ? e.blob_id.slice(0, 12) : "-"}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      )}

      {entry && entry.kind === "ENTRY_KIND_FILE" && fileContent && (
        <div className="file-view mono">
          <code>{fileContent}</code>
        </div>
      )}

      {entry && entry.kind === "ENTRY_KIND_FILE" && !fileContent && !loading && (
        <div className="muted">(empty file)</div>
      )}

      {coveringSlices.length > 0 && (
        <div className="box" style={{ marginTop: 12 }}>
          <div className="box-header">covering slices</div>
          <div className="box-body">
            {coveringSlices.map((s) => (
              <div key={s.id}>
                <Link to={`/slices/${s.id}`}>
                  {s.ref?.account}/{s.ref?.slice}
                </Link>
              </div>
            ))}
          </div>
        </div>
      )}
    </div>
  );
}
