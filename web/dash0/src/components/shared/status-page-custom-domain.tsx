import { useState } from "react";
import { useTranslation } from "react-i18next";
import { Link } from "@tanstack/react-router";
import {
  CheckCircle2,
  Globe,
  Loader2,
  Lock,
  ShieldAlert,
  Trash2,
} from "lucide-react";
import { toast } from "sonner";
import {
  useUpdateStatusPage,
  useVerifyStatusPageDomain,
  type StatusPage,
} from "@/api/hooks";
import { ApiError } from "@/api/client";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
  AlertDialogTrigger,
} from "@/components/ui/alert-dialog";
import { DnsRecordRow } from "@/components/shared/dns-record-row";
import { DocsLink } from "@/components/shared/docs-link";

export function StatusPageCustomDomain({
  org,
  page,
}: {
  org: string;
  page: StatusPage;
}) {
  const { t } = useTranslation("statusPages");
  const update = useUpdateStatusPage(org, page.uid);
  const verify = useVerifyStatusPageDomain(org, page.uid);

  const [domain, setDomain] = useState(page.customDomain ?? "");
  const [quotaBlocked, setQuotaBlocked] = useState(false);

  const currentDomain = page.customDomain ?? "";
  const isVerified = page.customDomainStatus === "verified";
  const records = page.customDomainRecords ?? [];
  const dirty = domain.trim() !== currentDomain;
  // Only present when the server terminates TLS itself (acme.enabled). With an
  // external TLS proxy the field is absent and no chip is rendered.
  const certStatus = page.customDomainCertStatus;

  const handleSave = async () => {
    setQuotaBlocked(false);
    try {
      await update.mutateAsync({ customDomain: domain.trim() });
      toast.success(t("customDomain.savedToast"));
    } catch (err) {
      if (err instanceof ApiError && err.status === 402) {
        setQuotaBlocked(true);
        return;
      }
      if (err instanceof ApiError && err.status === 409) {
        toast.error(t("customDomain.taken"));
        return;
      }
      if (err instanceof ApiError && err.status === 400) {
        toast.error(t("customDomain.invalid"));
        return;
      }
      toast.error(t("customDomain.saveFailed"));
    }
  };

  const handleRemove = async () => {
    try {
      await update.mutateAsync({ customDomain: null });
      setDomain("");
      setQuotaBlocked(false);
      toast.success(t("customDomain.removedToast"));
    } catch {
      toast.error(t("customDomain.saveFailed"));
    }
  };

  const handleVerify = async () => {
    try {
      const updated = await verify.mutateAsync();
      if (updated.customDomainStatus === "verified") {
        toast.success(t("customDomain.verifiedToast"));
      } else {
        toast.error(t("customDomain.notVerifiedToast"));
      }
    } catch {
      toast.error(t("customDomain.notVerifiedToast"));
    }
  };

  return (
    <Card data-testid="status-page-custom-domain">
      <CardHeader className="flex flex-row items-start justify-between gap-3 space-y-0">
        <div>
          <CardTitle className="flex items-center gap-2">
            <Globe className="h-4 w-4" />
            {t("customDomain.title")}
          </CardTitle>
          <CardDescription>{t("customDomain.description")}</CardDescription>
        </div>
        <DocsLink href="/docs/features/custom-domains" />
      </CardHeader>
      <CardContent className="space-y-4">
        <div className="space-y-2">
          <Label htmlFor="customDomain">{t("customDomain.domainLabel")}</Label>
          <div className="flex flex-col gap-2 sm:flex-row">
            <Input
              id="customDomain"
              data-testid="custom-domain-input"
              value={domain}
              onChange={(e) => setDomain(e.target.value)}
              placeholder={t("customDomain.domainPlaceholder")}
              autoCapitalize="none"
              autoCorrect="off"
              spellCheck={false}
              className="font-mono"
            />
            <Button
              type="button"
              data-testid="custom-domain-save"
              onClick={handleSave}
              disabled={update.isPending || !dirty}
              className="shrink-0"
            >
              {update.isPending && <Loader2 className="mr-2 h-4 w-4 animate-spin" />}
              {t("customDomain.save")}
            </Button>
          </div>
        </div>

        {quotaBlocked && (
          <Alert>
            <AlertTitle>{t("customDomain.quotaTitle")}</AlertTitle>
            <AlertDescription className="space-y-2">
              <p>{t("customDomain.quotaDescription")}</p>
              <Link
                to="/orgs/$org/organization/usage"
                params={{ org }}
                className="font-medium underline underline-offset-4"
              >
                {t("customDomain.viewUsage")}
              </Link>
            </AlertDescription>
          </Alert>
        )}

        {currentDomain && (
          <div className="space-y-4 border-t pt-4">
            <div className="flex flex-wrap items-center justify-between gap-2">
              <div className="flex items-center gap-2">
                {isVerified ? (
                  <Badge variant="success" className="gap-1">
                    <CheckCircle2 className="h-3 w-3" />
                    {t("customDomain.verified")}
                  </Badge>
                ) : (
                  <Badge variant="secondary">
                    {t("customDomain.unverified")}
                  </Badge>
                )}
                {certStatus === "issued" && (
                  <Badge
                    variant="success"
                    className="gap-1"
                    data-testid="custom-domain-cert-status"
                  >
                    <Lock className="h-3 w-3" />
                    {t("customDomain.certIssued")}
                  </Badge>
                )}
                {certStatus === "error" && (
                  <Badge
                    variant="destructive"
                    className="gap-1"
                    data-testid="custom-domain-cert-status"
                  >
                    <ShieldAlert className="h-3 w-3" />
                    {t("customDomain.certError")}
                  </Badge>
                )}
                {certStatus === "none" && (
                  <Badge
                    variant="secondary"
                    className="gap-1"
                    data-testid="custom-domain-cert-status"
                  >
                    {t("customDomain.certPending")}
                  </Badge>
                )}
                <span className="font-mono text-sm">{currentDomain}</span>
              </div>
              <div className="flex items-center gap-2">
                <Button
                  type="button"
                  data-testid="custom-domain-verify"
                  variant="outline"
                  size="sm"
                  onClick={handleVerify}
                  disabled={verify.isPending}
                >
                  {verify.isPending && (
                    <Loader2 className="mr-2 h-4 w-4 animate-spin" />
                  )}
                  {verify.isPending
                    ? t("customDomain.verifying")
                    : t("customDomain.verify")}
                </Button>
                <AlertDialog>
                  <AlertDialogTrigger asChild>
                    <Button
                      type="button"
                      variant="ghost"
                      size="icon"
                      className="text-destructive hover:text-destructive"
                      aria-label={t("customDomain.remove")}
                    >
                      <Trash2 className="h-4 w-4" />
                    </Button>
                  </AlertDialogTrigger>
                  <AlertDialogContent>
                    <AlertDialogHeader>
                      <AlertDialogTitle>
                        {t("customDomain.removeConfirmTitle")}
                      </AlertDialogTitle>
                      <AlertDialogDescription>
                        {t("customDomain.removeConfirmDescription")}
                      </AlertDialogDescription>
                    </AlertDialogHeader>
                    <AlertDialogFooter>
                      <AlertDialogCancel>
                        {t("customDomain.cancel")}
                      </AlertDialogCancel>
                      <AlertDialogAction
                        onClick={handleRemove}
                        className="bg-destructive text-white hover:bg-destructive/90"
                      >
                        {t("customDomain.remove")}
                      </AlertDialogAction>
                    </AlertDialogFooter>
                  </AlertDialogContent>
                </AlertDialog>
              </div>
            </div>

            {isVerified && certStatus === "none" && (
              <p className="text-xs text-muted-foreground">
                {t("customDomain.certPendingHint")}
              </p>
            )}

            {records.length > 0 && (
              <div className="space-y-2" data-testid="custom-domain-records">
                <p className="text-sm text-muted-foreground">
                  {t("customDomain.recordsIntro")}
                </p>
                <div className="space-y-2">
                  {records.map((record) => (
                    <DnsRecordRow key={record.type + record.name} record={record} />
                  ))}
                </div>
              </div>
            )}
          </div>
        )}
      </CardContent>
    </Card>
  );
}
