import { useEffect, useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import { Copy, Check as CheckIcon } from "lucide-react";
import { toast } from "sonner";
import { Button } from "@/components/ui/button";
import {
  Card,
  CardContent,
  CardHeader,
  CardTitle,
  CardDescription,
} from "@/components/ui/card";
import { Label } from "@/components/ui/label";
import { Input } from "@/components/ui/input";
import { Checkbox } from "@/components/ui/checkbox";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { CollapsibleSection } from "@/components/ui/collapsible-section";

type WidgetMode = "inline" | "floating";
type WidgetTheme = "auto" | "light" | "dark";
type WidgetPosition = "bottom-right" | "bottom-left";
type WidgetSize = "sm" | "md" | "lg";
type PageStatus = "operational" | "degraded" | "down" | "maintenance" | "unknown";

/** Mirrors STATUSES in web/status0/src/embed/widget.ts — keep in sync. */
const LABEL_STATUSES: PageStatus[] = [
  "operational",
  "degraded",
  "down",
  "maintenance",
  "unknown",
];

/**
 * Escapes a value for safe interpolation into a double-quoted HTML attribute.
 * Applies to every `data-label-*` value we emit — both into the copyable
 * snippet (a customer could otherwise paste a broken tag, or one carrying an
 * attribute they never typed, onto their own site) and into the preview
 * iframe's `srcDoc` (same HTML-injection risk, just scoped to this card).
 */
function escapeHtmlAttr(value: string): string {
  return value
    .replace(/&/g, "&amp;")
    .replace(/"/g, "&quot;")
    .replace(/</g, "&lt;")
    .replace(/>/g, "&gt;");
}

function CopyButton({ text, label }: { text: string; label: string }) {
  const { t } = useTranslation("badges");
  const [copied, setCopied] = useState(false);

  const handleCopy = async () => {
    await navigator.clipboard.writeText(text);
    setCopied(true);
    toast.success(t("copiedToClipboard", { label }));
    setTimeout(() => setCopied(false), 2000);
  };

  return (
    <Button variant="outline" size="sm" onClick={handleCopy}>
      {copied ? (
        <CheckIcon className="mr-1.5 h-3.5 w-3.5" />
      ) : (
        <Copy className="mr-1.5 h-3.5 w-3.5" />
      )}
      {copied ? t("copied") : label}
    </Button>
  );
}

/**
 * Tracks dash0's own light/dark theme (the `html.dark` class toggled by
 * `ThemeToggle`) so the preview iframe's "auto" background can mirror it
 * reactively, without a shared theme context to plug into.
 */
function useIsDashDark(): boolean {
  const [dark, setDark] = useState(
    () => document.documentElement.classList.contains("dark"),
  );

  useEffect(() => {
    const target = document.documentElement;
    const observer = new MutationObserver(() => {
      setDark(target.classList.contains("dark"));
    });
    observer.observe(target, { attributes: true, attributeFilter: ["class"] });
    return () => observer.disconnect();
  }, []);

  return dark;
}

function buildAttributes({
  org,
  pageSlug,
  mode,
  theme,
  position,
  size,
  labels,
  linkEnabled,
  forceStatus,
}: {
  org: string;
  pageSlug: string;
  mode: WidgetMode;
  theme: WidgetTheme;
  position: WidgetPosition;
  size: WidgetSize;
  labels: Record<PageStatus, string>;
  linkEnabled: boolean;
  forceStatus?: PageStatus;
}): string[] {
  const attributes = [`data-page="${org}/${pageSlug}"`];
  if (mode !== "inline") attributes.push(`data-mode="${mode}"`);
  if (mode === "floating") attributes.push(`data-position="${position}"`);
  if (theme !== "auto") attributes.push(`data-theme="${theme}"`);
  if (size !== "md") attributes.push(`data-size="${size}"`);
  for (const status of LABEL_STATUSES) {
    const value = labels[status].trim();
    if (value) {
      attributes.push(`data-label-${status}="${escapeHtmlAttr(value)}"`);
    }
  }
  // Linking is the default (matches the widget's own default when the
  // attribute is absent) — only emit the attribute for the opt-out, so the
  // common case stays a minimal, unchanged snippet.
  if (!linkEnabled) {
    attributes.push(`data-link="false"`);
  }
  if (forceStatus) {
    attributes.push(`data-force-status="${forceStatus}"`);
  }

  return attributes;
}

function previewBackground(theme: WidgetTheme, dashDark: boolean): {
  background: string;
  color: string;
} {
  const dark = theme === "dark" || (theme === "auto" && dashDark);
  return dark
    ? { background: "#0b1220", color: "#e5e7eb" }
    : { background: "#f8fafc", color: "#1f2937" };
}

/**
 * Snippet generator for the embeddable live status widget (spec
 * 2026-08-08-08, extended by 2026-08-08-09) — the scripted, always-current
 * sibling of StatusPageBadgeCard's static SVG badge, and its neighbour on the
 * status page appearance settings screen.
 *
 * The generated tag targets `/embed/v1/widget.js`, a FROZEN public contract:
 * only attributes v1 actually understands may be emitted here, otherwise we'd
 * hand customers a snippet that silently does nothing.
 */
export function StatusPageWidgetCard({
  org,
  pageSlug,
}: {
  org: string;
  pageSlug: string;
}) {
  const { t } = useTranslation("badges");
  const { t: tStatusPages } = useTranslation("statusPages");
  const dashDark = useIsDashDark();

  const [mode, setMode] = useState<WidgetMode>("inline");
  const [theme, setTheme] = useState<WidgetTheme>("auto");
  const [position, setPosition] = useState<WidgetPosition>("bottom-right");
  const [size, setSize] = useState<WidgetSize>("md");
  const [previewStatus, setPreviewStatus] = useState<PageStatus>("operational");
  const [linkEnabled, setLinkEnabled] = useState(true);
  const [labels, setLabels] = useState<Record<PageStatus, string>>({
    operational: "",
    degraded: "",
    down: "",
    maintenance: "",
    unknown: "",
  });

  const scriptUrl = `${window.location.origin}/embed/v1/widget.js`;

  const attributes = buildAttributes({
    org,
    pageSlug,
    mode,
    theme,
    position,
    size,
    labels,
    linkEnabled,
  });
  const snippet = `<script async src="${scriptUrl}" ${attributes.join(" ")}></script>`;

  const previewAttributes = buildAttributes({
    org,
    pageSlug,
    mode,
    theme,
    position,
    size,
    labels,
    linkEnabled,
    forceStatus: previewStatus,
  });
  const previewScriptTag = `<script async src="${scriptUrl}" ${previewAttributes.join(" ")}></script>`;
  const { background, color } = previewBackground(theme, dashDark);
  const previewSrcDoc = useMemo(() => {
    return `<!doctype html>
<html>
<head>
<meta charset="utf-8" />
<style>
  html, body { margin: 0; height: 100%; }
  body {
    box-sizing: border-box;
    min-height: 100%;
    padding: 24px;
    display: flex;
    align-items: ${mode === "floating" ? "flex-start" : "center"};
    justify-content: ${mode === "floating" ? "flex-start" : "center"};
    background: ${background};
    color: ${color};
    font-family: ui-sans-serif, system-ui, -apple-system, "Segoe UI", Roboto, Helvetica, Arial, sans-serif;
    font-size: 14px;
  }
</style>
</head>
<body>
  <p style="margin:0;">Status: ${previewScriptTag}</p>
</body>
</html>`;
  }, [previewScriptTag, mode, background, color]);

  const hasCustomLabels = LABEL_STATUSES.some((status) => labels[status].trim());

  return (
    <Card data-testid="status-page-widget-card">
      <CardHeader>
        <CardTitle className="text-base">
          {tStatusPages("widget.title")}
        </CardTitle>
        <CardDescription>
          {tStatusPages("widget.description")}
        </CardDescription>
      </CardHeader>
      <CardContent className="space-y-4">
        <div className="grid gap-4 sm:grid-cols-2">
          <div className="space-y-2">
            <Label htmlFor="status-page-widget-mode">
              {tStatusPages("widget.mode")}
            </Label>
            <Select
              value={mode}
              onValueChange={(value) => setMode(value as WidgetMode)}
            >
              <SelectTrigger
                id="status-page-widget-mode"
                data-testid="status-page-widget-mode"
              >
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="inline">
                  {tStatusPages("widget.modes.inline")}
                </SelectItem>
                <SelectItem value="floating">
                  {tStatusPages("widget.modes.floating")}
                </SelectItem>
              </SelectContent>
            </Select>
          </div>
          <div className="space-y-2">
            <Label htmlFor="status-page-widget-theme">
              {tStatusPages("widget.theme")}
            </Label>
            <Select
              value={theme}
              onValueChange={(value) => setTheme(value as WidgetTheme)}
            >
              <SelectTrigger
                id="status-page-widget-theme"
                data-testid="status-page-widget-theme"
              >
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="auto">
                  {tStatusPages("widget.themes.auto")}
                </SelectItem>
                <SelectItem value="light">
                  {tStatusPages("widget.themes.light")}
                </SelectItem>
                <SelectItem value="dark">
                  {tStatusPages("widget.themes.dark")}
                </SelectItem>
              </SelectContent>
            </Select>
          </div>
          {mode === "floating" && (
            <div className="space-y-2">
              <Label htmlFor="status-page-widget-position">
                {tStatusPages("widget.position")}
              </Label>
              <Select
                value={position}
                onValueChange={(value) => setPosition(value as WidgetPosition)}
              >
                <SelectTrigger
                  id="status-page-widget-position"
                  data-testid="status-page-widget-position"
                >
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="bottom-right">
                    {tStatusPages("widget.positions.bottomRight")}
                  </SelectItem>
                  <SelectItem value="bottom-left">
                    {tStatusPages("widget.positions.bottomLeft")}
                  </SelectItem>
                </SelectContent>
              </Select>
            </div>
          )}
          <div className="space-y-2">
            <Label htmlFor="status-page-widget-size">
              {tStatusPages("widget.size")}
            </Label>
            <Select
              value={size}
              onValueChange={(value) => setSize(value as WidgetSize)}
            >
              <SelectTrigger
                id="status-page-widget-size"
                data-testid="status-page-widget-size"
              >
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="sm">{tStatusPages("widget.sizes.sm")}</SelectItem>
                <SelectItem value="md">{tStatusPages("widget.sizes.md")}</SelectItem>
                <SelectItem value="lg">{tStatusPages("widget.sizes.lg")}</SelectItem>
              </SelectContent>
            </Select>
          </div>
        </div>

        <CollapsibleSection
          id="status-page-widget-labels"
          title={tStatusPages("widget.customizeLabels")}
          customized={hasCustomLabels}
          data-testid="status-page-widget-customize-labels"
        >
          <div className="grid gap-3 sm:grid-cols-2">
            {LABEL_STATUSES.map((status) => (
              <div key={status} className="space-y-2">
                <Label htmlFor={`status-page-widget-label-${status}`}>
                  {tStatusPages(`widget.labelOverrides.${status}`)}
                </Label>
                <Input
                  id={`status-page-widget-label-${status}`}
                  data-testid={`status-page-widget-label-${status}`}
                  value={labels[status]}
                  onChange={(event) =>
                    setLabels((prev) => ({ ...prev, [status]: event.target.value }))
                  }
                  placeholder={tStatusPages(`widget.labelPlaceholders.${status}`)}
                />
              </div>
            ))}
          </div>
        </CollapsibleSection>

        <label className="flex items-center gap-2">
          <Checkbox
            data-testid="status-page-widget-link-toggle"
            checked={linkEnabled}
            onCheckedChange={(value) => setLinkEnabled(value === true)}
          />
          <span className="text-sm">{tStatusPages("widget.link")}</span>
        </label>

        <div className="space-y-2">
          <div className="flex items-center justify-between gap-3">
            <Label className="text-xs text-muted-foreground">
              {tStatusPages("widget.previewTitle")}
            </Label>
            <div className="w-40">
              <Select
                value={previewStatus}
                onValueChange={(value) => setPreviewStatus(value as PageStatus)}
              >
                <SelectTrigger
                  data-testid="status-page-widget-preview-status"
                  aria-label={tStatusPages("widget.previewState")}
                >
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  {LABEL_STATUSES.map((status) => (
                    <SelectItem key={status} value={status}>
                      {tStatusPages(`widget.previewStates.${status}`)}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </div>
          </div>
          <div className="overflow-hidden rounded-lg border border-dashed">
            <iframe
              data-testid="status-page-widget-preview"
              title={tStatusPages("widget.previewTitle")}
              srcDoc={previewSrcDoc}
              sandbox="allow-scripts"
              className="h-40 w-full sm:h-48"
            />
          </div>
        </div>

        <div className="space-y-2">
          <div className="flex items-center justify-between">
            <Label className="text-xs text-muted-foreground">{t("html")}</Label>
            <CopyButton text={snippet} label={t("html")} />
          </div>
          <code
            data-testid="widget-embed-snippet"
            className="block rounded-md border bg-muted/50 p-3 text-xs break-all font-mono"
          >
            {snippet}
          </code>
          <p className="text-xs text-muted-foreground">
            {tStatusPages("widget.hint")}
          </p>
        </div>
      </CardContent>
    </Card>
  );
}
