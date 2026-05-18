# Badge minimum width option

## Goal

Add a `minWidth` query parameter that sets a floor on the total badge width.
Badges with short labels and short values (e.g., `up`) can look disproportionately
small when embedded alongside other badges. A minimum width pads the value side so
the badge reaches the requested pixel size.

## Why

People embed multiple badges in README files or status pages. When badges have varying
label lengths, the resulting mix of narrow and wide badges looks unbalanced. A minimum
width lets users match the visual weight of a row of badges without manually faking it
with extra spaces in the label.

## URL parameter

```
GET /api/v1/orgs/:org/checks/:check/badges/:components?minWidth=<int>
```

- Integer, unit: pixels.
- Range: 1–800. Values outside this range are clamped silently.
- Default: 0 (no minimum — identical to current behaviour).

The minimum applies to the **total** badge width (`labelWidth + valueWidth`). When the
computed width is already larger than `minWidth`, the parameter has no effect. When
smaller, the extra pixels are added to the value side.

## Backend

### `BadgeOptions` (`service.go`)

```go
type BadgeOptions struct {
    Period   string
    Label    string
    Style    string
    MinWidth int   // 0 = no minimum
}
```

### `handler.go`

Parse and clamp in `GetBadge`:

```go
if raw := req.URL.Query().Get("minWidth"); raw != "" {
    if v, err := strconv.Atoi(raw); err == nil {
        if v < 0 { v = 0 }
        if v > 800 { v = 800 }
        opts.MinWidth = v
    }
}
```

### `svg.go` — `GenerateSVG`

Add `minWidth int` to the signature. After computing `totalWidth`:

```go
if minWidth > 0 && totalWidth < minWidth {
    valueWidth += minWidth - totalWidth
    totalWidth = minWidth
}
```

Everything downstream (text centering, rect sizes) already uses `labelWidth`,
`valueWidth`, and `totalWidth`, so no further changes are needed.

## Frontend (`web/dash0/src/routes/orgs/$org/badges.tsx`)

### Search-param schema

Add `minWidth?: number` to `BadgeSearch`. In `validateSearch`, parse it as an integer
in `[1, 800]`; omit when 0 or absent. Strip from URL in `updateSearch` when `<= 0`.

### Configuration card

Add a labeled number input below the Style select:

```
Min width (px)   [     0 ]
```

- Type: `number`, `min={0}`, `max={800}`, `step={10}`.
- Value 0 means "auto" — no `minWidth` param in the URL.
- Hint text: "Minimum total badge width in pixels (0 = auto)."

### URL builder

Append `&minWidth=N` to the badge path when `minWidth > 0`.

### i18n (`locales/{en,fr,es,de}/badges.json`)

Add:
- `minWidth` — label ("Minimum width (px)")
- `minWidthDescription` — hint ("Pads the badge to at least this width. Use 0 for auto.")

## Acceptance criteria

- [ ] `?minWidth=200` on a narrow "up" badge produces an SVG with `width="200"` in the
      `<svg>` element.
- [ ] `?minWidth=50` on a badge that is already 120px wide has no effect (width stays 120).
- [ ] `?minWidth=0` (or absent) behaves identically to today.
- [ ] Values outside `[1, 800]` are clamped, not rejected.
- [ ] Unit tests in `service_test.go` cover min-width application and the no-op case.
- [ ] Frontend number input appears in the config card; changes update the preview.
- [ ] Preview badge grows visually when a min width larger than the natural size is set.
