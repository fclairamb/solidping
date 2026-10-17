import { useTranslation } from "react-i18next";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Badge } from "@/components/ui/badge";

interface VendorIncident {
  name?: string;
  impact?: string;
  status?: string;
  shortlink?: string;
}

/** The incidents the vendor reports, from the last result's output. */
export function VendorStatusCard({ output }: { output: Record<string, unknown> | undefined }) {
  const { t } = useTranslation("checks");
  const raw = output?.incidents;
  const incidents = (Array.isArray(raw) ? raw : []) as VendorIncident[];

  return (
    <Card data-testid="vendor-status-card">
      <CardHeader>
        <CardTitle>{t("vendorStatus.detail.title")}</CardTitle>
      </CardHeader>
      <CardContent>
        {incidents.length === 0 ? (
          <p className="text-sm text-muted-foreground" data-testid="vendor-status-no-incidents">
            {t("vendorStatus.detail.noIncidents")}
          </p>
        ) : (
          <ul className="divide-y" data-testid="vendor-status-incidents">
            {incidents.map((incident, index) => (
              <li
                key={`${incident.shortlink ?? incident.name}-${index}`}
                className="flex flex-wrap items-center gap-2 py-2"
                data-testid="vendor-status-incident"
              >
                <span className="font-medium">{incident.name}</span>
                {incident.impact && <Badge variant="outline">{incident.impact}</Badge>}
                {incident.shortlink && /^https:\/\//.test(incident.shortlink) && (
                  <a
                    href={incident.shortlink}
                    target="_blank"
                    rel="noreferrer noopener"
                    className="text-sm text-primary underline"
                  >
                    {incident.shortlink}
                  </a>
                )}
              </li>
            ))}
          </ul>
        )}
      </CardContent>
    </Card>
  );
}
