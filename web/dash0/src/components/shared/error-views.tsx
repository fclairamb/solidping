import { useState } from "react";
import { Link } from "@tanstack/react-router";
import { Button } from "@/components/ui/button";
import {
  Card,
  CardContent,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import { ShieldX, FileQuestion, AlertTriangle, RefreshCw, ChevronDown, ChevronUp } from "lucide-react";
import { ApiError, NetworkError } from "@/api/client";
import { useTranslation } from "react-i18next";

export function PermissionDenied({ org }: { org: string }) {
  const { t } = useTranslation();

  return (
    <div className="text-center py-12">
      <Card className="max-w-md mx-auto">
        <CardHeader>
          <div className="flex justify-center mb-2">
            <ShieldX className="h-10 w-10 text-destructive" />
          </div>
          <CardTitle>{t("permissionDenied")}</CardTitle>
        </CardHeader>
        <CardContent>
          <p className="text-muted-foreground mb-4">
            {t("permissionDeniedDescription")}
          </p>
          <Link to="/orgs/$org" params={{ org }}>
            <Button variant="outline">{t("returnToDashboard")}</Button>
          </Link>
        </CardContent>
      </Card>
    </div>
  );
}

export function NotFound({
  resource,
  backTo,
  backLabel,
  org,
}: {
  resource: string;
  backTo: string;
  backLabel: string;
  org: string;
}) {
  const { t } = useTranslation();

  return (
    <div className="text-center py-12">
      <Card className="max-w-md mx-auto">
        <CardHeader>
          <div className="flex justify-center mb-2">
            <FileQuestion className="h-10 w-10 text-muted-foreground" />
          </div>
          <CardTitle>{t("notFound", { resource })}</CardTitle>
        </CardHeader>
        <CardContent>
          <p className="text-muted-foreground mb-4">
            {t("notFoundDescription", { resource: resource.toLowerCase() })}
          </p>
          <Link to={backTo} params={{ org }}>
            <Button variant="outline">{backLabel}</Button>
          </Link>
        </CardContent>
      </Card>
    </div>
  );
}

export function ServerError({
  detail,
  onRetry,
}: {
  detail?: string;
  onRetry?: () => void;
}) {
  const { t } = useTranslation();
  const [showDetail, setShowDetail] = useState(false);

  return (
    <div className="text-center py-12">
      <Card className="max-w-md mx-auto">
        <CardHeader>
          <div className="flex justify-center mb-2">
            <AlertTriangle className="h-10 w-10 text-destructive" />
          </div>
          <CardTitle>{t("somethingWentWrong")}</CardTitle>
        </CardHeader>
        <CardContent>
          <p className="text-muted-foreground mb-4">
            {t("unexpectedError")}
          </p>
          {onRetry && (
            <Button onClick={onRetry} className="mb-2">
              <RefreshCw className="mr-2 h-4 w-4" />
              {t("retry")}
            </Button>
          )}
          {detail && (
            <div className="mt-4">
              <button
                onClick={() => setShowDetail(!showDetail)}
                className="text-xs text-muted-foreground hover:text-foreground inline-flex items-center gap-1"
              >
                {showDetail ? <ChevronUp className="h-3 w-3" /> : <ChevronDown className="h-3 w-3" />}
                {showDetail ? t("hideDetails") : t("showDetails")}
              </button>
              {showDetail && (
                <div className="mt-2 bg-muted rounded-md p-3 text-xs text-left font-mono">
                  {detail}
                </div>
              )}
            </div>
          )}
        </CardContent>
      </Card>
    </div>
  );
}

export function QueryErrorView({
  error,
  org,
  resource,
  backTo,
  backLabel,
  onRetry,
}: {
  error: Error;
  org: string;
  resource?: string;
  backTo?: string;
  backLabel?: string;
  onRetry?: () => void;
}) {
  const { t } = useTranslation();

  if (error instanceof ApiError) {
    if (error.status === 403) {
      return <PermissionDenied org={org} />;
    }
    if (error.status === 404 && resource && backTo) {
      return (
        <NotFound
          resource={resource}
          backTo={backTo}
          backLabel={backLabel || t("backTo", { resource })}
          org={org}
        />
      );
    }
    return <ServerError detail={error.detail || error.message} onRetry={onRetry} />;
  }

  if (error instanceof NetworkError) {
    return (
      <ServerError
        detail={t("connectionError")}
        onRetry={onRetry}
      />
    );
  }

  return <ServerError detail={error.message} onRetry={onRetry} />;
}
