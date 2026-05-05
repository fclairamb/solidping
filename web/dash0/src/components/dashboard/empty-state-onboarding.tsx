import { useState } from "react";
import { useTranslation } from "react-i18next";
import { useQueryClient } from "@tanstack/react-query";
import { Globe, Network, Shield, Loader2, Plus } from "lucide-react";
import { Card, CardContent } from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Alert, AlertDescription } from "@/components/ui/alert";
import { useCreateCheck } from "@/api/hooks";
import { ApiError } from "@/api/client";

type QuickType = "http" | "icmp" | "ssl";

interface EmptyStateOnboardingProps {
  org: string;
}

const QUICK_DEFS: Record<
  QuickType,
  {
    icon: typeof Globe;
    inputType: "url" | "text";
    placeholder: string;
    field: "url" | "host" | "domain";
    namePrefix: string;
  }
> = {
  http: {
    icon: Globe,
    inputType: "url",
    placeholder: "https://example.com",
    field: "url",
    namePrefix: "HTTP",
  },
  icmp: {
    icon: Network,
    inputType: "text",
    placeholder: "example.com",
    field: "host",
    namePrefix: "Ping",
  },
  ssl: {
    icon: Shield,
    inputType: "text",
    placeholder: "example.com",
    field: "domain",
    namePrefix: "SSL",
  },
};

// EmptyStateOnboarding renders the focused "create your first check" hero
// shown on the org dashboard when there are zero checks. Three quick-start
// chips (HTTP / Ping / SSL) let a user create the first check with a single
// input, no other fields. The full editor is one click away if they want
// the long form.
export function EmptyStateOnboarding({ org }: EmptyStateOnboardingProps) {
  const { t } = useTranslation("dashboard");
  const [tab, setTab] = useState<QuickType>("http");
  const [value, setValue] = useState("");
  const [error, setError] = useState<string | null>(null);
  const queryClient = useQueryClient();
  const createCheck = useCreateCheck(org);

  const def = QUICK_DEFS[tab];
  const Icon = def.icon;

  async function handleSubmit(e: React.FormEvent) {
    e.preventDefault();
    setError(null);

    const trimmed = value.trim();
    if (!trimmed) return;

    try {
      await createCheck.mutateAsync({
        name: `${def.namePrefix} — ${displayHostFor(def.field, trimmed)}`,
        type: tab,
        config: { [def.field]: trimmed },
      });
      setValue("");
      // Refresh checks so the dashboard re-renders with the regular view.
      queryClient.invalidateQueries({ queryKey: ["checks", org] });
    } catch (err) {
      setError(err instanceof ApiError ? err.message : t("welcome.unexpectedError"));
    }
  }

  return (
    <Card className="border-2 border-blue-200 dark:border-blue-800 bg-blue-50/50 dark:bg-blue-950/30">
      <CardContent className="pt-6 pb-8 flex flex-col items-center text-center gap-6">
        <div className="rounded-full bg-blue-100 dark:bg-blue-900 p-4">
          <Plus className="h-8 w-8 text-blue-600 dark:text-blue-400" />
        </div>
        <div>
          <h2 className="text-2xl font-bold">{t("welcome.title")}</h2>
          <p className="text-muted-foreground mt-1">
            {t("welcome.firstCheckSubtitle", "Pick a check type and tell us what to monitor.")}
          </p>
        </div>

        <div className="flex gap-2 flex-wrap justify-center">
          {(["http", "icmp", "ssl"] as const).map((quick) => {
            const QIcon = QUICK_DEFS[quick].icon;
            return (
              <Button
                key={quick}
                type="button"
                variant={tab === quick ? "default" : "outline"}
                size="sm"
                onClick={() => {
                  setTab(quick);
                  setValue("");
                  setError(null);
                }}
                data-testid={`quick-start-${quick}`}
              >
                <QIcon className="h-4 w-4 mr-1" />
                {t(`welcome.quick.${quick}`, quick === "icmp" ? "Ping" : quick.toUpperCase())}
              </Button>
            );
          })}
        </div>

        <form onSubmit={handleSubmit} className="w-full max-w-md space-y-3">
          <div className="space-y-1.5 text-left">
            <Label htmlFor="quick-input">
              <Icon className="inline h-4 w-4 mr-1 align-text-bottom" />
              {t(`welcome.quickLabel.${tab}`, def.placeholder)}
            </Label>
            <Input
              id="quick-input"
              type={def.inputType}
              required
              placeholder={def.placeholder}
              value={value}
              onChange={(e) => {
                setValue(e.target.value);
                setError(null);
              }}
              disabled={createCheck.isPending}
              data-testid="quick-start-input"
            />
          </div>

          {error && (
            <Alert variant="destructive">
              <AlertDescription>{error}</AlertDescription>
            </Alert>
          )}

          <Button
            type="submit"
            className="w-full"
            disabled={createCheck.isPending || !value.trim()}
            data-testid="quick-start-submit"
          >
            {createCheck.isPending ? (
              <>
                <Loader2 className="h-4 w-4 mr-2 animate-spin" />
                {t("welcome.creating", "Creating...")}
              </>
            ) : (
              t("welcome.create", "Create check")
            )}
          </Button>
        </form>

        <p className="text-xs text-muted-foreground">
          {t("welcome.advancedHint", "Need more control?")} {" "}
          <a
            href={`/dash0/orgs/${org}/checks/new`}
            className="underline"
          >
            {t("welcome.advancedLink", "Open the full check editor")}
          </a>
        </p>
      </CardContent>
    </Card>
  );
}

function displayHostFor(field: string, value: string): string {
  if (field !== "url") return value;

  try {
    return new URL(value).hostname;
  } catch {
    return value;
  }
}
