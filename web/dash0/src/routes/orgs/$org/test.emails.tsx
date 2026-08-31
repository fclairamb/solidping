// Dev-facing email template catalog: renders every shipped email template
// through the REAL formatter (premailer CSS inlining included), so the design
// pass on server/internal/email/templates/ can be iterated on visually.
//
// It lives under /orgs/$org/test, whose layout already gates on
// runMode === "test" and degrades gracefully outside it — the same gate the
// backend route carries. Nothing here is a user-facing feature.
import { useMemo, useState } from "react";
import { createFileRoute, useNavigate } from "@tanstack/react-router";
import { useTranslation } from "react-i18next";
import { useQuery } from "@tanstack/react-query";
import { AlertTriangle, ExternalLink, Mail, RefreshCw } from "lucide-react";

import { useEmailPreviewIndex, type EmailTemplateSummary } from "@/api/hooks";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import { Skeleton } from "@/components/ui/skeleton";
import { SegmentedControl } from "@/components/ui/segmented-control";
import { cn } from "@/lib/utils";

type PreviewFormat = "html" | "text";
/**
 * Which rendering of the template to preview. An <iframe> cannot be told to
 * report `prefers-color-scheme: dark` to the document it hosts, so "dark" is
 * served by the endpoint instead: it rewrites the template's own dark media
 * query to `@media all`. What is on screen is therefore the shipped CSS, not a
 * preview-only palette.
 */
type PreviewScheme = "light" | "dark";

interface EmailsSearch {
  template?: string;
  format?: PreviewFormat;
  colorScheme?: PreviewScheme;
}

export const Route = createFileRoute("/orgs/$org/test/emails")({
  validateSearch: (search: Record<string, unknown>): EmailsSearch => ({
    template:
      typeof search.template === "string" && search.template.length > 0
        ? search.template
        : undefined,
    format: search.format === "text" ? "text" : undefined,
    colorScheme: search.colorScheme === "dark" ? "dark" : undefined,
  }),
  component: EmailTemplatesTab,
});

/** Strips the .html suffix for display — the file name is still the id. */
function displayName(template: string): string {
  return template.replace(/\.html$/, "");
}

function previewUrl(
  template: string,
  format: PreviewFormat,
  scheme: PreviewScheme = "light",
): string {
  const base = `/api/mgmt/email-preview/${template}?format=${format}`;

  // Only appended for dark: the light URL stays the one the endpoint serves
  // untouched, so "no param" remains the exact bytes the mailer sends.
  return scheme === "dark" ? `${base}&colorScheme=dark` : base;
}

/**
 * Fetches the plaintext part. The HTML part goes straight into an iframe via
 * its src, but the text part is fetched so it lands in the parent document —
 * readable with real wrapping, and assertable from E2E.
 */
function useTextPreview(template: string | undefined) {
  return useQuery({
    queryKey: ["email-preview-text", template],
    enabled: Boolean(template),
    queryFn: async () => {
      const response = await fetch(previewUrl(template as string, "text"));
      if (!response.ok) {
        throw new Error(`preview failed: ${response.status}`);
      }
      return response.text();
    },
  });
}

function EmailTemplatesTab() {
  const { t } = useTranslation("nav");
  const navigate = useNavigate();
  const search = Route.useSearch();

  const { data, isLoading, isError, refetch, isFetching } =
    useEmailPreviewIndex();

  const templates: EmailTemplateSummary[] = useMemo(
    () => data?.data ?? [],
    [data],
  );

  // Seeded from the URL on mount and written through on every change: search
  // params under a layout route are not reliably present on a cold deep-link,
  // so local state is the source of truth and the URL is kept in step.
  const [picked, setPicked] = useState<string | undefined>(search.template);
  const [format, setFormat] = useState<PreviewFormat>(search.format ?? "html");
  const [scheme, setScheme] = useState<PreviewScheme>(
    search.colorScheme ?? "light",
  );

  // Derived, not synced through an effect: the selection falls back to the
  // first template until the user picks one, and also when the URL named a
  // template that no longer exists.
  const selected =
    picked && templates.some((row) => row.template === picked)
      ? picked
      : templates[0]?.template;

  const writeThrough = (next: EmailsSearch) => {
    void navigate({
      to: ".",
      search: (prev: EmailsSearch) => ({ ...prev, ...next }),
      replace: true,
    });
  };

  const selectTemplate = (template: string) => {
    setPicked(template);
    writeThrough({ template });
  };

  const selectFormat = (next: PreviewFormat) => {
    setFormat(next);
    writeThrough({ format: next === "text" ? "text" : undefined });
  };

  const selectScheme = (next: PreviewScheme) => {
    setScheme(next);
    writeThrough({ colorScheme: next === "dark" ? "dark" : undefined });
  };

  const current = templates.find((row) => row.template === selected);
  const textPreview = useTextPreview(
    format === "text" ? current?.template : undefined,
  );

  if (isError) {
    return (
      <Card>
        <CardHeader>
          <CardTitle className="flex items-center gap-2">
            <AlertTriangle className="h-4 w-4 text-destructive" />
            {t("test.emails.unavailableTitle")}
          </CardTitle>
          <CardDescription>{t("test.emails.unavailableBody")}</CardDescription>
        </CardHeader>
        <CardContent>
          <Button variant="outline" size="sm" onClick={() => void refetch()}>
            <RefreshCw className="h-4 w-4" />
            {t("test.emails.retry")}
          </Button>
        </CardContent>
      </Card>
    );
  }

  return (
    <div className="space-y-4" data-testid="email-preview-page">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <div>
          <h2 className="text-lg font-semibold">{t("test.emails.title")}</h2>
          <p className="text-sm text-muted-foreground">
            {t("test.emails.description")}
          </p>
        </div>
        <div className="flex flex-wrap items-center gap-2">
          <SegmentedControl<PreviewFormat>
            aria-label={t("test.emails.formatLabel")}
            value={format}
            onValueChange={selectFormat}
            options={[
              {
                value: "html",
                label: t("test.emails.formatHtml"),
                testId: "email-preview-format-html",
              },
              {
                value: "text",
                label: t("test.emails.formatText"),
                testId: "email-preview-format-text",
              },
            ]}
          />
          {/* Only meaningful for the HTML part — the plaintext alternative has
              no styling to switch. */}
          {format === "html" && (
            <SegmentedControl<PreviewScheme>
              aria-label={t("test.emails.schemeLabel")}
              value={scheme}
              onValueChange={selectScheme}
              options={[
                {
                  value: "light",
                  label: t("test.emails.schemeLight"),
                  testId: "email-preview-scheme-light",
                },
                {
                  value: "dark",
                  label: t("test.emails.schemeDark"),
                  tooltip: t("test.emails.schemeDarkHint"),
                  testId: "email-preview-scheme-dark",
                },
              ]}
            />
          )}
          <Button
            variant="outline"
            size="sm"
            onClick={() => void refetch()}
            disabled={isFetching}
          >
            <RefreshCw className={cn("h-4 w-4", isFetching && "animate-spin")} />
            {t("test.emails.refresh")}
          </Button>
        </div>
      </div>

      <div className="grid gap-4 lg:grid-cols-[minmax(0,260px)_minmax(0,1fr)]">
        <Card className="lg:sticky lg:top-4 lg:self-start">
          <CardHeader className="pb-2">
            <CardTitle className="flex items-center gap-2 text-base">
              <Mail className="h-4 w-4" />
              {t("test.emails.listTitle")}
              {templates.length > 0 && (
                <Badge variant="secondary">{templates.length}</Badge>
              )}
            </CardTitle>
          </CardHeader>
          <CardContent className="max-h-[60vh] overflow-y-auto px-2">
            {isLoading ? (
              <div className="space-y-2 p-2">
                {Array.from({ length: 8 }).map((_, index) => (
                  <Skeleton key={index} className="h-8 w-full" />
                ))}
              </div>
            ) : (
              <ul className="space-y-1" data-testid="email-preview-list">
                {templates.map((row) => (
                  <li key={row.template}>
                    <button
                      type="button"
                      onClick={() => selectTemplate(row.template)}
                      aria-current={row.template === selected}
                      data-testid={`email-preview-item-${displayName(row.template)}`}
                      className={cn(
                        "w-full truncate rounded-md px-2 py-1.5 text-left text-sm transition-colors",
                        row.template === selected
                          ? "bg-accent font-medium text-accent-foreground"
                          : "hover:bg-muted",
                      )}
                    >
                      {displayName(row.template)}
                      {row.error && (
                        <AlertTriangle className="ml-1 inline h-3 w-3 text-destructive" />
                      )}
                    </button>
                  </li>
                ))}
              </ul>
            )}
          </CardContent>
        </Card>

        <Card className="min-w-0">
          <CardHeader className="gap-1 pb-3">
            <CardDescription>{t("test.emails.subjectLabel")}</CardDescription>
            <CardTitle
              className="text-base break-words"
              data-testid="email-preview-subject"
            >
              {current?.subject || t("test.emails.noSubject")}
            </CardTitle>
            {current && (
              <div className="flex flex-wrap items-center gap-2 pt-1">
                <code className="text-xs text-muted-foreground">
                  {current.template}
                </code>
                {!current.hasText && (
                  <Badge variant="outline">{t("test.emails.noTextPart")}</Badge>
                )}
                <a
                  className="inline-flex items-center gap-1 text-xs text-primary hover:underline"
                  href={previewUrl(current.template, format, scheme)}
                  target="_blank"
                  rel="noreferrer"
                >
                  <ExternalLink className="h-3 w-3" />
                  {t("test.emails.openRaw")}
                </a>
              </div>
            )}
          </CardHeader>
          <CardContent className="min-w-0">
            {current?.error && (
              <p
                className="mb-3 rounded-md border border-destructive/40 bg-destructive/5 p-3 text-sm text-destructive"
                data-testid="email-preview-error"
              >
                {current.error}
              </p>
            )}
            {!current ? (
              <Skeleton className="h-[480px] w-full" />
            ) : format === "html" ? (
              <iframe
                key={`${current.template}-html-${scheme}`}
                title={t("test.emails.iframeTitle", {
                  template: current.template,
                })}
                data-testid="email-preview-frame"
                data-scheme={scheme}
                src={previewUrl(current.template, "html", scheme)}
                // The surround is part of the test: a dark card judged against a
                // white pane reads far better than it does in a real dark inbox.
                // Hard-coded rather than themed — this mirrors the mail client's
                // own backdrop, not the dashboard's theme.
                className={cn(
                  "h-[70vh] min-h-[420px] w-full rounded-md border",
                  scheme === "dark" ? "bg-[#0d151d]" : "bg-white",
                )}
              />
            ) : (
              <pre
                data-testid="email-preview-text"
                className="max-h-[70vh] overflow-auto rounded-md border bg-muted/40 p-4 text-xs whitespace-pre-wrap"
              >
                {textPreview.isLoading
                  ? t("test.emails.loading")
                  : (textPreview.data ?? t("test.emails.noTextPart"))}
              </pre>
            )}
          </CardContent>
        </Card>
      </div>
    </div>
  );
}
