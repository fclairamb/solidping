import { useState } from "react";
import { useTranslation } from "react-i18next";
import { useQueryClient } from "@tanstack/react-query";
import { Link, useNavigate } from "@tanstack/react-router";
import { ArrowRight, Bot, Globe, Network, Shield, Loader2, Plus } from "lucide-react";
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

// QUICK_DEFS deliberately does NOT follow CHECK_TYPE_IDENTITY
// (check-type-identity.tsx), and that divergence is intentional, not drift:
// this surface is a beginner-facing vocabulary aimed at someone who hasn't
// created their first check yet, while CHECK_TYPE_IDENTITY is the precise
// label used everywhere a check already exists. `icmp` already reads
// "Ping" here vs. "ICMP" elsewhere. `ssl` keeps "SSL" here (both the chip
// label in welcome.quick.ssl / welcome.quickLabel.ssl and namePrefix below)
// even though every other surface renamed it to "TLS" (spec
// 2026-08-24-04): "SSL" is still the term a newcomer recognizes (e.g. "SSL
// certificate"), matching the same friendlier-over-precise choice already
// made for Ping/ICMP — see spec 2026-08-24-07 for the full reasoning. Do
// not "fix" this to TLS without redoing that analysis.
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
// input, no other fields. Two secondary paths sit below: connect an AI
// assistant through MCP (links to the per-client setup page under Account)
// and the full check editor for those who want the long form.
export function EmptyStateOnboarding({ org }: EmptyStateOnboardingProps) {
  const { t } = useTranslation("dashboard");
  const navigate = useNavigate();
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
      const created = await createCheck.mutateAsync({
        name: `${def.namePrefix} — ${displayHostFor(def.field, trimmed)}`,
        type: tab,
        config: { [def.field]: trimmed },
      });
      setValue("");
      // Refresh checks so the list (and this hero, once the user navigates
      // back to the dashboard) reflect the new check.
      queryClient.invalidateQueries({ queryKey: ["checks", org] });
      // Land on the check's own page — the moment of highest engagement
      // right after creating it — instead of staying on the dashboard.
      // Mirrors checks.new.tsx's post-create navigation.
      navigate({
        to: "/orgs/$org/checks/$checkUid",
        params: { org, checkUid: created.uid },
      });
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

        {/* Secondary path: connect an AI assistant through MCP and let it do
            the whole onboarding. Kept visually subordinate to the one-field
            quick-create form above — a divider plus a bordered sub-card
            linking to the existing per-client MCP setup page. */}
        <div
          className="flex w-full max-w-md items-center gap-3"
          aria-hidden="true"
        >
          <div className="h-px flex-1 bg-border" />
          <span className="text-xs uppercase text-muted-foreground">
            {t("welcome.or", "or")}
          </span>
          <div className="h-px flex-1 bg-border" />
        </div>

        <div className="flex w-full max-w-md flex-col gap-3 rounded-md border bg-card p-4 text-left sm:flex-row sm:items-center">
          <div className="flex flex-1 items-start gap-3">
            <Bot className="mt-0.5 h-5 w-5 shrink-0 text-primary" />
            <div>
              <p className="text-sm font-medium">
                {t("welcome.mcp.title", "Let AI set everything up")}
              </p>
              <p className="mt-0.5 text-xs text-muted-foreground">
                {t(
                  "welcome.mcp.description",
                  "Connect Claude, Cursor or VS Code to SolidPing's MCP server and ask it to create and configure all your checks for you.",
                )}
              </p>
            </div>
          </div>
          <Button
            asChild
            variant="outline"
            size="sm"
            className="shrink-0"
          >
            <Link
              to="/orgs/$org/account/mcp"
              params={{ org }}
              data-testid="quick-start-mcp-link"
            >
              {t("welcome.mcp.cta", "Set up MCP")}
              <ArrowRight className="ml-1 h-4 w-4" />
            </Link>
          </Button>
        </div>

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
