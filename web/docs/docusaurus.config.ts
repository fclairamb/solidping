import { themes as prismThemes } from "prism-react-renderer";
import type { Config } from "@docusaurus/types";
import type * as Preset from "@docusaurus/preset-classic";
import type * as OpenApiPlugin from "docusaurus-plugin-openapi-docs";

// SolidPing documentation site — served at https://docs.solidping.io.
// This is the docs-only half of the former solidping-website Docusaurus site;
// the marketing landing, blog, and SaaS/legal pages stay on www.solidping.io.
// Docs live at the host root (baseUrl '/', routeBasePath '/') so the URL is
// docs.solidping.io/<page>, not docs.solidping.io/docs/<page>.
//
// The API reference under /api is generated at build time from the canonical
// spec at ../../server/internal/app/openapi/openapi.yaml (relative path, same
// repo — no cross-repo fetch), so it cannot drift from the server.
const config: Config = {
  title: "SolidPing Docs",
  tagline: "Documentation for SolidPing — open-source monitoring",
  favicon: "img/favicon.ico",

  url: "https://docs.solidping.io",
  // Docs are served at the /docs path on every host (like /dash0 and /status0),
  // so they work on the main domain (solidping.io/docs) with no extra infra.
  // docs.solidping.io redirects its root into /docs (see handlerWithDocsHost).
  baseUrl: "/docs/",

  organizationName: "fclairamb",
  projectName: "solidping",
  trailingSlash: false,

  onBrokenLinks: "throw",

  // Enable Mermaid so ```mermaid fences render as themed diagrams.
  markdown: {
    mermaid: true,
  },

  i18n: {
    defaultLocale: "en",
    locales: ["en"],
  },

  presets: [
    [
      "classic",
      {
        docs: {
          sidebarPath: "./sidebars.ts",
          // Render API reference pages with the OpenAPI theme component.
          docItemComponent: "@theme/ApiItem",
          // Docs ARE the site — serve them at the root, not under /docs.
          routeBasePath: "/",
          editUrl: "https://github.com/fclairamb/solidping/tree/main/web/docs/",
        },
        // Blog stays on www.solidping.io.
        blog: false,
        theme: {
          customCss: "./src/css/custom.css",
        },
      } satisfies Preset.Options,
    ],
  ],

  plugins: [
    [
      "docusaurus-plugin-llms",
      {
        generateLLMsTxt: true,
        generateLLMsFullTxt: true,
        generateMarkdownFiles: true,
        docsDir: "docs",
        ignoreFiles: [],
        title: "SolidPing",
        description:
          "SolidPing is an open-source monitoring platform for HTTP, TCP, and ping checks with status pages, incident tracking, and notifications.",
        includeBlog: false,
      },
    ],
    [
      "docusaurus-plugin-openapi-docs",
      {
        id: "api",
        docsPluginId: "classic",
        config: {
          solidping: {
            // Relative path to the canonical spec in the same repo — no fetch.
            specPath: "../../server/internal/app/openapi/openapi.yaml",
            outputDir: "docs/api",
            hideSendButton: true,
          } satisfies OpenApiPlugin.Options,
        },
      },
    ],
  ],

  themes: [
    "docusaurus-theme-openapi-docs",
    "@docusaurus/theme-mermaid",
    [
      require.resolve("@easyops-cn/docusaurus-search-local"),
      {
        hashed: true, // long-term-cacheable index filenames
        docsRouteBasePath: "/", // docs are served at the site root (routeBasePath: "/")
        indexBlog: false, // blog is disabled
        highlightSearchTermsOnTargetPage: true,
      } satisfies import("@easyops-cn/docusaurus-search-local").PluginOptions,
    ],
  ],

  themeConfig: {
    colorMode: {
      defaultMode: "light",
      respectPrefersColorScheme: true,
    },
    // Match Mermaid's light/dark palettes to the site's color modes.
    mermaid: {
      theme: { light: "neutral", dark: "dark" },
    },
    navbar: {
      title: "SolidPing",
      logo: {
        alt: "SolidPing Logo",
        src: "img/logo.png",
      },
      items: [
        {
          href: "https://www.solidping.io",
          label: "Home",
          position: "left",
        },
        {
          href: "https://www.solidping.io/blog",
          label: "Blog",
          position: "left",
        },
        {
          href: "https://github.com/fclairamb/solidping",
          label: "GitHub",
          position: "right",
        },
      ],
    },
    footer: {
      style: "dark",
      links: [
        {
          title: "Documentation",
          items: [
            {
              label: "Getting Started",
              to: "/",
            },
            {
              label: "Installation",
              to: "/installation/docker",
            },
            {
              label: "Configuration",
              to: "/configuration",
            },
          ],
        },
        {
          title: "Community",
          items: [
            {
              label: "GitHub Discussions",
              href: "https://github.com/fclairamb/solidping/discussions",
            },
            {
              label: "GitHub Issues",
              href: "https://github.com/fclairamb/solidping/issues",
            },
          ],
        },
        {
          title: "SolidPing",
          items: [
            {
              label: "Home",
              href: "https://www.solidping.io",
            },
            {
              label: "Blog",
              href: "https://www.solidping.io/blog",
            },
            {
              label: "Terms of Service",
              href: "https://www.solidping.io/terms",
            },
          ],
        },
      ],
      copyright: `Copyright © ${new Date().getFullYear()} SolidPing.`,
    },
    prism: {
      theme: prismThemes.github,
      darkTheme: prismThemes.dracula,
      additionalLanguages: ["bash", "yaml", "docker", "json"],
    },
  } satisfies Preset.ThemeConfig,
};

export default config;
