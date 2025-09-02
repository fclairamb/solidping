import React from "react";
import ReactDOM from "react-dom/client";
import { Playground } from "./components/playground";
import "./index.css";
import { ThemeProvider } from "./components/ui/theme-provider";

ReactDOM.createRoot(document.getElementById("root")!).render(
  <React.StrictMode>
    <ThemeProvider>
      <Playground />
    </ThemeProvider>
  </React.StrictMode>,
);
