import React from "react";
import { Route, Routes } from "react-router-dom";
import { TracesListPage } from "./TracesListPage.js";
import { TraceDetailPage } from "./TraceDetailPage.js";
import "./tracing.css";

// The exposed surface. The shell mounts this at "/traces/*", so these routes are
// relative to that mount: the list at the index, a trace's tree at ":traceId".
// Every byte it renders arrives through @llmobs/plugin-sdk.
export default function TracingPlugin(): React.ReactElement {
  return (
    <Routes>
      <Route index element={<TracesListPage />} />
      <Route path=":traceId" element={<TraceDetailPage />} />
    </Routes>
  );
}
