import { useState } from "react";
import { useTranslation } from "react-i18next";
import { createFileRoute, Link } from "@tanstack/react-router";
import { AlertCircle, ArrowLeft, Check, Loader2 } from "lucide-react";

import { Alert, AlertDescription } from "@/components/ui/alert";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { ApiError } from "@/api/client";
import { PermissionDenied } from "@/components/shared/error-views";
import { useAdminUpdateUser, useAdminUser } from "@/api/hooks";
import type { AdminUserRow } from "@/api/hooks";

/**
 * Super-admin edit page for one user (spec 2026-09-30-08). Only the email
 * today: this is how the seeded admin@solidping.io gets a real address.
 */
export const Route = createFileRoute("/orgs/$org/server/users/$uid")({
  component: UserEditPage,
});

function UserEditPage() {
  const { t } = useTranslation("server");
  const { org, uid } = Route.useParams();
  const { data, isLoading, error } = useAdminUser(uid);

  return (
    <div className="space-y-6">
      <Button variant="ghost" size="sm" asChild className="-ml-2">
        <Link to="/orgs/$org/server/users" params={{ org }}>
          <ArrowLeft className="mr-2 h-4 w-4" />
          {t("users.edit.back")}
        </Link>
      </Button>

      {isLoading ? (
        <div className="flex items-center justify-center py-8">
          <Loader2 className="h-6 w-6 animate-spin text-muted-foreground" />
        </div>
      ) : error instanceof ApiError && error.status === 403 ? (
        // 403 renders Permission Denied in place, never a redirect.
        <PermissionDenied org={org} />
      ) : error instanceof ApiError && error.status === 404 ? (
        <p className="text-sm text-destructive">{t("users.edit.notFound")}</p>
      ) : error || !data ? (
        <p className="text-sm text-destructive">{t("users.edit.loadError")}</p>
      ) : (
        // Keyed on the user: a refetch after a save keeps the form (and its
        // "saved" notice) mounted.
        <UserEmailForm key={data.uid} user={data} />
      )}
    </div>
  );
}

function UserEmailForm({ user }: { user: AdminUserRow }) {
  const { t } = useTranslation("server");
  const update = useAdminUpdateUser(user.uid);
  const [email, setEmail] = useState(user.email);
  const [error, setError] = useState<string | null>(null);
  const [saved, setSaved] = useState(false);

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault();
    setError(null);
    setSaved(false);

    try {
      await update.mutateAsync({ email });
      setSaved(true);
    } catch (err) {
      if (err instanceof ApiError && err.status === 409) {
        setError(t("users.edit.errors.conflict"));
      } else if (err instanceof ApiError && err.status === 400) {
        setError(t("users.edit.errors.invalid"));
      } else {
        setError(err instanceof Error ? err.message : String(err));
      }
    }
  };

  return (
    <Card>
      <CardHeader>
        <CardTitle className="break-all">
          {t("users.edit.title", { email: user.email })}
        </CardTitle>
        <p className="text-sm text-muted-foreground">
          {t("users.edit.description")}
        </p>
        <div className="flex flex-wrap gap-1">
          {user.superAdmin ? (
            <Badge variant="secondary">{t("users.flags.superAdmin")}</Badge>
          ) : null}
          {!user.emailVerified ? (
            <Badge variant="warning">{t("users.flags.unverified")}</Badge>
          ) : null}
        </div>
      </CardHeader>
      <CardContent>
        <form onSubmit={handleSubmit} className="space-y-4 sm:max-w-md">
          {error ? (
            <Alert variant="destructive">
              <AlertCircle className="h-4 w-4" />
              <AlertDescription data-testid="user-edit-error">
                {error}
              </AlertDescription>
            </Alert>
          ) : null}
          {saved ? (
            <Alert>
              <Check className="h-4 w-4" />
              <AlertDescription data-testid="user-edit-saved">
                {t("users.edit.saved")}
              </AlertDescription>
            </Alert>
          ) : null}

          <div className="space-y-2">
            <Label htmlFor="user-edit-email">{t("users.edit.email")}</Label>
            <Input
              id="user-edit-email"
              type="email"
              required
              value={email}
              onChange={(e) => setEmail(e.target.value)}
              disabled={update.isPending}
              data-testid="user-edit-email"
            />
            <p className="text-sm text-muted-foreground">
              {t("users.edit.emailHelp")}
            </p>
          </div>

          <Button
            type="submit"
            className="w-full sm:w-auto"
            disabled={update.isPending || !email || email === user.email}
            data-testid="user-edit-save"
          >
            {update.isPending ? (
              <Loader2 className="mr-2 h-4 w-4 animate-spin" />
            ) : null}
            {t("users.edit.save")}
          </Button>
        </form>
      </CardContent>
    </Card>
  );
}
