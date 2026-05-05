import { useTranslation } from "react-i18next";
import { createFileRoute } from "@tanstack/react-router";
import { useQuery } from "@tanstack/react-query";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import { Loader2 } from "lucide-react";
import { apiFetch } from "@/api/client";

export const Route = createFileRoute("/orgs/$org/server/activation")({
  component: ActivationPage,
});

interface ActivationFunnelRow {
  organizationUid: string;
  slug: string;
  name: string;
  signupAt?: string;
  firstCheckAt?: string;
  firstResultAt?: string;
  firstNotifierAt?: string;
  firstIncidentAt?: string;
  orgCreatedAt: string;
}

interface ActivationFunnelResponse {
  data: ActivationFunnelRow[];
}

function fmtTs(value?: string): string {
  if (!value) return "—";
  try {
    return new Date(value).toLocaleString();
  } catch {
    return value;
  }
}

function ActivationPage() {
  const { t } = useTranslation("server");

  const { data, isLoading, error } = useQuery({
    queryKey: ["system", "activation"],
    queryFn: () => apiFetch<ActivationFunnelResponse>("/api/v1/system/activation"),
    refetchInterval: 60_000,
  });

  return (
    <Card>
      <CardHeader>
        <CardTitle>
          {t("activation.title", "Activation funnel")}
        </CardTitle>
        <p className="text-sm text-muted-foreground">
          {t(
            "activation.description",
            "Per-organization timestamps for each activation milestone.",
          )}
        </p>
      </CardHeader>
      <CardContent>
        {isLoading ? (
          <div className="flex items-center justify-center py-8">
            <Loader2 className="h-6 w-6 animate-spin text-muted-foreground" />
          </div>
        ) : error ? (
          <p className="text-destructive text-sm">
            {t("activation.error", "Failed to load activation funnel.")}
          </p>
        ) : (
          <div className="overflow-x-auto">
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>{t("activation.org", "Organization")}</TableHead>
                  <TableHead>{t("activation.signup", "Signup")}</TableHead>
                  <TableHead>{t("activation.firstCheck", "First check")}</TableHead>
                  <TableHead>{t("activation.firstResult", "First result")}</TableHead>
                  <TableHead>{t("activation.firstNotifier", "First notifier")}</TableHead>
                  <TableHead>{t("activation.firstIncident", "First page")}</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {(data?.data || []).map((row) => (
                  <TableRow key={row.organizationUid}>
                    <TableCell>
                      <div className="font-medium">{row.name || row.slug}</div>
                      <div className="text-xs text-muted-foreground">{row.slug}</div>
                    </TableCell>
                    <TableCell className="text-sm">{fmtTs(row.signupAt)}</TableCell>
                    <TableCell className="text-sm">{fmtTs(row.firstCheckAt)}</TableCell>
                    <TableCell className="text-sm">{fmtTs(row.firstResultAt)}</TableCell>
                    <TableCell className="text-sm">{fmtTs(row.firstNotifierAt)}</TableCell>
                    <TableCell className="text-sm">{fmtTs(row.firstIncidentAt)}</TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          </div>
        )}
      </CardContent>
    </Card>
  );
}
