import { useState } from "react";
import { createFileRoute, useNavigate } from "@tanstack/react-router";
import { useTranslation } from "react-i18next";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Alert, AlertDescription } from "@/components/ui/alert";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Activity, AlertCircle, Loader2 } from "lucide-react";
import { ApiError, setToken } from "@/api/client";
import { useCreateOrg } from "@/api/hooks";
import { useAuth } from "@/contexts/AuthContext";

export const Route = createFileRoute("/no-org")({
  component: NoOrgPage,
});

function slugify(name: string): string {
  return name
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, "-")
    .replace(/^-|-$/g, "")
    .slice(0, 20);
}

function NoOrgPage() {
  const { t } = useTranslation("auth");
  const navigate = useNavigate();
  const createOrg = useCreateOrg();
  const { logout } = useAuth();

  const [name, setName] = useState("");
  const [slug, setSlug] = useState("");
  const [slugTouched, setSlugTouched] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const handleNameChange = (value: string) => {
    setName(value);
    if (!slugTouched) {
      setSlug(slugify(value));
    }
  };

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault();
    setError(null);

    try {
      const result = await createOrg.mutateAsync({ name, slug });
      if (result.accessToken) {
        setToken(result.accessToken);
      }
      navigate({ to: "/orgs/$org", params: { org: result.slug } });
    } catch (err) {
      if (err instanceof ApiError) {
        setError(err.message);
      } else {
        setError(t("unexpectedError"));
      }
    }
  };

  return (
    <div className="min-h-screen flex items-center justify-center bg-background p-4">
      <Card className="w-full max-w-md">
        <CardHeader className="text-center">
          <div className="flex justify-center mb-4">
            <Activity className="h-12 w-12 text-primary" />
          </div>
          <CardTitle className="text-2xl">{t("noOrg.welcome")}</CardTitle>
          <p className="text-sm text-muted-foreground mt-1">
            {t("noOrg.subtitle")}
          </p>
        </CardHeader>
        <CardContent>
          {error && (
            <Alert variant="destructive" className="mb-4">
              <AlertCircle className="h-4 w-4" />
              <AlertDescription>{error}</AlertDescription>
            </Alert>
          )}

          <form onSubmit={handleSubmit} className="space-y-4">
            <div className="space-y-2">
              <Label htmlFor="orgName">{t("noOrg.orgName")}</Label>
              <Input
                id="orgName"
                type="text"
                placeholder={t("noOrg.orgNamePlaceholder")}
                value={name}
                onChange={(e) => handleNameChange(e.target.value)}
                required
                disabled={createOrg.isPending}
              />
            </div>

            <div className="space-y-2">
              <Label htmlFor="orgSlug">{t("noOrg.slug")}</Label>
              <Input
                id="orgSlug"
                type="text"
                placeholder={t("noOrg.slugPlaceholder")}
                value={slug}
                onChange={(e) => {
                  setSlug(e.target.value);
                  setSlugTouched(true);
                }}
                required
                pattern="[a-z0-9][a-z0-9-]{1,18}[a-z0-9]"
                title={t("noOrg.slugTitle")}
                disabled={createOrg.isPending}
              />
              <p className="text-xs text-muted-foreground">
                {t("noOrg.slugHelp", { slug: slug || "my-company" })}
              </p>
            </div>

            <Button
              type="submit"
              className="w-full"
              disabled={createOrg.isPending}
            >
              {createOrg.isPending ? (
                <>
                  <Loader2 className="mr-2 h-4 w-4 animate-spin" />
                  {t("noOrg.creating")}
                </>
              ) : (
                t("noOrg.createOrg")
              )}
            </Button>
          </form>

          <div className="mt-4 text-center">
            <Button
              variant="ghost"
              size="sm"
              onClick={async () => {
                await logout();
                navigate({
                  to: "/orgs/$org/login",
                  params: { org: "default" },
                  search: { session_expired: false, returnTo: undefined },
                });
              }}
            >
              {t("noOrg.signOut")}
            </Button>
          </div>
        </CardContent>
      </Card>
    </div>
  );
}
