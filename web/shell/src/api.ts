// Thin same-origin API client for the shell's own needs: auth + the plugin
// registry. Plugins never use this — they get data through the plugin SDK. The
// kernel serves the shell and the API from the same origin, so paths are
// relative and the session cookie rides along automatically.

export interface CurrentUser {
  id: string;
  email: string;
  role: string;
}

export interface Session {
  user: CurrentUser;
  csrfToken: string;
}

// API base: same origin by default. A subpath deployment can inject a prefix at
// build time via PUBLIC_PATH; the API is always mounted at the app root here.
const API_BASE = "";

async function json<T>(res: Response): Promise<T> {
  const text = await res.text();
  const body = text ? JSON.parse(text) : undefined;
  if (!res.ok) {
    const code = (body && body.code) || String(res.status);
    const message = (body && body.message) || res.statusText;
    throw new ApiError(res.status, code, message);
  }
  return body as T;
}

export class ApiError extends Error {
  constructor(
    public readonly status: number,
    public readonly code: string,
    message: string,
  ) {
    super(message);
    this.name = "ApiError";
  }
}

export async function login(email: string, password: string): Promise<Session> {
  const res = await fetch(`${API_BASE}/auth/login`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    credentials: "same-origin",
    body: JSON.stringify({ email, password }),
  });
  const data = await json<{ user: CurrentUser; csrf_token: string }>(res);
  return { user: data.user, csrfToken: data.csrf_token };
}

export async function me(): Promise<Session | null> {
  const res = await fetch(`${API_BASE}/auth/me`, { credentials: "same-origin" });
  if (res.status === 401) return null;
  const data = await json<{ user: CurrentUser; csrf_token: string }>(res);
  return { user: data.user, csrfToken: data.csrf_token };
}

export async function logout(csrfToken: string): Promise<void> {
  await fetch(`${API_BASE}/auth/logout`, {
    method: "POST",
    headers: { "X-CSRF-Token": csrfToken },
    credentials: "same-origin",
  });
}

/** A plugin as advertised by the registry (the shell's runtime input). */
export interface RegistryPlugin {
  id: string;
  name: string;
  version: string;
  /** MF remote entry URL (absolute or app-relative). */
  remoteEntry: string;
  /** MF remote container name (the `name` in the plugin's MF config). */
  remoteName: string;
  /** The exposed module to load, e.g. "./plugin". */
  exposedModule: string;
  /** Subresource-integrity hash for the remote entry, when pinned. */
  integrity?: string;
  nav: NavEntry[];
}

export interface NavEntry {
  /** Route path the shell mounts the plugin surface at, e.g. "/traces". */
  path: string;
  /** Nav label. */
  label: string;
  /** Optional section grouping. */
  section?: string;
}

export async function fetchRegistry(): Promise<RegistryPlugin[]> {
  const res = await fetch(`${API_BASE}/v1alpha1/registry/plugins`, { credentials: "same-origin" });
  const data = await json<{ plugins: RegistryPlugin[] }>(res);
  return data.plugins ?? [];
}

// --- Plugin frontend token (J1) ---------------------------------------------
//
// The shell mints a per-plugin, per-session, short-TTL frontend token and hands a
// PROVIDER (not a bare token) to each plugin's SDK, so the token can transparently
// refresh before expiry. Its scopes are plugin-grant ∩ session ∩ project, computed
// kernel-side — least-privilege by default for a cooperating frontend.
//
// This is NOT a security boundary: a plugin loads in the shell's origin and can
// bypass the SDK with the ambient session cookie. It exists so a well-behaved
// frontend runs at least-privilege, and so pure-frontend plugins have a confined
// identity to persist settings under (J2). See docs/plugin-authors/trust-model.

interface FrontendTokenResponse {
  token: string;
  expiresUnix: number;
  scopes: string[];
}

export async function mintFrontendToken(
  pluginId: string,
  csrfToken: string,
  projectId?: string,
): Promise<FrontendTokenResponse> {
  const headers: Record<string, string> = { "Content-Type": "application/json", "X-CSRF-Token": csrfToken };
  if (projectId) headers["X-LLMObs-Project"] = projectId;
  const res = await fetch(`${API_BASE}/v1alpha1/plugin-frontend-token`, {
    method: "POST",
    headers,
    credentials: "same-origin",
    body: JSON.stringify({ plugin: pluginId }),
  });
  return json<FrontendTokenResponse>(res);
}

/**
 * makeFrontendTokenProvider returns a getter the SDK calls before each request. It
 * caches the current token and re-mints once it is within `skewMs` of expiry, so a
 * long-lived plugin surface never presents an expired token. On a mint failure it
 * returns undefined — and the SDK now FAILS CLOSED on undefined (G1): the plugin's
 * kernel call throws rather than silently running at the user's full session scope.
 * A persistent mint failure surfaces as the plugin surface's error/unavailable state,
 * never as an unconfined (full-privilege) data read.
 */
export function makeFrontendTokenProvider(
  pluginId: string,
  csrfToken: string,
  getProjectId: () => string | undefined,
  skewMs = 30_000,
): () => Promise<string | undefined> {
  let cached: FrontendTokenResponse | null = null;
  let expiresAtMs = 0;
  let inflight: Promise<FrontendTokenResponse> | null = null;

  return async () => {
    // now is read per-call; Date is fine in the browser (this is shell code, not a
    // kernel normalizer). A token within skewMs of expiry is treated as stale.
    if (cached && Date.now() < expiresAtMs - skewMs) return cached.token;
    if (!inflight) {
      inflight = mintFrontendToken(pluginId, csrfToken, getProjectId()).finally(() => {
        inflight = null;
      });
    }
    try {
      cached = await inflight;
      expiresAtMs = cached.expiresUnix * 1000;
      return cached.token;
    } catch {
      cached = null;
      return undefined;
    }
  };
}
