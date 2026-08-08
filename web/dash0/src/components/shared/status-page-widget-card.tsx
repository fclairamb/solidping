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
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";

type WidgetMode = "inline" | "floating";
type WidgetTheme = "auto" | "light" | "dark";
type WidgetPosition = "bottom-right" | "bottom-left";

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
 * Snippet generator for the embeddable live status widget (spec
 * 2026-08-08-08) — the scripted, always-current sibling of
 * StatusPageBadgeCard's static SVG badge, and its neighbour on the status page
 * appearance settings screen.
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

  const [mode, setMode] = useState<WidgetMode>("inline");
  const [theme, setTheme] = useState<WidgetTheme>("auto");
  const [position, setPosition] = useState<WidgetPosition>("bottom-right");

  const scriptUrl = `${window.location.origin}/embed/v1/widget.js`;

  const attributes = [`data-page="${org}/${pageSlug}"`];
  if (mode !== "inline") attributes.push(`data-mode="${mode}"`);
  if (mode === "floating") attributes.push(`data-position="${position}"`);
  if (theme !== "auto") attributes.push(`data-theme="${theme}"`);

  const snippet = `<script async src="${scriptUrl}" ${attributes.join(" ")}></script>`;

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
