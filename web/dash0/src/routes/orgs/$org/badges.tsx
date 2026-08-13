import { useState, useEffect, useRef, useCallback } from "react";
import { createFileRoute, useNavigate, Link } from "@tanstack/react-router";
import { useTranslation } from "react-i18next";
import {
  ArrowLeft,
  AlertCircle,
  BadgeCheck,
  Copy,
  Check as CheckIcon,
  FileDown,
  RefreshCw,
} from "lucide-react";
import { toast } from "sonner";
import { useCheck, type Check } from "@/api/hooks";
import { Button } from "@/components/ui/button";
import { PageHeader } from "@/components/shared/page-header";
import { CheckPicker } from "@/components/shared/check-picker";
import { Card, CardContent, CardHeader, CardTitle, CardDescription } from "@/components/ui/card";
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert";
import { Label } from "@/components/ui/label";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { Input } from "@/components/ui/input";
import { Skeleton } from "@/components/ui/skeleton";
import { Checkbox } from "@/components/ui/checkbox";

type BadgePeriod = "24h" | "7d" | "30d" | "90d";
type BadgeStyle = "flat" | "flat-square";

const componentOrder = [
  "status",
  "availability",
  "duration",
  "response-time",
  "uptime-bar",
  "response-time-graph",
] as const;
const validComponentTokens = new Set<string>(componentOrder);
const validPeriods: BadgePeriod[] = ["24h", "7d", "30d", "90d"];
const validStyles: BadgeStyle[] = ["flat", "flat-square"];

const DEFAULT_WIDTH = 300;

interface BadgeSearch {
  check?: string;
  components?: string;
  period?: BadgePeriod;
  style?: BadgeStyle;
  label?: string;
  minWidth?: number;
  width?: number;
}

function parseComponentsString(raw: string): string[] {
  return raw
    .split(",")
    .map((t) => t.trim())
    .filter((t) => validComponentTokens.has(t));
}

function hasRowToken(tokens: string[]): boolean {
  return tokens.includes("uptime-bar") || tokens.includes("response-time-graph");
}

export const Route = createFileRoute("/orgs/$org/badges")({
  validateSearch: (search: Record<string, unknown>): BadgeSearch => {
    let components: string | undefined;
    if (typeof search.components === "string" && search.components) {
      const tokens = parseComponentsString(search.components);
      if (tokens.length > 0 && tokens.join(",") !== "status") {
        components = tokens.join(",");
      }
    }
    const rawWidth = Number(search.width);
    const rawMinWidth = Number(search.minWidth);
    return {
      check: typeof search.check === "string" ? search.check : undefined,
      // Keep components undefined when it equals the default ("status") so
      // TanStack Router omits it from the URL instead of serializing it.
      components,
      // Keep period/style undefined when they equal their defaults ("30d" / "flat")
      // so TanStack Router omits them from the URL.
      period: validPeriods.includes(search.period as BadgePeriod) &&
        search.period !== "30d"
        ? (search.period as BadgePeriod)
        : undefined,
      style: validStyles.includes(search.style as BadgeStyle) &&
        search.style !== "flat"
        ? (search.style as BadgeStyle)
        : undefined,
      label: typeof search.label === "string" && search.label
        ? search.label
        : undefined,
      minWidth: !isNaN(rawMinWidth) && rawMinWidth >= 1 && rawMinWidth <= 800
        ? rawMinWidth
        : undefined,
      width: !isNaN(rawWidth) && rawWidth >= 60 && rawWidth <= 800
        ? rawWidth
        : undefined,
    };
  },
  component: BadgesPage,
});

const badgePeriods: { value: BadgePeriod; labelKey: string }[] = [
  { value: "24h", labelKey: "periods.24h" },
  { value: "7d", labelKey: "periods.7d" },
  { value: "30d", labelKey: "periods.30d" },
  { value: "90d", labelKey: "periods.90d" },
];

const badgeStyles: { value: BadgeStyle; labelKey: string }[] = [
  { value: "flat", labelKey: "styles.flat" },
  { value: "flat-square", labelKey: "styles.flatSquare" },
];

const componentDefs = [
  { token: "status", labelKey: "components.status", descKey: "components.statusDescription" },
  { token: "availability", labelKey: "components.availability", descKey: "components.availabilityDescription" },
  { token: "duration", labelKey: "components.duration", descKey: "components.durationDescription" },
  { token: "response-time", labelKey: "components.responseTime", descKey: "components.responseTimeDescription" },
  { token: "uptime-bar", labelKey: "components.uptimeBar", descKey: "components.uptimeBarDescription" },
  { token: "response-time-graph", labelKey: "components.responseTimeGraph", descKey: "components.responseTimeGraphDescription" },
] as const;

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
      {copied ? <CheckIcon className="mr-1.5 h-3.5 w-3.5" /> : <Copy className="mr-1.5 h-3.5 w-3.5" />}
      {copied ? t("copied") : label}
    </Button>
  );
}

function BadgePreview({
  org,
  check,
  components,
  period,
  style,
  customLabel,
  minWidth,
  width,
}: {
  org: string;
  check: Check;
  components: string;
  period: BadgePeriod;
  style: BadgeStyle;
  customLabel: string;
  minWidth: number;
  width: number;
}) {
  const { t } = useTranslation("badges");
  const imgRef = useRef<HTMLImageElement>(null);
  const [cacheBust, setCacheBust] = useState(() => Date.now());

  const tokens = parseComponentsString(components);
  const showRowControls = hasRowToken(tokens);

  const identifier = check.slug || check.uid;
  const params = new URLSearchParams();
  if (period !== "30d") params.set("period", period);
  if (style !== "flat") params.set("style", style);
  if (customLabel) params.set("label", customLabel);
  if (!showRowControls && minWidth > 0) params.set("minWidth", String(minWidth));
  if (showRowControls && width !== DEFAULT_WIDTH) params.set("width", String(width));
  const query = params.toString();

  const badgePath = `/api/v1/orgs/${org}/checks/${identifier}/badges/${components}${query ? `?${query}` : ""}`;
  const badgeUrl = `${window.location.origin}${badgePath}`;

  const markdownCode = `![${check.name || identifier} badge](${badgeUrl})`;
  const htmlCode = `<img src="${badgeUrl}" alt="${check.name || identifier} badge" />`;

  const downloadBadge = useCallback(
    async (downloadFormat: "svg" | "png") => {
      try {
        const response = await fetch(badgePath);
        const svgText = await response.text();

        if (downloadFormat === "svg") {
          const blob = new Blob([svgText], { type: "image/svg+xml" });
          const url = URL.createObjectURL(blob);
          const a = document.createElement("a");
          a.href = url;
          a.download = `${identifier}-${components.replace(/,/g, "-")}.svg`;
          a.click();
          URL.revokeObjectURL(url);
          return;
        }

        const img = new window.Image();
        const svgBlob = new Blob([svgText], { type: "image/svg+xml" });
        const svgUrl = URL.createObjectURL(svgBlob);

        img.onload = () => {
          const scale = 3;
          const canvas = document.createElement("canvas");
          canvas.width = img.naturalWidth * scale;
          canvas.height = img.naturalHeight * scale;
          const ctx = canvas.getContext("2d")!;
          ctx.scale(scale, scale);
          ctx.drawImage(img, 0, 0);
          URL.revokeObjectURL(svgUrl);

          canvas.toBlob(
            (blob) => {
              if (!blob) return;
              const url = URL.createObjectURL(blob);
              const a = document.createElement("a");
              a.href = url;
              a.download = `${identifier}-${components.replace(/,/g, "-")}.png`;
              a.click();
              URL.revokeObjectURL(url);
            },
            "image/png",
            0.95
          );
        };
        img.src = svgUrl;
      } catch {
        toast.error(t("downloadFailed"));
      }
    },
    [badgePath, identifier, components, t]
  );

  const previewUrl = `${badgePath}${query ? "&" : "?"}t=${cacheBust}`;

  return (
    <div className="space-y-6">
      <Card>
        <CardHeader className="flex flex-row items-center justify-between">
          <CardTitle className="text-base">{t("preview")}</CardTitle>
          <div className="flex items-center gap-2">
            <Button
              variant="outline"
              size="sm"
              onClick={() => downloadBadge("svg")}
              data-testid="badge-download-svg"
            >
              <FileDown className="mr-1.5 h-3.5 w-3.5" />
              {t("downloadSvg")}
            </Button>
            <Button
              variant="outline"
              size="sm"
              onClick={() => downloadBadge("png")}
              data-testid="badge-download-png"
            >
              <FileDown className="mr-1.5 h-3.5 w-3.5" />
              {t("downloadPng")}
            </Button>
            <Button
              variant="outline"
              size="sm"
              onClick={() => setCacheBust(Date.now())}
              data-testid="badge-refresh-preview"
            >
              <RefreshCw className="mr-1.5 h-3.5 w-3.5" />
              {t("refresh")}
            </Button>
          </div>
        </CardHeader>
        <CardContent>
          <div className="flex items-center justify-center rounded-lg border border-dashed bg-muted/30 p-3 sm:p-8">
            <img
              ref={imgRef}
              src={previewUrl}
              alt={`${check.name || identifier} badge`}
              data-testid="badge-preview-img"
            />
          </div>
        </CardContent>
      </Card>

      <Card>
        <CardHeader>
          <CardTitle className="text-base">{t("embedCode")}</CardTitle>
          <CardDescription>{t("embedCodeDescription")}</CardDescription>
        </CardHeader>
        <CardContent className="space-y-4">
          <div className="space-y-2">
            <div className="flex items-center justify-between">
              <Label className="text-xs text-muted-foreground">{t("url")}</Label>
              <CopyButton text={badgeUrl} label={t("url")} />
            </div>
            <code data-testid="badge-embed-url" className="block rounded-md border bg-muted/50 p-3 text-xs break-all font-mono">
              {badgeUrl}
            </code>
          </div>
          <div className="space-y-2">
            <div className="flex items-center justify-between">
              <Label className="text-xs text-muted-foreground">{t("markdown")}</Label>
              <CopyButton text={markdownCode} label={t("markdown")} />
            </div>
            <code data-testid="badge-embed-markdown" className="block rounded-md border bg-muted/50 p-3 text-xs break-all font-mono">
              {markdownCode}
            </code>
          </div>
          <div className="space-y-2">
            <div className="flex items-center justify-between">
              <Label className="text-xs text-muted-foreground">{t("html")}</Label>
              <CopyButton text={htmlCode} label={t("html")} />
            </div>
            <code data-testid="badge-embed-html" className="block rounded-md border bg-muted/50 p-3 text-xs break-all font-mono">
              {htmlCode}
            </code>
          </div>
        </CardContent>
      </Card>
    </div>
  );
}

function BadgesPage() {
  const { t } = useTranslation("badges");
  const { org } = Route.useParams();
  const search = Route.useSearch();
  const navigate = useNavigate({ from: Route.fullPath });
  // Resolve the selected check directly (by uid or slug) so a deep-link to a check
  // outside the picker's current matches still works. Auto-disabled when no check param.
  const { data: directCheck, isLoading: directLoading } = useCheck(
    org,
    search.check ?? ""
  );

  const components = search.components ?? "status";
  const period = search.period ?? "30d";
  const style = search.style ?? "flat";
  const customLabel = search.label ?? "";
  const minWidth = search.minWidth ?? 0;
  const width = search.width ?? DEFAULT_WIDTH;

  const [localWidth, setLocalWidth] = useState(String(width));
  const [localMinWidth, setLocalMinWidth] = useState(String(minWidth));

  useEffect(() => setLocalWidth(String(width)), [width]);
  useEffect(() => setLocalMinWidth(String(minWidth)), [minWidth]);

  const activeTokens = parseComponentsString(components);
  const showRowControls = hasRowToken(activeTokens);
  const showPeriod =
    activeTokens.includes("availability") ||
    activeTokens.includes("response-time") ||
    showRowControls;

  const selectedCheck = search.check ? directCheck : undefined;

  // A check param is set but nothing resolved and the direct fetch is no longer
  // in flight → stale/unknown check (e.g. deleted, or a broken bookmark).
  const checkNotFound = !!search.check && !selectedCheck && !directLoading;

  const updateSearch = (updates: Partial<BadgeSearch>) => {
    navigate({
      search: (prev: BadgeSearch) => {
        const next = { ...prev, ...updates };
        if (next.components === "status") delete next.components;
        if (next.period === "30d") delete next.period;
        if (next.style === "flat") delete next.style;
        if (!next.label) delete next.label;
        if (!next.minWidth || next.minWidth <= 0) delete next.minWidth;
        if (!next.width || next.width === DEFAULT_WIDTH) delete next.width;
        return next;
      },
      replace: true,
    });
  };

  // Write the check slug (fallback uid) to the ?check= search param; clearing
  // the picker removes the param entirely.
  const handleCheckChange = (uid: string | undefined, check?: Check) => {
    updateSearch({ check: uid ? check?.slug || uid : undefined });
  };

  const handleComponentToggle = (token: string, checked: boolean) => {
    let tokens = [...activeTokens];
    if (checked) {
      if (!tokens.includes(token)) {
        // Insert in canonical order.
        const newTokens: string[] = [];
        for (const t of componentOrder) {
          if (tokens.includes(t) || t === token) {
            newTokens.push(t);
          }
        }
        tokens = newTokens;
      }
    } else {
      tokens = tokens.filter((t) => t !== token);
    }

    // Keep the URL non-empty: fall back to "status" when everything is off.
    if (tokens.length === 0) {
      tokens = ["status"];
    }

    updateSearch({ components: tokens.join(",") });
  };

  return (
    <div className="space-y-6">
      <div className="space-y-2">
        {selectedCheck && (
          <Link
            to="/orgs/$org/checks/$checkUid"
            params={{ org, checkUid: selectedCheck.uid }}
            search={{ graphPeriod: undefined, graphFull: undefined, region: undefined }}
            className="text-sm text-primary hover:underline inline-flex items-center gap-1"
            data-testid="badge-back-to-check"
          >
            <ArrowLeft className="h-4 w-4" />
            {selectedCheck.name || selectedCheck.slug || selectedCheck.uid.slice(0, 8)}
          </Link>
        )}
        <PageHeader
          icon={BadgeCheck}
          title={t("title")}
          description={t("subtitle")}
          className="flex-wrap"
        />
      </div>

      <div className="grid gap-6 lg:grid-cols-[400px_1fr]">
        <Card>
          <CardHeader>
            <CardTitle className="text-base">{t("configuration")}</CardTitle>
          </CardHeader>
          <CardContent className="space-y-4">
            <div className="space-y-2">
              <Label>{t("check")}</Label>
              <CheckPicker
                org={org}
                value={selectedCheck?.uid}
                selectedLabel={
                  selectedCheck
                    ? selectedCheck.name ||
                      selectedCheck.slug ||
                      selectedCheck.uid.slice(0, 8)
                    : undefined
                }
                placeholder={t("selectCheck")}
                onChange={handleCheckChange}
                triggerTestId="badge-check-select"
              />
            </div>

            <div className="space-y-2">
              <Label>{t("components")}</Label>
              <div className="space-y-3">
                {componentDefs.map(({ token, labelKey, descKey }) => {
                  const checked = activeTokens.includes(token);
                  return (
                    <div key={token} className="flex items-start gap-3">
                      <Checkbox
                        id={`component-${token}`}
                        checked={checked}
                        onCheckedChange={(v) =>
                          handleComponentToggle(token, v === true)
                        }
                        data-testid={`badge-component-${token}`}
                      />
                      <div className="grid gap-0.5">
                        <label
                          htmlFor={`component-${token}`}
                          className="text-sm font-medium leading-none peer-disabled:cursor-not-allowed peer-disabled:opacity-70 cursor-pointer"
                        >
                          {t(labelKey)}
                        </label>
                        <p className="text-xs text-muted-foreground">{t(descKey)}</p>
                      </div>
                    </div>
                  );
                })}
              </div>
            </div>

            {showPeriod && (
              <div className="space-y-2">
                <Label>{t("period")}</Label>
                <Select
                  value={period}
                  onValueChange={(v) => updateSearch({ period: v as BadgePeriod })}
                >
                  <SelectTrigger data-testid="badge-period-select">
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    {badgePeriods.map((p) => (
                      <SelectItem key={p.value} value={p.value}>
                        {t(p.labelKey)}
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
              </div>
            )}

            <div className="space-y-2">
              <Label>{t("style")}</Label>
              <Select
                value={style}
                onValueChange={(v) => updateSearch({ style: v as BadgeStyle })}
              >
                <SelectTrigger data-testid="badge-style-select">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  {badgeStyles.map((s) => (
                    <SelectItem key={s.value} value={s.value}>
                      {t(s.labelKey)}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </div>

            {showRowControls ? (
              <div className="space-y-2">
                <Label>{t("width")}</Label>
                <Input
                  data-testid="badge-width"
                  type="number"
                  min={60}
                  max={800}
                  step={10}
                  value={localWidth}
                  onChange={(e) => setLocalWidth(e.target.value)}
                  onBlur={() => {
                    const n = Number(localWidth);
                    if (!isNaN(n) && n >= 60 && n <= 800) updateSearch({ width: n });
                    else setLocalWidth(String(width));
                  }}
                  onKeyDown={(e) => e.key === "Enter" && e.currentTarget.blur()}
                />
              </div>
            ) : (
              <div className="space-y-2">
                <Label>{t("minWidth")}</Label>
                <Input
                  data-testid="badge-min-width"
                  type="number"
                  min={0}
                  max={800}
                  step={10}
                  value={localMinWidth}
                  onChange={(e) => setLocalMinWidth(e.target.value)}
                  onBlur={() => {
                    const n = Number(localMinWidth);
                    if (!isNaN(n) && n >= 0 && n <= 800) updateSearch({ minWidth: n || undefined });
                    else setLocalMinWidth(String(minWidth));
                  }}
                  onKeyDown={(e) => e.key === "Enter" && e.currentTarget.blur()}
                />
                <p className="text-xs text-muted-foreground">{t("minWidthDescription")}</p>
              </div>
            )}

            <div className="space-y-2">
              <Label>{t("customLabel")}</Label>
              <Input
                data-testid="badge-custom-label"
                placeholder={t("customLabelPlaceholder")}
                value={customLabel}
                onChange={(e) => updateSearch({ label: e.target.value })}
              />
            </div>
          </CardContent>
        </Card>

        <div>
          {selectedCheck ? (
            <div className="space-y-6">
              <BadgePreview
                org={org}
                check={selectedCheck}
                components={components}
                period={period}
                style={style}
                customLabel={customLabel}
                minWidth={minWidth}
                width={width}
              />
            </div>
          ) : checkNotFound ? (
            <Alert variant="warning" data-testid="badge-check-not-found">
              <AlertCircle className="h-4 w-4" />
              <AlertTitle>{t("checkNotFoundTitle")}</AlertTitle>
              <AlertDescription>{t("checkNotFound")}</AlertDescription>
            </Alert>
          ) : search.check ? (
            // A check is named in the URL and the direct fetch is still resolving —
            // show a skeleton rather than flashing the "select a check" prompt.
            <Card>
              <CardContent className="py-8">
                <Skeleton className="h-48 w-full" data-testid="badge-preview-loading" />
              </CardContent>
            </Card>
          ) : (
            <Card>
              <CardContent className="flex items-center justify-center py-16">
                <p className="text-muted-foreground">{t("selectCheckPrompt")}</p>
              </CardContent>
            </Card>
          )}
        </div>
      </div>
    </div>
  );
}
