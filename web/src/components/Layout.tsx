import { Link, useLocation } from "react-router-dom";
import { isLoggedIn, getSubject, clearAuth, getToken } from "../api";

const NAV_ITEMS = [
  { to: "/", label: "Home" },
  { to: "/source", label: "Source" },
  { to: "/slices", label: "Slices" },
  { to: "/changesets", label: "Changeset" },
];

export function Layout({ children }: { children: React.ReactNode }) {
  const location = useLocation();
  const loggedIn = isLoggedIn();
  const subject = getSubject();

  return (
    <div className="app-layout">
      <nav className="sidebar">
        <div className="sidebar-title">gitslice</div>
        {NAV_ITEMS.map((item) => (
          <Link
            key={item.to}
            to={item.to}
            className={`sidebar-link${location.pathname === item.to ? " active" : ""}`}
          >
            {item.label}
          </Link>
        ))}
      </nav>
      <div className="main-area">
        <header className="topbar">
          <span className="muted">ref: main</span>
          <div className="topbar-right">
            {loggedIn ? (
              <>
                <span className="muted">{subject}</span>
                <button
                  onClick={() => {
                    clearAuth();
                    window.location.reload();
                  }}
                >
                  logout
                </button>
              </>
            ) : (
              <Link to="/login">
                <button>login</button>
              </Link>
            )}
          </div>
        </header>
        <main className="page-content">{children}</main>
      </div>
    </div>
  );
}
