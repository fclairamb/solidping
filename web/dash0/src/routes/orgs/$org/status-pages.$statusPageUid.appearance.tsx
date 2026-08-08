import { useEffect, useMemo, useRef, useState } from "react";
import { createFileRoute, Link, useNavigate } from "@tanstack/react-router";
import { useTranslation } from "react-i18next";
import { ArrowLeft, ExternalLink, Loader2, Palette } from "lucide-react";
import { toast } from "sonner";
import { useStatusPage, useUpdateStatusPage } from "@/api/hooks";
import { ApiError } from "@/api/client";
import { Button } from "@/components/ui/button";
import { Label } from "@/components/ui/label";
import { Skeleton } from "@/components/ui/skeleton";
import { CodeTextarea } from "@/components/ui/code-textarea";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import { QueryErrorView } from "@/components/shared/error-views";
import { StatusPageBadgeCard } from "@/components/shared/status-page-badge-card";
import { StatusPageWidgetCard } from "@/components/shared/status-page-widget-card";
import { useDebounce } from "@/lib/use-debounce";

export const Route = createFileRoute(
  "/orgs/$org/status-pages/$statusPageUid/appearance",
)({
  component: StatusPageAppearancePage,
});

/**
 * Starter template offered when the page has no stylesheet yet. It is a
 * commented listing of the theming API — every custom property status0 reads,
 * plus the stable `sp-*` element hooks — so the editor itself is the
 * documentation. Keep it in sync with
 * web/docs/docs/features/status-pages.md ("Custom CSS").
 */
const STARTER_TEMPLATE = `/* Status page theme — every rule below is optional.
   These are the CSS custom properties the public page reads.
   Colors accept any CSS color (hex, rgb(), oklch(), …). */

:root {
  /* Brand — logo and outbound links */
  --brand: #e11d63;
  --brand-foreground: #ffffff;

  /* Page surface */
  --background: #f8fafc;
  --foreground: #0f172a;

  /* Cards (each section is a card) */
  --card: #ffffff;
  --card-foreground: #0f172a;

  /* Hairlines and separators */
  --border: #e2e8f0;

  /* Status colors — the dots, badges and uptime bars */
  --status-ok: #16a34a;
  --status-warning: #eab308;
  --status-error: #dc2626;

  /* Corner radius used across the page */
  --radius: 0.625rem;
}

/* Visitors whose browser/OS asks for dark mode get these instead. */
.dark {
  --background: #0b1220;
  --foreground: #e2e8f0;
  --card: #131c2e;
  --card-foreground: #e2e8f0;
  --border: #24314a;
}

/* ---------------------------------------------------------------
   Element hooks — stable class names on the public page:

     .sp-logo        header logo (the <img> lives inside it)
     .sp-page-name   status page name next to the logo
     .sp-footer      footer container
     .sp-powered-by  "Powered by SolidPing" link
     .sp-version     version line
   --------------------------------------------------------------- */

/* Replace the logo with your own image (Chromium / Safari): */
/*
.sp-logo img {
  content: url("https://cdn.example.com/logo.svg");
}
*/

/* Same, works everywhere: hide the <img> and paint the wrapper. */
/*
.sp-logo img { display: none; }
.sp-logo {
  background: url("https://cdn.example.com/logo.svg") center / contain no-repeat;
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
   @import is not allowed; external url() (fonts, images) is. */
`;

/**
 * Message contract shared with status0's preview listener
 * (web/status0/src/lib/preview-css.ts). Both sides check the literal type and
 * that css is a string; status0 additionally requires a same-origin sender.
 */
const PREVIEW_CSS_MESSAGE_TYPE = "sp:preview-css";

const MAX_CUSTOM_CSS_BYTES = 64 * 1024;

function StatusPageAppearancePage() {
  const { t } = useTranslation("statusPages");
  const navigate = useNavigate();
  const { org, statusPageUid } = Route.useParams();
  const {
    data: page,
    isLoading,
    error,
    refetch,
  } = useStatusPage(org, statusPageUid);
  const update = useUpdateStatusPage(org, statusPageUid);

  // `null` means "untouched": the editor then mirrors whatever the server
  // last returned. The first keystroke pins a local draft that no refetch may
  // overwrite — seeding via an effect would fight the typist (and cascade
  // renders).
  const [css, setCss] = useState<string | null>(null);
  const iframeRef = useRef<HTMLIFrameElement>(null);
  const [iframeReady, setIframeReady] = useState(false);

  const value = css ?? page?.customCss ?? "";
  const debounced = useDebounce(value, 300);
  const previewUrl = useMemo(
    () => (page ? `/status0/${org}/${page.slug}?preview=1` : ""),
    [org, page],
  );

  // Push the debounced draft into the preview. No server round-trip: the iframe
  // runs the real status0 renderer, so what you see is what visitors get.
  useEffect(() => {
    if (!iframeReady) return;
    iframeRef.current?.contentWindow?.postMessage(
      { type: PREVIEW_CSS_MESSAGE_TYPE, css: debounced },
      window.location.origin,
    );
  }, [debounced, iframeReady]);

  const tooLarge = new Blob([value]).size > MAX_CUSTOM_CSS_BYTES;
  const hasImport = /@import/i.test(value);
  const invalid = tooLarge || hasImport;
  const dirty = page ? value !== (page.customCss ?? "") : false;

  const handleSave = async () => {
    try {
      await update.mutateAsync({ customCss: value });
      // Drop the local draft so the editor tracks the server value again.
      setCss(null);
      toast.success(t("appearance.savedToast"));
    } catch (err) {
      if (err instanceof ApiError && err.status === 422) {
        toast.error(t("appearance.rejected"));
        return;
      }
      toast.error(t("appearance.saveFailed"));
    }
  };

  if (isLoading) {
    return (
      <div className="space-y-6">
        <Skeleton className="h-8 w-64" />
        <Skeleton className="h-[32rem] rounded-lg" />
      </div>
    );
  }

  if (error) {
    return (
      <QueryErrorView
        error={error}
        org={org}
        resource="Status Page"
        backTo="/orgs/$org/status-pages"
        backLabel="Back to Status Pages"
        onRetry={() => refetch()}
      />
    );
  }

  if (!page) return null;

  return (
    <div className="space-y-6">
      <div className="flex items-start justify-between gap-4">
        <div className="min-w-0">
          <h1 className="text-2xl font-bold flex items-center gap-2">
            <Palette className="h-5 w-5 shrink-0" />
            <span className="truncate">{t("appearance.title")}</span>
          </h1>
          <p className="text-muted-foreground mt-1 truncate">
            {page.name} — /{page.slug}
          </p>
        </div>
        <div className="flex gap-2 shrink-0">
          <Link
            to="/orgs/$org/status-pages/$statusPageUid"
            params={{ org, statusPageUid }}
          >
            <Button
              variant="ghost"
              size="icon"
              aria-label={t("appearance.back")}
            >
              <ArrowLeft className="h-4 w-4" />
            </Button>
          </Link>
          <a
            href={`/status0/${org}/${page.slug}`}
            target="_blank"
            rel="noopener noreferrer"
          >
            <Button variant="outline" size="sm">
              <ExternalLink className="sm:mr-2 h-4 w-4" />
              <span className="hidden sm:inline">{t("detail.view")}</span>
            </Button>
          </a>
        </div>
      </div>

      {/* Editor + preview: side by side from lg up, stacked on mobile. */}
      <div className="grid gap-6 lg:grid-cols-2 items-start">
        <Card>
          <CardHeader>
            <CardTitle>{t("appearance.editorTitle")}</CardTitle>
            <CardDescription>{t("appearance.description")}</CardDescription>
          </CardHeader>
          <CardContent className="space-y-4">
            <div className="space-y-2">
              <Label htmlFor="custom-css">{t("appearance.cssLabel")}</Label>
              <CodeTextarea
                id="custom-css"
                data-testid="custom-css-input"
                rows={22}
                value={value}
                onChange={(event) => setCss(event.target.value)}
                placeholder={t("appearance.placeholder")}
              />
              <p className="text-xs text-muted-foreground">
                {t("appearance.limitsHint")}
              </p>
            </div>

            {hasImport && (
              <p className="text-xs text-destructive" role="alert">
                {t("appearance.importRejected")}
              </p>
            )}
            {tooLarge && (
              <p className="text-xs text-destructive" role="alert">
                {t("appearance.tooLarge")}
              </p>
            )}

            <div className="flex flex-wrap gap-2">
              <Button
                onClick={handleSave}
                disabled={!dirty || invalid || update.isPending}
                data-testid="custom-css-save"
              >
                {update.isPending && (
                  <Loader2 className="mr-2 h-4 w-4 animate-spin" />
                )}
                {t("appearance.save")}
              </Button>
              {value === "" && (
                <Button
                  variant="outline"
                  onClick={() => setCss(STARTER_TEMPLATE)}
                  data-testid="custom-css-template"
                >
                  {t("appearance.insertTemplate")}
                </Button>
              )}
              {dirty && (
                <Button
                  variant="ghost"
                  onClick={() => setCss(null)}
                >
                  {t("appearance.reset")}
                </Button>
              )}
              <Button
                variant="ghost"
                onClick={() =>
                  navigate({
                    to: "/orgs/$org/status-pages/$statusPageUid",
                    params: { org, statusPageUid },
                  })
                }
              >
                {t("appearance.done")}
              </Button>
            </div>
          </CardContent>
        </Card>

        <Card>
          <CardHeader>
            <CardTitle>{t("appearance.previewTitle")}</CardTitle>
            <CardDescription>
              {t("appearance.previewDescription")}
            </CardDescription>
          </CardHeader>
          <CardContent>
            <div className="overflow-hidden rounded-md border border-border">
              <iframe
                ref={iframeRef}
                data-testid="custom-css-preview"
                title={t("appearance.previewTitle")}
                src={previewUrl}
                onLoad={() => setIframeReady(true)}
                className="h-[32rem] w-full bg-background lg:h-[46rem]"
              />
            </div>
          </CardContent>
        </Card>
      </div>

      <StatusPageBadgeCard org={org} pageSlug={page.slug} pageName={page.name} />

      <StatusPageWidgetCard org={org} pageSlug={page.slug} />
    </div>
  );
}
