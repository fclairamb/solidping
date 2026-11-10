import { useTranslation } from "react-i18next";
import { Link } from "@tanstack/react-router";
import { toast } from "sonner";

import { useNonCompliantChecks, useUpdateOrgSettings, type OrgSettings } from "@/api/hooks";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Label } from "@/components/ui/label";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { canSealInBrowser } from "@/lib/sealing";

/**
 * "Check secret encryption" (spec 2026-11-08-01 §1, §6): `optional` (server
 * or browser, per check) or `required` (browser only), with the checks a
 * `required` org still has to fix. `required` cannot be picked from a page
 * that cannot seal itself (no secure context).
 */
export function BrowserSealingCard({ org, settings }: { org: string; settings?: OrgSettings }) {
  const { t } = useTranslation("org");
  const update = useUpdateOrgSettings(org);
  const mode = settings?.browserSealingMode ?? "optional";
  const { data: nonCompliant } = useNonCompliantChecks(org, { enabled: mode === "required" });
  const secure = canSealInBrowser();

  const onChange = async (next: string) => {
    try {
      await update.mutateAsync({ browserSealingMode: next as "optional" | "required" });
      toast.success(t("settings.browserSealing.saved"));
    } catch {
      toast.error(t("settings.browserSealing.failed"));
    }
  };

  return (
    <Card className="mt-6" data-testid="browser-sealing-card">
      <CardHeader>
        <CardTitle>{t("settings.browserSealing.title")}</CardTitle>
        <CardDescription>{t("settings.browserSealing.description")}</CardDescription>
      </CardHeader>
      <CardContent className="space-y-3">
        <div className="space-y-2">
          <Label>{t("settings.browserSealing.mode")}</Label>
          <Select value={mode} onValueChange={onChange} disabled={update.isPending}>
            <SelectTrigger className="w-full sm:w-80" data-testid="browser-sealing-mode-select">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="optional" data-testid="browser-sealing-mode-optional">
                {t("settings.browserSealing.optional")}
              </SelectItem>
              <SelectItem value="required" disabled={!secure} data-testid="browser-sealing-mode-required">
                {t("settings.browserSealing.required")}
              </SelectItem>
            </SelectContent>
          </Select>
          {!secure && (
            <p className="text-xs text-muted-foreground" data-testid="browser-sealing-insecure">
              {t("settings.browserSealing.insecure")}
            </p>
          )}
        </div>
        {mode === "required" && (
          <div className="space-y-1" data-testid="browser-sealing-noncompliant">
            <p className="text-sm font-medium">
              {t("settings.browserSealing.toFix", { count: nonCompliant?.length ?? 0 })}
            </p>
            <ul className="space-y-1 text-sm">
              {nonCompliant?.map((check) => (
                <li key={check.uid} data-testid="browser-sealing-noncompliant-item">
                  <Link
                    to="/orgs/$org/checks/$checkUid/edit"
                    params={{ org, checkUid: check.uid }}
                    className="underline"
                    data-testid={`browser-sealing-fix-${check.slug}`}
                  >
                    {check.name}
                  </Link>{" "}
                  <span className="text-xs text-muted-foreground">
                    {check.reason === "sharedRegion"
                      ? t("settings.browserSealing.reasonSharedRegion")
                      : t("settings.browserSealing.reasonServer")}
                  </span>
                </li>
              ))}
            </ul>
          </div>
        )}
      </CardContent>
    </Card>
  );
}
