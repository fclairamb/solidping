/**
 * Starter template offered when the page has no stylesheet yet. It is a
 * commented listing of the theming API — every custom property status0 reads,
 * plus the stable `sp-*` element hooks — so the editor itself is the
 * documentation. Keep it in sync with
 * web/docs/docs/features/status-pages.md ("Custom CSS").
 *
 * It must pass the server validation as is: the server rejects the text
 * "@import" anywhere, comments included, so the template never mentions it.
 */
export const STARTER_TEMPLATE = `/* Status page theme — every rule below is optional.
   These are the CSS custom properties the public page reads.
   Colors accept any CSS color (hex, rgb(), oklch(), …). */

:root {
  /* Brand — logo and outbound links */
  --brand: #e11d63;
  --brand-foreground: #ffffff;

  /* Page surface */
  --background: #f5f9fc;
  --foreground: #09121f;

  /* Cards (each section is a card) */
  --card: #ffffff;
  --card-foreground: #09121f;

  /* Hairlines and separators */
  --border: #dee3eb;

  /* Buttons, links and the maintenance state. The buttons paint
     --primary-gradient over --primary: if you set --primary, set the
     gradient too (a flat one drops the gradient). */
  /* --primary: #1e64ef; */
  /* --primary-foreground: #ffffff; */
  /* --primary-gradient: linear-gradient(#1e64ef, #1e64ef); */
  /* --gradient-foreground: #ffffff; */

  /* Status colors — the dots, badges and uptime bars */
  --status-ok: #16a34a;
  --status-warning: #eab308;
  --status-error: #dc2626;

  /* Corner radius used across the page */
  --radius: 0.625rem;
}

/* Visitors whose browser/OS asks for dark mode get these instead. */
.dark {
  --background: #060a13;
  --foreground: #edf2f9;
  --card: #0c131e;
  --card-foreground: #edf2f9;
  --border: #1f293a;
}

/* ---------------------------------------------------------------
   Element hooks — stable class names on the public page:

     .sp-logo        header logo (the <img> lives inside it)
     .sp-page-name   status page name next to the logo
     .sp-footer      footer container
     .sp-powered-by  "Powered by SolidPing" link
     .sp-version     version line
   --------------------------------------------------------------- */

/* Replace the logo: the simplest way is to upload it under Branding.
   url() only loads images and fonts from this site or from data: URIs —
   the page's security policy blocks external hosts. With a data: URI
   (Chromium / Safari): */
/*
.sp-logo img {
  content: url("data:image/svg+xml;base64,PHN2Zy8+");
}
*/

/* Same, works everywhere: hide the <img> and paint the wrapper. */
/*
.sp-logo img { display: none; }
.sp-logo {
  background: url("data:image/svg+xml;base64,PHN2Zy8+") center / contain no-repeat;
  width: 120px;
  height: 32px;
}
*/

/* Hide the version line (and, if you want, the SolidPing credit). */
/*
.sp-version { display: none; }
.sp-powered-by { display: none; }
*/

/* Anything else is fair game too — plain CSS against the live page.
   url() may only point at this site or data:. */
`;

/**
 * Mirrors the server rule (statuspages.validateCustomCSS): the text "@import"
 * is refused anywhere, case-insensitively, even inside a comment.
 */
export function customCssHasImport(css: string): boolean {
  return /@import/i.test(css);
}
