/**
 * Docs landing page (/docs/): the website's navy hero, one card per section
 * of the sidebar, the one-command install and the most visited guides. The
 * Introduction it replaced as the front page lives at /docs/intro.
 */
import React from "react";
import Link from "@docusaurus/Link";
import Layout from "@theme/Layout";
import Heading from "@theme/Heading";
import CodeBlock from "@theme/CodeBlock";
import styles from "./index.module.css";

/** A 24px stroke icon; children are Feather icon shapes (MIT). */
function Icon({ children }: { children: React.ReactNode }): React.JSX.Element {
  return (
    <svg
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth="2"
      strokeLinecap="round"
      strokeLinejoin="round"
      aria-hidden="true"
    >
      {children}
    </svg>
  );
}

const SECTIONS = [
  {
    id: "installation",
    title: "Installation",
    to: "/category/installation",
    text: "Docker, Docker Compose, Kubernetes, Linux and Windows binaries, and one-click platforms.",
    icon: (
      <>
        <path d="M21 15v4a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2v-4" />
        <polyline points="7 10 12 15 17 10" />
        <line x1="12" y1="15" x2="12" y2="3" />
      </>
    ),
  },
  {
    id: "configuration",
    title: "Configuration",
    to: "/configuration",
    text: "Environment variables, database, authentication, notifications and security settings.",
    icon: (
      <>
        <line x1="4" y1="21" x2="4" y2="14" />
        <line x1="4" y1="10" x2="4" y2="3" />
        <line x1="12" y1="21" x2="12" y2="12" />
        <line x1="12" y1="8" x2="12" y2="3" />
        <line x1="20" y1="21" x2="20" y2="16" />
        <line x1="20" y1="12" x2="20" y2="3" />
        <line x1="1" y1="14" x2="7" y2="14" />
        <line x1="9" y1="8" x2="15" y2="8" />
        <line x1="17" y1="16" x2="23" y2="16" />
      </>
    ),
  },
  {
    id: "features",
    title: "Features",
    to: "/category/features",
    text: "Check types, incidents, on-call, status pages, maintenance windows and more.",
    icon: <polyline points="22 12 18 12 15 21 9 3 6 12 2 12" />,
  },
  {
    id: "api",
    title: "API reference",
    to: "/api",
    text: "Every REST endpoint, generated from the OpenAPI spec the server ships with.",
    icon: (
      <>
        <polyline points="16 18 22 12 16 6" />
        <polyline points="8 6 2 12 8 18" />
      </>
    ),
  },
  {
    id: "cli",
    title: "CLI client",
    to: "/cli",
    text: "Manage checks from a terminal or a CI pipeline with the sp command.",
    icon: (
      <>
        <polyline points="4 17 10 11 4 5" />
        <line x1="12" y1="19" x2="20" y2="19" />
      </>
    ),
  },
  {
    id: "changelog",
    title: "Changelog",
    to: "/changelog",
    text: "What changed in each release, breaking changes first.",
    icon: (
      <>
        <circle cx="12" cy="12" r="10" />
        <polyline points="12 6 12 12 16 14" />
      </>
    ),
  },
];

const POPULAR = [
  { label: "Install with Docker", to: "/installation/docker" },
  { label: "Deploy on Kubernetes", to: "/installation/kubernetes" },
  { label: "Check types", to: "/features/check-types" },
  { label: "Notifications", to: "/configuration/notifications" },
  { label: "Status pages", to: "/features/status-pages" },
  { label: "On-call and escalation", to: "/features/on-call" },
  { label: "MCP server", to: "/features/mcp" },
  { label: "Migrate from Uptime Kuma", to: "/features/migrate/from-uptime-kuma" },
];

const HELP = [
  {
    id: "discussions",
    title: "Ask a question",
    text: "Setups, ideas and answers from other users on GitHub Discussions.",
    href: "https://github.com/fclairamb/solidping/discussions",
  },
  {
    id: "issues",
    title: "Report a bug",
    text: "Something broken, or missing from these pages? Open a GitHub issue.",
    href: "https://github.com/fclairamb/solidping/issues",
  },
  {
    id: "tour",
    title: "See it first",
    text: "A video and screenshots of the dashboard, recorded from the real UI.",
    to: "/tour",
  },
];

const DOCKER_RUN = `docker run -d \\
  --name solidping \\
  -p 4000:4000 \\
  -v solidping-data:/data \\
  ghcr.io/fclairamb/solidping:latest`;

const check = <polyline points="20 6 9 17 4 12" />;

export default function DocsHome(): React.JSX.Element {
  return (
    <Layout description="Install, configure and run SolidPing, the open-source uptime monitoring platform: guides, configuration reference, REST API and CLI.">
      <header className={`sp-aurora ${styles.hero}`}>
        <div className={`container ${styles.heroInner}`}>
          <span className={styles.badge}>Open source · AGPL-3.0 · Self-host or cloud</span>
          <Heading as="h1" className={styles.heroTitle}>
            SolidPing documentation
          </Heading>
          <p className={styles.heroLead}>
            Install SolidPing, configure it and monitor your first service. Every setting, check
            type and API endpoint is documented here.
          </p>
          <div className={styles.heroCtas}>
            <Link
              className="button button--primary button--lg"
              to="/intro"
              data-testid="docs-home-get-started"
            >
              Get started
            </Link>
            <Link
              className="button button--onNavy button--lg"
              to="/api"
              data-testid="docs-home-api-reference"
            >
              API reference
            </Link>
          </div>
          <ul className={styles.heroMeta}>
            <li>
              <Icon>{check}</Icon>Free to self-host
            </li>
            <li>
              <Icon>{check}</Icon>One binary, SQLite or PostgreSQL
            </li>
            <li>
              <Icon>{check}</Icon>
              <a href="https://solidping.io/demo" data-testid="docs-home-demo">
                Live demo, no signup
              </a>
            </li>
          </ul>
        </div>
      </header>

      <main>
        <section className={styles.section}>
          <div className="container">
            <Heading as="h2" className={styles.sectionTitle}>
              Browse the docs
            </Heading>
            <div className={styles.grid}>
              {SECTIONS.map((section) => (
                <Link
                  key={section.id}
                  to={section.to}
                  className={styles.card}
                  data-testid={`docs-home-section-${section.id}`}
                >
                  <span className={styles.cardIcon}>
                    <Icon>{section.icon}</Icon>
                  </span>
                  <span className={styles.cardTitle}>{section.title}</span>
                  <span className={styles.cardText}>{section.text}</span>
                </Link>
              ))}
            </div>
          </div>
        </section>

        <section className={`${styles.section} ${styles.sectionAlt}`}>
          <div className={`container ${styles.split}`}>
            <div>
              <Heading as="h2" className={styles.sectionTitle}>
                Run it in one command
              </Heading>
              <p className={styles.sectionLead}>
                One container, its data in a volume, SQLite by default. Switch to PostgreSQL when
                you need it.
              </p>
              <CodeBlock language="bash">{DOCKER_RUN}</CodeBlock>
              <p className={styles.after}>
                Then open <code>http://localhost:4000</code> and sign in as{" "}
                <code>admin@solidping.io</code> / <code>solidpass</code>. The first login asks
                for a new password.
              </p>
              <Link
                to="/installation/docker"
                className={styles.more}
                data-testid="docs-home-docker-guide"
              >
                Full Docker guide →
              </Link>
            </div>
            <div>
              <Heading as="h2" className={styles.sectionTitle}>
                Popular pages
              </Heading>
              <ul className={styles.popular}>
                {POPULAR.map((page) => (
                  <li key={page.to}>
                    <Link to={page.to} data-testid="docs-home-popular-link">
                      {page.label}
                    </Link>
                  </li>
                ))}
              </ul>
            </div>
          </div>
        </section>

        <section className={styles.section}>
          <div className={`container ${styles.help}`}>
            {HELP.map((item) => (
              <Link
                key={item.id}
                {...(item.to ? { to: item.to } : { href: item.href })}
                className={styles.helpCard}
                data-testid={`docs-home-help-${item.id}`}
              >
                <span className={styles.helpTitle}>{item.title}</span>
                <span className={styles.cardText}>{item.text}</span>
              </Link>
            ))}
          </div>
        </section>
      </main>
    </Layout>
  );
}
