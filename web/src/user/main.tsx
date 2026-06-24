import React from "react";
import { createRoot } from "react-dom/client";
import { UserPortalApp } from "./App";
import "../styles.css";
import "../resource-explorer.css";
import "../knowledge-explorer.css";
import "../observability.css";
import "../responsive.css";

createRoot(document.getElementById("root")!).render(
  <React.StrictMode>
    <UserPortalApp />
  </React.StrictMode>,
);
