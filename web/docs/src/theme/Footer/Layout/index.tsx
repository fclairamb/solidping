/**
 * Swizzle of the theme's Footer/Layout, after the one on www.solidping.io
 * (solidping-website src/theme/Footer/Layout): the theme still builds the
 * links, logo and copyright from docusaurus.config.ts; this file only arranges
 * them. A brand column with the tagline sits left of the link columns, and the
 * copyright gets a bottom bar of its own.
 */
import React from "react";
import { ThemeClassNames } from "@docusaurus/theme-common";
import type { Props } from "@theme/Footer/Layout";
import styles from "./styles.module.css";

export default function FooterLayout({ style, links, logo, copyright }: Props): React.JSX.Element {
  const classes = [ThemeClassNames.layout.footer.container, "footer", styles.footer];
  if (style === "dark") classes.push("footer--dark");

  return (
    <footer className={classes.join(" ")}>
      <div className="container">
        <div className={styles.top}>
          <div className={styles.brand}>
            <div className={styles.brandLogo}>
              {logo}
              <span className={styles.brandName}>SolidPing</span>
            </div>
            <p className={styles.tagline}>
              Uptime monitoring, alerting, on-call and status pages. Open source, hosted in the EU.
            </p>
          </div>
          <div className={styles.links}>{links}</div>
        </div>
        {copyright && <div className={styles.bottom}>{copyright}</div>}
      </div>
    </footer>
  );
}
