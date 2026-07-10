import React, { useState } from "react";
import { Button, Field, Input } from "@llmobs/ui";
import { login, ApiError, type Session } from "./api.js";

export function LoginPage({ onSignedIn }: { onSignedIn: (s: Session) => void }): React.ReactElement {
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

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
          <span>LLMObs</span>
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
      </form>
    </div>
  );
}
