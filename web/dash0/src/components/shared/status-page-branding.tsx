import { useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { Image as ImageIcon, Trash2, Upload } from "lucide-react";
import { toast } from "sonner";
import {
  useClearStatusPageAsset,
  useUploadStatusPageAsset,
  type StatusPage,
  type StatusPageAssetKind,
} from "@/api/hooks";
import { ApiError } from "@/api/client";
import { Button } from "@/components/ui/button";
import { Label } from "@/components/ui/label";
import { Alert, AlertDescription } from "@/components/ui/alert";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";

// Mirror the server allowlists (handlers/statuspageassets) so the file picker
// never offers something the upload would reject. The favicon list drops JPEG
// and adds .ico — a lossy photo format has no business being a 32x32 icon.
const LOGO_ACCEPT = "image/png,image/jpeg,image/webp,image/gif,image/svg+xml";
const FAVICON_ACCEPT =
  "image/x-icon,image/vnd.microsoft.icon,image/png,image/webp,image/gif,image/svg+xml";

interface AssetSlotProps {
  org: string;
  page: StatusPage;
  kind: StatusPageAssetKind;
  label: string;
  hint: string;
  accept: string;
  currentUrl?: string;
  /** Preview box size in px — a favicon is shown at icon scale, not logo scale. */
  previewSize: number;
}

/**
 * One asset slot: preview, replace, remove.
 *
 * The two slots are otherwise identical, so they share this component rather
 * than being written twice — the only real differences are the MIME allowlist,
 * the size of the preview, and the copy.
 */
function AssetSlot({
  org,
  page,
  kind,
  label,
  hint,
  accept,
  currentUrl,
  previewSize,
}: AssetSlotProps) {
  const { t } = useTranslation("statusPages");
  const upload = useUploadStatusPageAsset(org, page.uid, kind);
  const clear = useClearStatusPageAsset(org, page.uid, kind);
  const fileInput = useRef<HTMLInputElement>(null);
  const [error, setError] = useState<string | null>(null);

  const busy = upload.isPending || clear.isPending;

  const report = (err: unknown) => {
    const message =
      err instanceof ApiError ? err.message : t("branding.unexpectedError");
    setError(message);
    toast.error(message);
  };

  const handleFile = async (event: React.ChangeEvent<HTMLInputElement>) => {
    const file = event.target.files?.[0];
    // Reset the input BEFORE awaiting: re-picking the same file after a failed
    // upload must still fire a change event.
    event.target.value = "";
    if (!file) return;

    setError(null);
    try {
      await upload.mutateAsync(file);
      toast.success(t("branding.uploaded"));
    } catch (err) {
      report(err);
    }
  };

  const handleClear = async () => {
    setError(null);
    try {
      await clear.mutateAsync();
      toast.success(t("branding.removed"));
    } catch (err) {
      report(err);
    }
  };

  return (
    <div className="space-y-2" data-testid={`status-page-asset-${kind}`}>
      <Label>{label}</Label>
      {/* Wraps on narrow viewports so the preview, the two buttons and the
          hint stack instead of overflowing on a phone. */}
      <div className="flex flex-wrap items-center gap-3">
        <div
          className="flex shrink-0 items-center justify-center rounded-md border bg-muted/40"
          style={{ width: previewSize, height: previewSize }}
        >
          {currentUrl ? (
            <img
              src={currentUrl}
              alt={label}
              className="max-h-full max-w-full object-contain p-1"
              data-testid={`status-page-asset-preview-${kind}`}
            />
          ) : (
            <ImageIcon className="h-4 w-4 text-muted-foreground" aria-hidden />
          )}
        </div>
        <input
          ref={fileInput}
          type="file"
          accept={accept}
          className="hidden"
          onChange={handleFile}
          data-testid={`status-page-asset-input-${kind}`}
        />
        <Button
          type="button"
          variant="outline"
          size="sm"
          disabled={busy}
          onClick={() => fileInput.current?.click()}
          data-testid={`status-page-asset-upload-${kind}`}
        >
          <Upload className="mr-2 h-4 w-4" />
          {currentUrl ? t("branding.replace") : t("branding.upload")}
        </Button>
        {currentUrl && (
          <Button
            type="button"
            variant="ghost"
            size="sm"
            className="text-destructive"
            disabled={busy}
            onClick={handleClear}
            data-testid={`status-page-asset-remove-${kind}`}
          >
            <Trash2 className="mr-2 h-4 w-4" />
            {t("branding.remove")}
          </Button>
        )}
      </div>
      <p className="text-xs text-muted-foreground">{hint}</p>
      {error && (
        <Alert variant="destructive">
          <AlertDescription data-testid={`status-page-asset-error-${kind}`}>
            {error}
          </AlertDescription>
        </Alert>
      )}
    </div>
  );
}

/**
 * Brand assets for one status page (spec 2026-08-21-07).
 *
 * Uploads go to dedicated endpoints rather than through the page's PATCH: the
 * payload is a binary blob, and the server needs the multipart body bounded
 * before it is parsed. That is also why this card sits next to the form rather
 * than inside it — nothing here participates in the form's submit.
 */
export function StatusPageBranding({
  org,
  page,
}: {
  org: string;
  page: StatusPage;
}) {
  const { t } = useTranslation("statusPages");

  return (
    <Card data-testid="status-page-branding-card">
      <CardHeader>
        <CardTitle className="flex items-center gap-2">
          <ImageIcon className="h-4 w-4" />
          {t("branding.title")}
        </CardTitle>
        <CardDescription>{t("branding.description")}</CardDescription>
      </CardHeader>
      <CardContent className="space-y-6">
        <AssetSlot
          org={org}
          page={page}
          kind="logo"
          label={t("branding.logo")}
          hint={t("branding.logoHint")}
          accept={LOGO_ACCEPT}
          currentUrl={page.logoUrl}
          previewSize={64}
        />
        <AssetSlot
          org={org}
          page={page}
          kind="favicon"
          label={t("branding.favicon")}
          hint={t("branding.faviconHint")}
          accept={FAVICON_ACCEPT}
          currentUrl={page.faviconUrl}
          previewSize={40}
        />
      </CardContent>
    </Card>
  );
}
