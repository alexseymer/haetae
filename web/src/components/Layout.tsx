import { useEffect, useId, useRef, useState } from "react";
import { Link, NavLink, Outlet, useNavigate } from "react-router-dom";
import { api } from "../api/client";
import { useAuth } from "../auth";

export function Layout() {
  const { user, orgName, clearSession } = useAuth();
  const navigate = useNavigate();
  const [menuOpen, setMenuOpen] = useState(false);
  const menuId = useId();
  const menuRef = useRef<HTMLDivElement>(null);

  useEffect(() => {
    if (!menuOpen) return;
    const onDoc = (e: MouseEvent) => {
      if (!menuRef.current?.contains(e.target as Node)) {
        setMenuOpen(false);
      }
    };
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") setMenuOpen(false);
    };
    document.addEventListener("mousedown", onDoc);
    document.addEventListener("keydown", onKey);
    return () => {
      document.removeEventListener("mousedown", onDoc);
      document.removeEventListener("keydown", onKey);
    };
  }, [menuOpen]);

  const signOut = async () => {
    try {
      await api.logout();
    } catch {
      // still clear local session
    }
    clearSession();
    navigate("/login", { replace: true });
  };

  return (
    <div>
      <header className="topbar">
        <Link to="/" className="brand">
          <span className="brand-name">Haetae</span>
          {orgName ? <span className="brand-org">{orgName}</span> : null}
        </Link>
        <nav className="topnav" aria-label="Main">
          <NavLink to="/" end>
            Hosts
          </NavLink>
          <NavLink to="/incidents">Incidents</NavLink>
          <NavLink to="/chat">Chat</NavLink>
        </nav>
        <div className="menu" ref={menuRef}>
          <button
            type="button"
            className="btn btn-ghost"
            aria-haspopup="menu"
            aria-expanded={menuOpen}
            aria-controls={menuId}
            onClick={() => setMenuOpen((o) => !o)}
          >
            {user?.email ?? "Account"}
          </button>
          {menuOpen ? (
            <div className="menu-panel" role="menu" id={menuId}>
              <NavLink
                to="/settings"
                role="menuitem"
                onClick={() => setMenuOpen(false)}
              >
                Settings
              </NavLink>
              <button type="button" role="menuitem" onClick={() => void signOut()}>
                Sign out
              </button>
            </div>
          ) : null}
        </div>
      </header>
      <Outlet />
    </div>
  );
}
