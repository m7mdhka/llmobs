import React, { useEffect, useState } from "react";
import { BrowserRouter } from "react-router-dom";
import { LoadingState } from "@llmobs/ui";
import { me, type Session } from "./api.js";
import { LoginPage } from "./LoginPage.js";
import { AppShell } from "./AppShell.js";

type AuthState = { status: "loading" } | { status: "anon" } | { status: "authed"; session: Session };

export function App(): React.ReactElement {
  const [auth, setAuth] = useState<AuthState>({ status: "loading" });

  useEffect(() => {
    let alive = true;
    me()
      .then((s) => alive && setAuth(s ? { status: "authed", session: s } : { status: "anon" }))
      .catch(() => alive && setAuth({ status: "anon" }));
    return () => {
      alive = false;
    };
  }, []);

  if (auth.status === "loading") {
    return <LoadingState title="Starting LLMObs…" />;
  }
  if (auth.status === "anon") {
    return <LoginPage onSignedIn={(session) => setAuth({ status: "authed", session })} />;
  }
  return (
    <BrowserRouter>
      <AppShell session={auth.session} onSignedOut={() => setAuth({ status: "anon" })} />
    </BrowserRouter>
  );
}
