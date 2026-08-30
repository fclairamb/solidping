import { useState } from "react";
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
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";

type BadgeStyle = "flat" | "flat-square";

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
 * Embed block for a status page's public SVG badge (spec 2026-08-08-07) — the
 * static, script-free sibling of the per-check badges page
 * (routes/orgs/$org/badges.tsx) and of the JS embed widget. Mirrors that
 * page's preview + copyable URL/Markdown/HTML pattern and testid conventions
 * (`badge-embed-url` / `-markdown` / `-html`) so the two badge surfaces look
 * and behave like siblings.
 */
export function StatusPageBadgeCard({
  org,
  pageSlug,
  pageName,
}: {
  org: string;
  pageSlug: string;
  pageName: string;
}) {
  const { t } = useTranslation("badges");
  const { t: tStatusPages } = useTranslation("statusPages");

  const [label, setLabel] = useState("");
  const [style, setStyle] = useState<BadgeStyle>("flat");

  const params = new URLSearchParams();
  if (label) params.set("label", label);
  if (style !== "flat") params.set("style", style);
  const query = params.toString();

  const badgePath = `/api/v1/status-pages/${org}/${pageSlug}/badge${query ? `?${query}` : ""}`;
  const badgeUrl = `${window.location.origin}${badgePath}`;

  const markdownCode = `![${pageName} status](${badgeUrl})`;
  const htmlCode = `<img src="${badgeUrl}" alt="${pageName} status" />`;

  return (
    <Card data-testid="status-page-badge-card">
      <CardHeader>
        <CardTitle className="text-base">
          {tStatusPages("badge.title")}
        </CardTitle>
        <CardDescription>{tStatusPages("badge.description")}</CardDescription>
      </CardHeader>
      <CardContent className="space-y-4">
        <div className="grid gap-4 sm:grid-cols-2">
          <div className="space-y-2">
            <Label htmlFor="status-page-badge-label">{t("customLabel")}</Label>
            <Input
              id="status-page-badge-label"
              data-testid="status-page-badge-label"
              value={label}
              onChange={(event) => setLabel(event.target.value)}
              placeholder={tStatusPages("badge.labelPlaceholder")}
            />
          </div>
          <div className="space-y-2">
            <Label htmlFor="status-page-badge-style">{t("style")}</Label>
            <Select
              value={style}
              onValueChange={(value) => setStyle(value as BadgeStyle)}
            >
              <SelectTrigger
                id="status-page-badge-style"
                data-testid="status-page-badge-style"
              >
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="flat">{t("styles.flat")}</SelectItem>
                <SelectItem value="flat-square">
                  {t("styles.flatSquare")}
                </SelectItem>
              </SelectContent>
            </Select>
          </div>
        </div>

        <div className="flex items-center justify-center rounded-lg border border-dashed bg-muted/30 p-3 sm:p-8">
          <a
            href={`/status0/${org}/${pageSlug}`}
            target="_blank"
            rel="noopener noreferrer"
            title={tStatusPages("viewPublic")}
            aria-label={tStatusPages("viewPublic")}
            data-testid="status-page-badge-preview-link"
          >
            <img
              src={badgePath}
              alt={`${pageName} status`}
              data-testid="status-page-badge-preview"
            />
          </a>
        </div>

        <div className="space-y-2">
          <div className="flex items-center justify-between">
            <Label className="text-xs text-muted-foreground">
              {t("url")}
            </Label>
            <CopyButton text={badgeUrl} label={t("url")} />
          </div>
          <code
            data-testid="badge-embed-url"
            className="block rounded-md border bg-muted/50 p-3 text-xs break-all font-mono"
          >
            {badgeUrl}
          </code>
        </div>
        <div className="space-y-2">
          <div className="flex items-center justify-between">
            <Label className="text-xs text-muted-foreground">
              {t("markdown")}
            </Label>
            <CopyButton text={markdownCode} label={t("markdown")} />
          </div>
          <code
            data-testid="badge-embed-markdown"
            className="block rounded-md border bg-muted/50 p-3 text-xs break-all font-mono"
          >
            {markdownCode}
          </code>
        </div>
        <div className="space-y-2">
          <div className="flex items-center justify-between">
            <Label className="text-xs text-muted-foreground">
              {t("html")}
            </Label>
            <CopyButton text={htmlCode} label={t("html")} />
          </div>
          <code
            data-testid="badge-embed-html"
            className="block rounded-md border bg-muted/50 p-3 text-xs break-all font-mono"
          >
            {htmlCode}
          </code>
        </div>
      </CardContent>
    </Card>
  );
}
