import React from "react";
import { createRoot } from "react-dom/client";
import { UserPortalApp } from "./App";
import "../styles.css";
import "../observability.css";
import "../responsive.css";

createRoot(document.getElementById("root")!).render(
  <React.StrictMode>
    <UserPortalApp />
  </React.StrictMode>,
);
