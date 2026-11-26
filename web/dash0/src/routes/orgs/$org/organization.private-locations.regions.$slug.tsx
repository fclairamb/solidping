import { useState } from "react";
import { useTranslation } from "react-i18next";
import { createFileRoute, Link, useNavigate } from "@tanstack/react-router";
import { ArrowLeft, Loader2 } from "lucide-react";
import { toast } from "sonner";

import {
  usePrivateRegions,
  useUpdatePrivateRegion,
  type PrivateRegion,
} from "@/api/hooks";
import { ApiError } from "@/api/client";
import { Button } from "@/components/ui/button";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Skeleton } from "@/components/ui/skeleton";
import { QueryErrorView } from "@/components/shared/error-views";

// Private location edit page: name and emoji. The slug is the location's
// identity (agents, tokens, keys and checks reference it) and stays read-only.
export const Route = createFileRoute(
  "/orgs/$org/organization/private-locations/regions/$slug",
)({
  component: RegionEditPage,
});

function RegionEditPage() {
  const { t } = useTranslation(["org"]);
  const { org, slug } = Route.useParams();
  const { data: regions, isLoading, error, refetch } = usePrivateRegions(org);
  const region = regions?.find((row) => row.slug === slug);

  if (error) {
    return (
      <QueryErrorView
        error={error}
        org={org}
        resource={t("privateLocations.regionEdit.title")}
        backTo="/orgs/$org/organization/private-locations"
        backLabel={t("privateLocations.reseal.back")}
        onRetry={() => refetch()}
      />
    );
  }

  if (isLoading) {
    return <Skeleton className="h-48 max-w-2xl" />;
  }

  if (!region) {
    return (
      <p
        className="text-sm text-muted-foreground"
        data-testid="region-edit-not-found"
      >
        {t("privateLocations.regionEdit.notFound")}
      </p>
    );
  }

  return <RegionEditForm org={org} region={region} />;
}

function RegionEditForm({
  org,
  region,
}: {
  org: string;
  region: PrivateRegion;
}) {
  const { t } = useTranslation(["org", "common"]);
  const navigate = useNavigate();
  const updateRegion = useUpdatePrivateRegion(org);
  const [name, setName] = useState(region.name);
  const [emoji, setEmoji] = useState(region.emoji);

  const goBack = () =>
    navigate({
      to: "/orgs/$org/organization/private-locations",
      params: { org },
    });

  const onSubmit = async (event: React.FormEvent) => {
    event.preventDefault();
    try {
      await updateRegion.mutateAsync({
        slug: region.slug,
        name: name.trim(),
        emoji: emoji.trim(),
      });
      toast.success(t("privateLocations.regionEdit.saved"));
      goBack();
    } catch (err) {
      toast.error(
        err instanceof ApiError
          ? err.message
          : t("privateLocations.regionEdit.saveFailed"),
      );
    }
  };

  return (
    <form
      onSubmit={onSubmit}
      className="max-w-2xl space-y-4"
      data-testid="region-edit-form"
    >
      <Button
        asChild
        variant="ghost"
        size="sm"
        data-testid="region-edit-back-button"
      >
        <Link
          to="/orgs/$org/organization/private-locations"
          params={{ org }}
          data-testid="region-edit-back-link"
        >
          <ArrowLeft className="h-4 w-4" />
          {t("privateLocations.reseal.back")}
        </Link>
      </Button>
      <Card>
        <CardHeader>
          <CardTitle>{t("privateLocations.regionEdit.title")}</CardTitle>
          <CardDescription>
            {t("privateLocations.regionEdit.description")}
          </CardDescription>
        </CardHeader>
        <CardContent className="space-y-4">
          <div className="space-y-2">
            <Label htmlFor="region-edit-name">
              {t("privateLocations.regions.displayName")}
            </Label>
            <Input
              id="region-edit-name"
              data-testid="region-edit-name-input"
              value={name}
              onChange={(event) => setName(event.target.value)}
              maxLength={100}
              required
            />
          </div>
          <div className="space-y-2">
            <Label htmlFor="region-edit-emoji">
              {t("privateLocations.regionEdit.emoji")}
            </Label>
            <Input
              id="region-edit-emoji"
              data-testid="region-edit-emoji-input"
              value={emoji}
              onChange={(event) => setEmoji(event.target.value)}
              maxLength={32}
              className="w-24"
            />
          </div>
          <div className="space-y-2">
            <Label htmlFor="region-edit-slug">
              {t("privateLocations.regions.slug")}
            </Label>
            <Input
              id="region-edit-slug"
              data-testid="region-edit-slug-input"
              value={region.region}
              disabled
            />
            <p className="text-xs text-muted-foreground">
              {t("privateLocations.regionEdit.slugHelp")}
            </p>
          </div>
        </CardContent>
      </Card>
      <div className="flex justify-end gap-2">
        <Button
          type="button"
          variant="outline"
          onClick={goBack}
          data-testid="region-edit-cancel"
        >
          {t("common:cancel")}
        </Button>
        <Button
          type="submit"
          disabled={updateRegion.isPending || !name.trim()}
          data-testid="region-edit-submit"
        >
          {updateRegion.isPending && (
            <Loader2 className="mr-2 h-4 w-4 animate-spin" />
          )}
          {t("common:save")}
        </Button>
      </div>
    </form>
  );
}
