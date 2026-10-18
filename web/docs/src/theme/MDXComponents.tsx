import React, { type ComponentProps } from "react";
import MDXComponents from "@theme-original/MDXComponents";

// Every Markdown table sits in a frame that scrolls sideways on its own, so a
// wide table (environment variables, API fields) never widens the page while
// a narrow one still spans the full column. Styled as .sp-table in
// src/css/custom.css.
export default {
  ...MDXComponents,
  table: (props: ComponentProps<"table">) => (
    <div className="sp-table">
      <table {...props} />
    </div>
  ),
};
