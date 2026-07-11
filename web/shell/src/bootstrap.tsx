import React from "react";
import { createRoot } from "react-dom/client";
import "@llmobs/tokens/tokens.css";
import "@llmobs/ui/ui.css";
import "./shell.css";
import { App } from "./App.js";
import { initTheme, initLocale } from "./theme.js";
import { brand } from "@llmobs/brand";

initTheme();
initLocale();
document.title = brand.name;

const el = document.getElementById("root");
if (el) {
  createRoot(el).render(
    <React.StrictMode>
      <App />
    </React.StrictMode>,
  );
}
