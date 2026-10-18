import React from "react";
import { createRoot } from "react-dom/client";
import { App } from "./App";
import "./styles.css";
import "./control/oauth-provider.css";
import "./resource-explorer.css";
import "./knowledge-explorer.css";
import "./observability.css";
import "./shell.css";

createRoot(document.getElementById("root")!).render(
  <React.StrictMode>
    <App />
  </React.StrictMode>,
);
