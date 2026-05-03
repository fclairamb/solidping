import { useState, useRef, useCallback } from "react";
import { createFileRoute, useNavigate } from "@tanstack/react-router";
import { useTranslation } from "react-i18next";
import {
  Copy,
  Check as CheckIcon,
  Download,
  Image,
  FileImage,
  RefreshCw,
} from "lucide-react";
import { toast } from "sonner";
import { useChecks, type Check } from "@/api/hooks";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle, CardDescription } from "@/components/ui/card";
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

type BadgeFormat = "status" | "availability" | "availability-duration";
type BadgePeriod = "1h" | "24h" | "7d" | "30d";
type BadgeStyle = "flat" | "flat-square";

const validFormats: BadgeFormat[] = ["status", "availability", "availability-duration"];
const validPeriods: BadgePeriod[] = ["1h", "24h", "7d", "30d"];
const validStyles: BadgeStyle[] = ["flat", "flat-square"];

interface BadgeSearch {
  check?: string;
  format?: BadgeFormat;
  period?: BadgePeriod;
  style?: BadgeStyle;
  label?: string;
}

export const Route = createFileRoute("/orgs/$org/badges")({
  validateSearch: (search: Record<string, unknown>): BadgeSearch => ({
    check: typeof search.check === "string" ? search.check : undefined,
    format: validFormats.includes(search.format as BadgeFormat)
      ? (search.format as BadgeFormat)
      : undefined,
    period: validPeriods.includes(search.period as BadgePeriod)
      ? (search.period as BadgePeriod)
      : undefined,
    style: validStyles.includes(search.style as BadgeStyle)
      ? (search.style as BadgeStyle)
      : undefined,
    label: typeof search.label === "string" && search.label
      ? search.label
      : undefined,
  }),
  component: BadgesPage,
});

const badgeFormats: { value: BadgeFormat; labelKey: string; descriptionKey: string }[] = [
  { value: "status", labelKey: "formats.status", descriptionKey: "formats.statusDescription" },
  { value: "availability", labelKey: "formats.availability", descriptionKey: "formats.availabilityDescription" },
  { value: "availability-duration", labelKey: "formats.availabilityDuration", descriptionKey: "formats.availabilityDurationDescription" },
];

const badgePeriods: { value: BadgePeriod; labelKey: string }[] = [
  { value: "1h", labelKey: "periods.1h" },
  { value: "24h", labelKey: "periods.24h" },
  { value: "7d", labelKey: "periods.7d" },
  { value: "30d", labelKey: "periods.30d" },
];

const badgeStyles: { value: BadgeStyle; labelKey: string }[] = [
  { value: "flat", labelKey: "styles.flat" },
  { value: "flat-square", labelKey: "styles.flatSquare" },
];

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
  format,
  period,
  style,
  customLabel,
}: {
  org: string;
  check: Check;
  format: BadgeFormat;
  period: BadgePeriod;
  style: BadgeStyle;
  customLabel: string;
}) {
  const { t } = useTranslation("badges");
  const imgRef = useRef<HTMLImageElement>(null);
  const [cacheBust, setCacheBust] = useState(() => Date.now());

  const identifier = check.slug || check.uid;
  const params = new URLSearchParams();
  if (period !== "24h") params.set("period", period);
  if (style !== "flat") params.set("style", style);
  if (customLabel) params.set("label", customLabel);
  const query = params.toString();

  const badgePath = `/api/v1/orgs/${org}/checks/${identifier}/badges/${format}${query ? `?${query}` : ""}`;
  const badgeUrl = `${window.location.origin}${badgePath}`;

  const markdownCode = `![${check.name || identifier} ${format}](${badgeUrl})`;
  const htmlCode = `<img src="${badgeUrl}" alt="${check.name || identifier} ${format}" />`;

  const downloadBadge = useCallback(
    async (downloadFormat: "svg" | "png" | "jpg") => {
      try {
        const response = await fetch(badgePath);
        const svgText = await response.text();

        if (downloadFormat === "svg") {
          const blob = new Blob([svgText], { type: "image/svg+xml" });
          const url = URL.createObjectURL(blob);
          const a = document.createElement("a");
          a.href = url;
          a.download = `${identifier}-${format}.svg`;
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
          if (downloadFormat === "jpg") {
            ctx.fillStyle = "#ffffff";
            ctx.fillRect(0, 0, canvas.width, canvas.height);
          }
          ctx.drawImage(img, 0, 0);
          URL.revokeObjectURL(svgUrl);

          const mimeType = downloadFormat === "png" ? "image/png" : "image/jpeg";
          canvas.toBlob(
            (blob) => {
              if (!blob) return;
              const url = URL.createObjectURL(blob);
              const a = document.createElement("a");
              a.href = url;
              a.download = `${identifier}-${format}.${downloadFormat}`;
              a.click();
              URL.revokeObjectURL(url);
            },
            mimeType,
            0.95
          );
        };
        img.src = svgUrl;
      } catch {
        toast.error(t("downloadFailed"));
      }
    },
    [badgePath, identifier, format, t]
  );

  const previewUrl = `${badgePath}${query ? "&" : "?"}t=${cacheBust}`;

  return (
    <div className="space-y-6">
      <Card>
        <CardHeader className="flex flex-row items-center justify-between">
          <CardTitle className="text-base">{t("preview")}</CardTitle>
          <Button
            variant="outline"
            size="sm"
            onClick={() => setCacheBust(Date.now())}
            data-testid="badge-refresh-preview"
          >
            <RefreshCw className="mr-1.5 h-3.5 w-3.5" />
            {t("refresh")}
          </Button>
        </CardHeader>
        <CardContent>
          <div className="flex items-center justify-center rounded-lg border border-dashed bg-muted/30 p-3 sm:p-8">
            <img
              ref={imgRef}
              src={previewUrl}
              alt={`${check.name || identifier} badge`}
              className="h-5"
              data-testid="badge-preview-img"
            />
          </div>
        </CardContent>
      </Card>

      <Card>
        <CardHeader>
          <CardTitle className="text-base">{t("download")}</CardTitle>
          <CardDescription>{t("downloadDescription")}</CardDescription>
        </CardHeader>
        <CardContent>
          <div className="flex flex-wrap gap-2">
            <Button variant="outline" size="sm" onClick={() => downloadBadge("svg")} data-testid="badge-download-svg">
              <Image className="mr-1.5 h-3.5 w-3.5" />
              SVG
            </Button>
            <Button variant="outline" size="sm" onClick={() => downloadBadge("png")} data-testid="badge-download-png">
              <FileImage className="mr-1.5 h-3.5 w-3.5" />
              PNG
            </Button>
            <Button variant="outline" size="sm" onClick={() => downloadBadge("jpg")} data-testid="badge-download-jpg">
              <Download className="mr-1.5 h-3.5 w-3.5" />
              JPG
            </Button>
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
  const { data: checks = [], isLoading, error } = useChecks(org);

  const format = search.format ?? "status";
  const period = search.period ?? "24h";
  const style = search.style ?? "flat";
  const customLabel = search.label ?? "";

  const selectedCheck = search.check
    ? checks.find((c) => c.uid === search.check) ??
      checks.find((c) => c.slug === search.check)
    : undefined;

  const updateSearch = (updates: Partial<BadgeSearch>) => {
    navigate({
      search: (prev: BadgeSearch) => {
        const next = { ...prev, ...updates };
        if (next.format === "status") delete next.format;
        if (next.period === "24h") delete next.period;
        if (next.style === "flat") delete next.style;
        if (!next.label) delete next.label;
        return next;
      },
      replace: true,
    });
  };

  const handleCheckChange = (uid: string) => {
    const check = checks.find((c) => c.uid === uid);
    updateSearch({ check: check?.slug || uid });
  };

  return (
    <div className="space-y-6">
      <div>
        <h1 className="text-3xl font-bold tracking-tight">{t("title")}</h1>
        <p className="text-muted-foreground">{t("subtitle")}</p>
      </div>

      <div className="grid gap-6 lg:grid-cols-[400px_1fr]">
        <Card>
          <CardHeader>
            <CardTitle className="text-base">{t("configuration")}</CardTitle>
          </CardHeader>
          <CardContent className="space-y-4">
            <div className="space-y-2">
              <Label>{t("check")}</Label>
              {isLoading ? (
                <Skeleton className="h-10 w-full" />
              ) : error ? (
                <p className="text-sm text-destructive">{t("loadFailed")}</p>
              ) : (
                <Select
                  value={selectedCheck?.uid ?? ""}
                  onValueChange={handleCheckChange}
                >
                  <SelectTrigger data-testid="badge-check-select">
                    <SelectValue placeholder={t("selectCheck")} />
                  </SelectTrigger>
                  <SelectContent>
                    {checks.map((check) => (
                      <SelectItem key={check.uid} value={check.uid}>
                        {check.name || check.slug || check.uid.slice(0, 8)}
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
              )}
            </div>

            <div className="space-y-2">
              <Label>{t("format")}</Label>
              <Select
                value={format}
                onValueChange={(v) => updateSearch({ format: v as BadgeFormat })}
              >
                <SelectTrigger data-testid="badge-format-select">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  {badgeFormats.map((f) => (
                    <SelectItem key={f.value} value={f.value}>
                      <div>
                        <span>{t(f.labelKey)}</span>
                        <span className="ml-2 text-xs text-muted-foreground">{t(f.descriptionKey)}</span>
                      </div>
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </div>

            {format !== "status" && (
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
            <BadgePreview
              org={org}
              check={selectedCheck}
              format={format}
              period={period}
              style={style}
              customLabel={customLabel}
            />
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
