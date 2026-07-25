import React from "react";
import { createRoot } from "react-dom/client";
import { App } from "./App";
import "./styles.css";
import "./resource-explorer.css";
import "./knowledge-explorer.css";
import "./observability.css";
import "./responsive.css";
import "./admin-system.css";
import "./admin-dashboard-components.css";

createRoot(document.getElementById("root")!).render(
  <React.StrictMode>
    <App />
  </React.StrictMode>,
);
