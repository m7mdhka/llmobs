import React, { useEffect, useState } from "react";
import { Button, Field, Input } from "@llmobs/ui";
import { login, ApiError, ssoProviders, ssoStartUrl, type Session, type SSOProvider } from "./api.js";
import { brand } from "@llmobs/brand";

export function LoginPage({ onSignedIn }: { onSignedIn: (s: Session) => void }): React.ReactElement {
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [providers, setProviders] = useState<SSOProvider[]>([]);

  useEffect(() => {
    let live = true;
    void ssoProviders().then((p) => {
      if (live) setProviders(p);
    });
    return () => {
      live = false;
    };
  }, []);

  async function submit(e: React.FormEvent): Promise<void> {
    e.preventDefault();
    setBusy(true);
    setError(null);
    try {
      const session = await login(email, password);
      onSignedIn(session);
    } catch (err) {
      if (err instanceof ApiError && err.status === 429) {
        setError("Too many attempts. Wait a moment and try again.");
      } else {
        setError("Invalid email or password.");
      }
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className="shell-login">
      <form className="shell-login__card" onSubmit={submit}>
        <div className="shell-login__brand">
          <span className="llm-brand__mark" aria-hidden />
          <span>{brand.name}</span>
        </div>
        <p className="shell-login__tagline">Sign in to your observability workspace.</p>
        <Field label="Email" htmlFor="email">
          <Input
            id="email"
            type="email"
            autoComplete="username"
            value={email}
            onChange={(e) => setEmail(e.target.value)}
            required
            autoFocus
          />
        </Field>
        <Field label="Password" htmlFor="password">
          <Input
            id="password"
            type="password"
            autoComplete="current-password"
            value={password}
            onChange={(e) => setPassword(e.target.value)}
            required
          />
        </Field>
        {error && (
          <div className="shell-login__error" role="alert">
            {error}
          </div>
        )}
        <Button type="submit" variant="primary" disabled={busy}>
          {busy ? "Signing in…" : "Sign in"}
        </Button>
        {providers.length > 0 && (
          <div className="shell-login__sso">
            {providers.map((p) => (
              <Button
                key={p.org_id}
                type="button"
                variant="secondary"
                onClick={() => {
                  window.location.href = ssoStartUrl(p.org_id);
                }}
              >
                Sign in with {p.name || "SSO"}
              </Button>
            ))}
          </div>
        )}
      </form>
    </div>
  );
}
