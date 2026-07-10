import React, { useEffect, useMemo, useState } from "react";
import { Navigate, Route, Routes, useLocation, useNavigate } from "react-router-dom";
import {
  AppFrame,
  Button,
  Content,
  EmptyState,
  LoadingState,
  NavItem,
  NavRail,
  NavSection,
  Topbar,
} from "@llmobs/ui";
import { toggleTheme } from "@llmobs/tokens";
import { fetchRegistry, logout, type RegistryPlugin, type Session } from "./api.js";
import { brand } from "@llmobs/brand";
import { registerPlugins } from "./remoteLoader.js";
import { PluginRoute } from "./PluginRoute.js";

interface Props {
  session: Session;
  onSignedOut: () => void;
}

export function AppShell({ session, onSignedOut }: Props): React.ReactElement {
  const [plugins, setPlugins] = useState<RegistryPlugin[] | null>(null);
  const navigate = useNavigate();
  const location = useLocation();

  useEffect(() => {
    let alive = true;
    fetchRegistry()
      .then((ps) => {
        if (!alive) return;
        registerPlugins(ps);
        setPlugins(ps);
      })
      .catch(() => alive && setPlugins([]));
    return () => {
      alive = false;
    };
  }, []);

  const sections = useMemo(() => groupNav(plugins ?? []), [plugins]);
  const firstRoute = plugins?.flatMap((p) => p.nav)[0]?.path;

  async function signOut(): Promise<void> {
    await logout(session.csrfToken);
    onSignedOut();
  }

  return (
    <AppFrame>
      <Topbar brand={brand.name}>
        <Button size="sm" variant="ghost" onClick={() => toggleTheme()} aria-label="Toggle theme">
          Theme
        </Button>
        <span className="shell-user" title={session.user.email}>
          {session.user.email}
        </span>
        <Button size="sm" variant="secondary" onClick={signOut}>
          Sign out
        </Button>
      </Topbar>

      <NavRail>
        <NavSection>Workspace</NavSection>
        {plugins === null && <div className="shell-nav-loading">Loading plugins…</div>}
        {plugins !== null &&
          sections.map((sec) => (
            <React.Fragment key={sec.section}>
              {sec.section !== "__default" && <NavSection>{sec.section}</NavSection>}
              {sec.items.map((it) => (
                <NavItem
                  key={it.path}
                  active={location.pathname === it.path}
                  onSelect={() => navigate(it.path)}
                >
                  {it.label}
                </NavItem>
              ))}
            </React.Fragment>
          ))}
        {plugins !== null && plugins.length === 0 && (
          <div className="shell-nav-empty">No plugins installed</div>
        )}
      </NavRail>

      <Content>
        {plugins === null ? (
          <LoadingState title="Loading workspace…" />
        ) : (
          <Routes>
            <Route
              path="/"
              element={firstRoute ? <Navigate to={firstRoute} replace /> : <NoPlugins />}
            />
            {plugins.flatMap((p) =>
              p.nav.map((n) => (
                // Mount as a subtree so the plugin owns its internal routes
                // (e.g. /traces and /traces/:id).
                <Route key={n.path} path={`${n.path}/*`} element={<PluginRoute plugin={p} session={session} />} />
              )),
            )}
            <Route path="*" element={<NoPlugins />} />
          </Routes>
        )}
      </Content>
    </AppFrame>
  );
}

function NoPlugins(): React.ReactElement {
  return (
    <EmptyState
      title="No plugins yet"
      body={`This ${brand.name} kernel has no plugins installed. Install a plugin (e.g. tracing) and it will appear here automatically — the shell discovers nav and routes from the registry.`}
    />
  );
}

interface NavGroup {
  section: string;
  items: { path: string; label: string }[];
}

function groupNav(plugins: RegistryPlugin[]): NavGroup[] {
  const bySection = new Map<string, NavGroup>();
  for (const p of plugins) {
    for (const n of p.nav) {
      const key = n.section ?? "__default";
      if (!bySection.has(key)) bySection.set(key, { section: key, items: [] });
      bySection.get(key)!.items.push({ path: n.path, label: n.label });
    }
  }
  return [...bySection.values()];
}
