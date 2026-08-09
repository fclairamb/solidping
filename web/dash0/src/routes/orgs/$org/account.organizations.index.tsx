import { useState } from "react";
import { createFileRoute, Link, useNavigate } from "@tanstack/react-router";
import { useTranslation } from "react-i18next";
import { toast } from "sonner";
import { Building, Plus } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { Card, CardContent } from "@/components/ui/card";
import { ApiError } from "@/api/client";
import { useAuth } from "@/contexts/AuthContext";

export const Route = createFileRoute("/orgs/$org/account/organizations/")({
  component: OrganizationsIndexPage,
});

function OrganizationsIndexPage() {
  const { t } = useTranslation("account");
  const { org } = Route.useParams();
  const navigate = useNavigate();
  const { organizations, switchOrg } = useAuth();
  const [switchingSlug, setSwitchingSlug] = useState<string | null>(null);

  // The current org first, then the rest alphabetically — mirrors the
  // sessions list's "current row first" convention (account.sessions.tsx).
  const sorted = [...organizations].sort((a, b) => {
    if (a.slug === org) return -1;
    if (b.slug === org) return 1;
    return (a.name || a.slug).localeCompare(b.name || b.slug);
  });

  const handleSwitch = async (slug: string) => {
    setSwitchingSlug(slug);
    try {
      await switchOrg(slug);
      // switchOrg only updates AuthContext's state — the URL still names the
      // old org, so the route params would disagree with the session. Follow
      // AppSidebar's handleSwitchOrg pattern and navigate to the same page
      // under the new org's slug.
      navigate({
        to: "/orgs/$org/account/organizations",
        params: { org: slug },
      });
    } catch (err) {
      toast.error(
        err instanceof ApiError ? err.message : t("organizations.switchFailed"),
      );
    } finally {
      setSwitchingSlug(null);
    }
  };

  return (
    <div className="space-y-6">
      <div className="flex items-center justify-between gap-4">
        <div>
          <h2 className="text-lg font-semibold">{t("organizations.title")}</h2>
          <p className="text-sm text-muted-foreground">
            {t("organizations.subtitle")}
          </p>
        </div>
        <Button asChild data-testid="new-organization-button">
          <Link to="/orgs/$org/account/organizations/new" params={{ org }}>
            <Plus className="mr-2 h-4 w-4" />
            <span className="hidden sm:inline">
              {t("organizations.newOrganization")}
            </span>
          </Link>
        </Button>
      </div>

      <div className="space-y-3">
        {sorted.map((o) => {
          const isCurrent = o.slug === org;
          return (
            <Card
              key={o.slug}
              data-testid={`organization-row-${o.slug}`}
              className={isCurrent ? "border-primary" : undefined}
            >
              <CardContent className="flex flex-col gap-3 p-4 sm:flex-row sm:items-center sm:justify-between">
                <div className="flex min-w-0 items-center gap-3">
                  <div className="flex h-9 w-9 shrink-0 items-center justify-center overflow-hidden rounded bg-muted">
                    {o.logoUrl ? (
                      <img
                        src={o.logoUrl}
                        alt=""
                        className="h-9 w-9 object-contain"
                        data-testid={`organization-logo-${o.slug}`}
                      />
                    ) : (
                      <Building className="h-5 w-5 text-muted-foreground" />
                    )}
                  </div>
                  <div className="min-w-0">
                    <div className="flex flex-wrap items-center gap-2">
                      <span className="truncate font-medium">
                        {o.name || o.slug}
                      </span>
                      {isCurrent && (
                        <Badge
                          data-testid="organization-current-badge"
                          className="border-primary"
                        >
                          {t("organizations.current")}
                        </Badge>
                      )}
                    </div>
                    <div className="text-xs text-muted-foreground">
                      {o.slug} · {t("organizations.role")}: {o.role}
                    </div>
                  </div>
                </div>
                {!isCurrent && (
                  <Button
                    variant="outline"
                    size="sm"
                    className="shrink-0"
                    onClick={() => handleSwitch(o.slug)}
                    disabled={switchingSlug === o.slug}
                    data-testid={`organization-switch-${o.slug}`}
                  >
                    {t("organizations.switch")}
                  </Button>
                )}
              </CardContent>
            </Card>
          );
        })}
      </div>
    </div>
  );
}
