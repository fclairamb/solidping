import { useTranslation } from "react-i18next";
import { AlertCircle } from "lucide-react";
import { Alert, AlertDescription } from "@/components/ui/alert";
import { CopyableCode } from "@/components/shared/copyable-code";
import { useDiscordSetup } from "@/api/hooks";
import { showLocalhostWarning } from "@/lib/discord-setup";

/**
 * The exact values to register in the Discord developer portal, as computed by
 * the server (the same helpers build the URIs the login and install flows send).
 */
export function DiscordSetupPanel() {
  const { t } = useTranslation("server");
  const { data } = useDiscordSetup();

  if (!data) return null;

  const warn = showLocalhostWarning(
    data.baseUrlIsDefault,
    window.location.hostname,
  );
  const rows = [
    {
      id: "login",
      label: t("auth.discordLoginRedirectLabel"),
      value: data.loginRedirectUri,
    },
    {
      id: "install",
      label: t("auth.discordInstallRedirectLabel"),
      value: data.installRedirectUri,
    },
    {
      id: "interactions",
      label: t("auth.discordInteractionsLabel"),
      value: data.interactionsUrl,
    },
  ];

  return (
    <div
      className="space-y-3 rounded-md border bg-muted/50 p-3"
      data-testid="discord-setup"
    >
      <p className="text-sm font-medium">{t("auth.discordSetupTitle")}</p>
      <p className="text-xs text-muted-foreground">
        {t("auth.discordSetupInfo")}
      </p>
      {warn && (
        <Alert variant="destructive" data-testid="discord-localhost-warning">
          <AlertCircle className="h-4 w-4" />
          <AlertDescription>
            {t("auth.discordLocalhostWarning", {
              baseUrl: data.baseUrl,
              host: window.location.host,
            })}
          </AlertDescription>
        </Alert>
      )}
      {rows.map((row) => (
        <div key={row.id} className="space-y-1">
          <p className="text-xs text-muted-foreground">{row.label}</p>
          <CopyableCode code={row.value} data-testid={`discord-setup-${row.id}`} />
        </div>
      ))}
    </div>
  );
}
