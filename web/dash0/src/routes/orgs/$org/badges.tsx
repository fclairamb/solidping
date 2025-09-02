import { useState, useRef, useCallback } from "react";
import { createFileRoute, useNavigate } from "@tanstack/react-router";
import {
  Copy,
  Check as CheckIcon,
  Download,
  Image,
  FileImage,
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

const badgeFormats = [
  { value: "status" as const, label: "Status", description: "Current up/down status" },
  { value: "availability" as const, label: "Availability", description: "Uptime percentage" },
  { value: "availability-duration" as const, label: "Availability + Duration", description: "Uptime % with duration" },
];

const badgePeriods = [
  { value: "1h" as const, label: "1 hour" },
  { value: "24h" as const, label: "24 hours" },
  { value: "7d" as const, label: "7 days" },
  { value: "30d" as const, label: "30 days" },
];

const badgeStyles = [
  { value: "flat" as const, label: "Flat" },
  { value: "flat-square" as const, label: "Flat Square" },
];

function CopyButton({ text, label }: { text: string; label: string }) {
  const [copied, setCopied] = useState(false);

  const handleCopy = async () => {
    await navigator.clipboard.writeText(text);
    setCopied(true);
    toast.success(`${label} copied to clipboard`);
    setTimeout(() => setCopied(false), 2000);
  };

  return (
    <Button variant="outline" size="sm" onClick={handleCopy}>
      {copied ? <CheckIcon className="mr-1.5 h-3.5 w-3.5" /> : <Copy className="mr-1.5 h-3.5 w-3.5" />}
      {copied ? "Copied" : label}
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
  const imgRef = useRef<HTMLImageElement>(null);

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

        // Convert SVG to PNG/JPG via canvas
        const img = new window.Image();
        const svgBlob = new Blob([svgText], { type: "image/svg+xml" });
        const svgUrl = URL.createObjectURL(svgBlob);

        img.onload = () => {
          const scale = 3; // High-res
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
        toast.error("Failed to download badge");
      }
    },
    [badgePath, identifier, format]
  );

  // Cache-bust preview with timestamp
  const previewUrl = `${badgePath}${query ? "&" : "?"}t=${Date.now()}`;

  return (
    <div className="space-y-6">
      {/* Live Preview */}
      <Card>
        <CardHeader>
          <CardTitle className="text-base">Preview</CardTitle>
        </CardHeader>
        <CardContent>
          <div className="flex items-center justify-center rounded-lg border border-dashed bg-muted/30 p-8">
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

      {/* Download */}
      <Card>
        <CardHeader>
          <CardTitle className="text-base">Download</CardTitle>
          <CardDescription>Download the badge in different formats</CardDescription>
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

      {/* Embed Codes */}
      <Card>
        <CardHeader>
          <CardTitle className="text-base">Embed Code</CardTitle>
          <CardDescription>Copy the badge URL or embed code for your README or website</CardDescription>
        </CardHeader>
        <CardContent className="space-y-4">
          <div className="space-y-2">
            <div className="flex items-center justify-between">
              <Label className="text-xs text-muted-foreground">URL</Label>
              <CopyButton text={badgeUrl} label="URL" />
            </div>
            <code data-testid="badge-embed-url" className="block rounded-md border bg-muted/50 p-3 text-xs break-all font-mono">
              {badgeUrl}
            </code>
          </div>
          <div className="space-y-2">
            <div className="flex items-center justify-between">
              <Label className="text-xs text-muted-foreground">Markdown</Label>
              <CopyButton text={markdownCode} label="Markdown" />
            </div>
            <code data-testid="badge-embed-markdown" className="block rounded-md border bg-muted/50 p-3 text-xs break-all font-mono">
              {markdownCode}
            </code>
          </div>
          <div className="space-y-2">
            <div className="flex items-center justify-between">
              <Label className="text-xs text-muted-foreground">HTML</Label>
              <CopyButton text={htmlCode} label="HTML" />
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
  const { org } = Route.useParams();
  const search = Route.useSearch();
  const navigate = useNavigate({ from: Route.fullPath });
  const { data: checks = [], isLoading, error } = useChecks(org);

  const format = search.format ?? "status";
  const period = search.period ?? "24h";
  const style = search.style ?? "flat";
  const customLabel = search.label ?? "";

  // Resolve check: match by UID first, then by slug
  const selectedCheck = search.check
    ? checks.find((c) => c.uid === search.check) ??
      checks.find((c) => c.slug === search.check)
    : undefined;

  const updateSearch = (updates: Partial<BadgeSearch>) => {
    navigate({
      search: (prev: BadgeSearch) => {
        const next = { ...prev, ...updates };
        // Strip defaults to keep URL clean
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
    // Prefer slug for cleaner URL, fall back to UID
    updateSearch({ check: check?.slug || uid });
  };

  return (
    <div className="space-y-6">
      <div>
        <h1 className="text-3xl font-bold tracking-tight">Badges</h1>
        <p className="text-muted-foreground">
          Generate embeddable status badges for your checks
        </p>
      </div>

      <div className="grid gap-6 lg:grid-cols-[400px_1fr]">
        {/* Configuration Panel */}
        <Card>
          <CardHeader>
            <CardTitle className="text-base">Configuration</CardTitle>
          </CardHeader>
          <CardContent className="space-y-4">
            {/* Check selector */}
            <div className="space-y-2">
              <Label>Check</Label>
              {isLoading ? (
                <Skeleton className="h-10 w-full" />
              ) : error ? (
                <p className="text-sm text-destructive">Failed to load checks</p>
              ) : (
                <Select
                  value={selectedCheck?.uid ?? ""}
                  onValueChange={handleCheckChange}
                >
                  <SelectTrigger data-testid="badge-check-select">
                    <SelectValue placeholder="Select a check" />
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

            {/* Badge format */}
            <div className="space-y-2">
              <Label>Format</Label>
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
                        <span>{f.label}</span>
                        <span className="ml-2 text-xs text-muted-foreground">{f.description}</span>
                      </div>
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </div>

            {/* Period (only for availability formats) */}
            {format !== "status" && (
              <div className="space-y-2">
                <Label>Period</Label>
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
                        {p.label}
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
              </div>
            )}

            {/* Style */}
            <div className="space-y-2">
              <Label>Style</Label>
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
                      {s.label}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </div>

            {/* Custom label */}
            <div className="space-y-2">
              <Label>Custom Label</Label>
              <Input
                data-testid="badge-custom-label"
                placeholder="Leave empty for check name"
                value={customLabel}
                onChange={(e) => updateSearch({ label: e.target.value })}
              />
            </div>
          </CardContent>
        </Card>

        {/* Preview & Embed Panel */}
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
                <p className="text-muted-foreground">
                  Select a check to preview and generate badges
                </p>
              </CardContent>
            </Card>
          )}
        </div>
      </div>
    </div>
  );
}
