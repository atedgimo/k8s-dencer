import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import App from "./App";
import { ErrorBoundary } from "./components/ErrorBoundary";
import "./index.css";
import { applyTheme, storedTheme } from "./theme";

const root = document.getElementById("root");
if (!root) {
  throw new Error("#root not found");
}

applyTheme(storedTheme());

createRoot(root).render(
  <StrictMode>
    <ErrorBoundary>
      <App />
    </ErrorBoundary>
  </StrictMode>,
);
