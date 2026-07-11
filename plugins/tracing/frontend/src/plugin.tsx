import React from "react";
import { BrowserRouter, Route, Routes } from "react-router-dom";
import { createReactBinding } from "@llmobs/plugin-sdk/react";
import type { PluginMountContext } from "@llmobs/plugin-sdk";
import { TracesListPage } from "./TracesListPage.js";
import { TraceDetailPage } from "./TraceDetailPage.js";
import { SettingsPage } from "./SettingsPage.js";
import "./tracing.css";

// The exposed surface, now mounted through the FRAMEWORK-NEUTRAL contract (ADR-0030):
// the module exports `mount`, and React is the binding. Because a neutral surface renders
// in its own React root (not inside the shell's tree), it owns its router — we wrap our
// routes in a BrowserRouter based at the shell-supplied mount path (context.basePath, e.g.
// "/traces") so links/navigation stay under the shell's URL exactly as before. The list is
// at the index, a trace's tree at ":traceId", the settings tab at "settings" (J2). Every
// byte it renders still arrives through @llmobs/plugin-sdk(/react).
function TracingApp({ context }: { context: PluginMountContext }): React.ReactElement {
  return (
    <BrowserRouter basename={context.basePath}>
      <Routes>
        <Route index element={<TracesListPage />} />
        <Route path="settings" element={<SettingsPage />} />
        <Route path=":traceId" element={<TraceDetailPage />} />
      </Routes>
    </BrowserRouter>
  );
}

export const mount = createReactBinding(TracingApp);
