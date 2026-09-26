---
model: opus
effort: medium
---

# The docs site is still pink: move /docs to the electric identity

Spec 6 of the "electric identity" series. The palette (hex column) comes from
[spec 01](2026-09-24-01-dash0-electric-identity-tokens-and-primitives.md)
§1 and §2, and the navy from
[spec 02](2026-09-24-02-dash0-electric-identity-app-chrome.md) §1. There is no
code dependency, so it can land in any order.

## Problem

The Docusaurus site in `web/docs` is embedded in the binary and served at
`/docs` on every host. It is themed pink in
[web/docs/src/css/custom.css](web/docs/src/css/custom.css):

- `--ifm-color-primary` is `#E91E63` in light, with shades `#AD1457` to
  `#F48FB1`, and `#F48FB1` in dark.
- The hero is a pink gradient (`#E91E63 → #AD1457`). Secondary buttons, the
  TOC active link, the sidebar active link tint, the feature card hover
  shadow and the announcement bar are all pink.
- The footer is a `#1a1a2e → #16213e` gradient, a purple-navy close to, but
  not the same as, the new sidebar navy.

A user who clicks "Docs" in the electric-blue dashboard lands on a pink site.

## Proposal

Colors only, in `custom.css`. No content, navigation or config changes
beyond what is listed.

- **Primary**:
  - Light `--ifm-color-primary: #1e64ef`. Dark `#57a8ff`.
  - Generate the six `-dark/-darker/-darkest/-light/-lighter/-lightest`
    shades from those two bases with Docusaurus' standard rule, or its palette
    generator. Don't hand-pick them.
  - `--ifm-link-color` and `--ifm-navbar-link-hover-color` follow.
  - Set `--docusaurus-highlighted-code-line-bg` to the primary at 10% (light)
    and 20% (dark).
- **Hero**: `linear-gradient(135deg, #0094d9 0%, #204ee3 60%, #3823a2 100%)`,
  the hero gradient from spec 01 §2. The white title and subtitle stay.
- **Secondary button, TOC active link, sidebar active tint, feature card hover
  shadow**: the new primary, with the same alphas as today.
- **Footer**: the sidebar navy,
  `linear-gradient(180deg, #0a1731 0%, #04091b 100%)`.
- **Announcement bar**: `linear-gradient(90deg, #00b0e4 0%, #175ee8 50%, #453cdb 100%)`
  (the accent gradient). Check white text contrast over its brightest part. If
  it is below 4.5:1, use the `--primary-gradient` stops
  (`#007bce → #175ee8 → #453cdb`) instead.
- **Navbar**: keep it theme-following, not navy. It holds the search box and
  many links, and a navy navbar in light mode is a bigger change than the app
  sidebar. As the signature, add a 2px bottom border painted with the accent
  gradient (`border-image` or a `::after`), in place of today's plain shadow.
- **Logo** (`img/logo.png`) and favicon: unchanged, crimson.
- Update the file's header comment. It still says "vibrant pink theme".

## Out of scope

- `img/social-card.jpg`, the OG image shared with the website. It is a
  static asset. Regenerating it belongs with the website spec.
- Docs content, including screenshots that show the old UI. Re-shoot them
  once specs 01 to 03 have landed, in a separate change.
- The marketing site (separate repo, own spec).

## Acceptance criteria

- [ ] `custom.css` has no `#E91E63` family value or `rgba(233, 30, 99, …)`
      left.
- [ ] Links meet 4.5:1 against the page in both themes.
- [ ] Hero, footer, announcement bar and navbar border use the palette
      above. The navbar border shows in both themes.
- [ ] `make build-docs` succeeds, and `/docs` served by the binary shows the
      new theme.

## Verification

- `bun run test:unit` and a full build in `web/docs`.
- Screenshots of the docs home, a long doc page (sidebar, TOC, code block),
  and the changelog in both themes, in the PR.
