import React from "react";
import { createRoot } from "react-dom/client";
import { UserPortalApp } from "./App";
import "../styles.css";
import "../resource-explorer.css";
import "./mcp-context.css";
import "../knowledge-explorer.css";
import "../graph-view.css";
import "../observability.css";
import "../responsive.css";
import "../admin-system.css";
import "../admin-dashboard-components.css";

createRoot(document.getElementById("root")!).render(
  <React.StrictMode>
    <UserPortalApp />
  </React.StrictMode>,
);
