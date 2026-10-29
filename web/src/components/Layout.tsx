import { useCallback, useEffect, useId, useRef, useState } from "react";
import { Link, Outlet, useLocation, useNavigate } from "react-router-dom";
import { api } from "../api/client";
import { useAuth } from "../auth";
import { usePolling } from "../hooks/usePolling";
import { DOCS_INDEX } from "../lib/help";
import { useTheme } from "../theme";
import { Icon } from "./icons";
import { LogoMark } from "./Logo";

function linkClass(active: boolean): string {
  return active ? "side-link active" : "side-link";
}

function initialOf(email: string | undefined): string {
  const c = (email ?? "").trim().charAt(0);
  return c ? c.toUpperCase() : "?";
}

export function Layout() {
  const { user, orgName, clearSession } = useAuth();
  const { theme, toggle: toggleTheme } = useTheme();
  const navigate = useNavigate();
  const { pathname } = useLocation();
  const [menuOpen, setMenuOpen] = useState(false);
  const [drawerOpen, setDrawerOpen] = useState(false);
  const menuId = useId();
  const menuRef = useRef<HTMLDivElement>(null);
  const toggleRef = useRef<HTMLButtonElement>(null);
  const sidebarRef = useRef<HTMLElement>(null);
  const [version, setVersion] = useState<string | null>(null);
  const [openIncidents, setOpenIncidents] = useState(0);

  useEffect(() => {
    let cancelled = false;
    api
      .status()
      .then((s) => {
        if (!cancelled) setVersion(s.version || null);
      })
      .catch(() => {
        // the footer simply omits the version
      });
    return () => {
      cancelled = true;
    };
  }, []);

  const loadIncidents = useCallback(async () => {
    try {
      const res = await api.incidents({ state: "open", limit: 100 });
      setOpenIncidents(res.incidents.length);
    } catch {
      // the badge is a convenience; keep the last value
    }
  }, []);
  usePolling(loadIncidents, 30_000, true, pathname);

  // Leaving a page closes the drawer and the account menu.
  useEffect(() => {
    setDrawerOpen(false);
    setMenuOpen(false);
  }, [pathname]);

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

  useEffect(() => {
    if (!drawerOpen) return;
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") {
        setDrawerOpen(false);
        toggleRef.current?.focus();
      }
    };
    document.addEventListener("keydown", onKey);
    sidebarRef.current?.querySelector<HTMLElement>("a[href]")?.focus();
    return () => document.removeEventListener("keydown", onKey);
  }, [drawerOpen]);

  const signOut = async () => {
    try {
      await api.logout();
    } catch {
      // still clear local session
    }
    clearSession();
    navigate("/login", { replace: true });
  };

  const onHosts =
    pathname === "/" ||
    pathname.startsWith("/hosts") ||
    pathname.startsWith("/projects") ||
    pathname.startsWith("/deploys");
  const onIncidents = pathname.startsWith("/incidents");
  const onChat = pathname.startsWith("/chat");
  const onHelp = pathname.startsWith("/help");
  const onSettings = pathname.startsWith("/settings");
  const dark = theme === "dark";

  return (
    <div className="app-shell">
      <a className="skip-link" href="#main">
        Skip to content
      </a>
      <aside
        ref={sidebarRef}
        id="app-sidebar"
        className={`sidebar${drawerOpen ? " open" : ""}`}
        aria-label="Sidebar"
      >
        <Link to="/" className="brand">
          <LogoMark />
          <span className="brand-name">Haetae</span>
        </Link>
        <nav className="side-nav" aria-label="Main">
          <p className="side-label" aria-hidden="true">
            Operate
          </p>
          <Link to="/" className={linkClass(onHosts)} aria-current={onHosts ? "page" : undefined}>
            <Icon name="hosts" />
            Hosts
          </Link>
          <Link
            to="/incidents"
            className={linkClass(onIncidents)}
            aria-current={onIncidents ? "page" : undefined}
          >
            <Icon name="incidents" />
            Incidents
            {openIncidents > 0 ? (
              <span className="side-count">
                <span aria-hidden="true">{openIncidents}</span>
                <span className="sr-only">{openIncidents} open</span>
              </span>
            ) : null}
          </Link>
          <Link to="/chat" className={linkClass(onChat)} aria-current={onChat ? "page" : undefined}>
            <Icon name="chat" />
            Chat
          </Link>
          <Link to="/help" className={linkClass(onHelp)} aria-current={onHelp ? "page" : undefined}>
            <Icon name="help" />
            Help
          </Link>
          <Link
            to="/settings"
            className={linkClass(onSettings)}
            aria-current={onSettings ? "page" : undefined}
          >
            <Icon name="settings" />
            Settings
          </Link>
        </nav>
        <div className="side-spacer" />
        <div className="side-foot">
          <span>Haetae{version ? ` ${version.startsWith("v") ? version : `v${version}`}` : ""}</span>
          <a href={DOCS_INDEX} target="_blank" rel="noopener noreferrer">
            Documentation
          </a>
        </div>
      </aside>
      <div
        className={`drawer-scrim${drawerOpen ? " open" : ""}`}
        aria-hidden="true"
        onClick={() => setDrawerOpen(false)}
      />
      <div className="content-col">
        <header className="topbar">
          <button
            ref={toggleRef}
            type="button"
            className="icon-btn menu-toggle"
            aria-label="Menu"
            aria-expanded={drawerOpen}
            aria-controls="app-sidebar"
            onClick={() => setDrawerOpen((o) => !o)}
          >
            <Icon name={drawerOpen ? "close" : "menu"} size={20} />
          </button>
          <Link to="/" className="brand topbar-brand">
            <LogoMark size={28} />
            <span className="brand-name">Haetae</span>
          </Link>
          {orgName ? (
            <span className="org-chip" title="Organisation">
              <Icon name="building" size={15} />
              <span>{orgName}</span>
            </span>
          ) : null}
          <div className="topbar-spacer" />
          <div className="topbar-actions">
            <button
              type="button"
              className="btn ask-btn"
              onClick={() => navigate("/chat", { state: { ask: true } })}
            >
              <Icon name="sparkle" size={16} />
              <span className="ask-label">Ask the operator</span>
            </button>
            <button
              type="button"
              className="icon-btn theme-toggle"
              aria-label={dark ? "Switch to light theme" : "Switch to dark theme"}
              aria-pressed={dark}
              onClick={toggleTheme}
            >
              <Icon name={dark ? "sun" : "moon"} size={19} />
            </button>
            <div className="menu" ref={menuRef}>
              <button
                type="button"
                className="user-btn"
                aria-haspopup="menu"
                aria-expanded={menuOpen}
                aria-controls={menuId}
                onClick={() => setMenuOpen((o) => !o)}
              >
                <span className="avatar" aria-hidden="true">
                  {initialOf(user?.email)}
                </span>
                <span className="user-email">{user?.email ?? "Account"}</span>
                <Icon name="chevron-down" size={14} />
              </button>
              {menuOpen ? (
                <div className="menu-panel" role="menu" id={menuId}>
                  <Link to="/settings" role="menuitem" onClick={() => setMenuOpen(false)}>
                    <Icon name="settings" size={16} />
                    Settings
                  </Link>
                  <button type="button" role="menuitem" onClick={() => void signOut()}>
                    <Icon name="logout" size={16} />
                    Sign out
                  </button>
                </div>
              ) : null}
            </div>
          </div>
        </header>
        <main className="app-main" id="main" tabIndex={-1}>
          <div className="route-view" key={pathname}>
            <Outlet />
          </div>
        </main>
        <footer className="app-footer">
          <span>Haetae{version ? ` ${version.startsWith("v") ? version : `v${version}`}` : ""}</span>
          <span aria-hidden="true">·</span>
          <Link to="/help">Help</Link>
          <span aria-hidden="true">·</span>
          <a href={DOCS_INDEX} target="_blank" rel="noopener noreferrer">
            Documentation
          </a>
        </footer>
      </div>
    </div>
  );
}
