import { useState } from "react";
import { useTranslation } from "react-i18next";
import { Link } from "@tanstack/react-router";
import { ChevronDown, MessageSquare, Phone } from "lucide-react";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardTitle } from "@/components/ui/card";
import {
  Collapsible,
  CollapsibleContent,
  CollapsibleTrigger,
} from "@/components/ui/collapsible";
import { cn } from "@/lib/utils";
import { useInstanceSMSConfig } from "@/api/public-config";
import type { Integration } from "@/api/hooks";

/**
 * Which credentials this organization's SMS actually goes out on.
 *
 * Mirrors the backend resolver exactly: a per-organization Twilio integration
 * wins, the instance-level provider is the fallback, and "unavailable" is what
 * is left when neither exists.
 */
export type SMSMode = "bring-your-own" | "server-provided" | "unavailable";

/**
 * Resolves the organization's effective SMS mode from its integrations and the
 * instance's public config. Exported so it can be unit-tested and reused
 * without rendering.
 */
export function resolveSMSMode(
  integrations: Integration[] | undefined,
  instanceEnabled: boolean,
): { mode: SMSMode; ownIntegration?: Integration } {
  const own = (integrations ?? []).find(
    (i) => i.type === "twilio" && i.enabled,
  );

  if (own) {
    return { mode: "bring-your-own", ownIntegration: own };
  }

  return { mode: instanceEnabled ? "server-provided" : "unavailable" };
}

/**
 * Explains — rather than merely implements — where this organization's SMS
 * comes from.
 *
 * The two modes are invisible otherwise: an admin looking at an empty
 * integrations list has no way to tell "SMS works, the server provides it"
 * from "SMS is not configured", and an admin who adds a Twilio integration has
 * no way to know it silently overrode the instance provider, or what deleting
 * it would do. Both are the kind of thing you find out at 3 a.m.
 */
export function SMSModePanel({
  org,
  integrations,
  isLoading,
}: {
  org: string;
  integrations: Integration[] | undefined;
  isLoading?: boolean;
}) {
  const { t } = useTranslation("integrations");
  const instance = useInstanceSMSConfig();
  const { mode, ownIntegration } = resolveSMSMode(integrations, instance.enabled);
  // Collapsed by default in every mode, including "unavailable" — this is a
  // contextual explainer an admin reads once, not primary page content.
  const [open, setOpen] = useState(false);

  // Nothing to explain while the list is still loading — a panel that flips
  // from "unavailable" to "server-provided" mid-render is worse than none.
  if (isLoading) {
    return null;
  }

  const badge =
    mode === "bring-your-own" ? (
      <Badge variant="default" data-testid="sms-mode-badge">
        {t("sms.mode.byo", "Your own account")}
      </Badge>
    ) : mode === "server-provided" ? (
      <Badge variant="success" data-testid="sms-mode-badge">
        {t("sms.mode.server", "Provided by this instance")}
      </Badge>
    ) : (
      <Badge variant="secondary" data-testid="sms-mode-badge">
        {t("sms.mode.unavailable", "Not available")}
      </Badge>
    );

  return (
    <Card className="shadow-card" data-testid="sms-mode-panel">
      <Collapsible open={open} onOpenChange={setOpen}>
        <CollapsibleTrigger
          type="button"
          data-testid="sms-mode-panel-trigger"
          className="flex w-full items-start justify-between gap-3 rounded-xl p-6 text-left outline-none transition-colors hover:bg-muted/40 focus-visible:ring-2 focus-visible:ring-ring"
        >
          <div className="flex min-w-0 flex-1 flex-wrap items-start justify-between gap-3">
            <div className="min-w-0 space-y-1.5">
              <CardTitle className="flex items-center gap-2 text-base font-semibold">
                <MessageSquare className="h-4 w-4 shrink-0" />
                {t("sms.title", "SMS & voice")}
              </CardTitle>
              <CardDescription>
                {t(
                  "sms.subtitle",
                  "Where this organization's text messages and alert calls are sent from.",
                )}
              </CardDescription>
            </div>
            {badge}
          </div>
          <ChevronDown
            className={cn(
              "mt-1 h-4 w-4 shrink-0 text-muted-foreground transition-transform",
              open && "rotate-180",
            )}
          />
        </CollapsibleTrigger>

        <CollapsibleContent>
          <CardContent className="space-y-3 pt-0 text-sm text-muted-foreground">
            {mode === "server-provided" && (
              <>
                <p data-testid="sms-mode-explanation">
                  {instance.sender
                    ? t(
                        "sms.server.bodyWithSender",
                        "SMS is provided by this instance. Messages go out from {{sender}} — no setup needed.",
                        { sender: instance.sender },
                      )
                    : t(
                        "sms.server.body",
                        "SMS is provided by this instance — no setup needed.",
                      )}
                </p>
                <p>
                  {t(
                    "sms.server.override",
                    "You may add your own Twilio integration to send with your own account, sender and billing instead. It overrides the instance provider for this organization.",
                  )}
                </p>
              </>
            )}

            {mode === "bring-your-own" && (
              <>
                <p data-testid="sms-mode-explanation">
                  {t(
                    "sms.byo.body",
                    'This organization sends through its own Twilio integration ("{{name}}"). It overrides the instance provider for this organization\'s SMS and voice, and Twilio bills you directly.',
                    { name: ownIntegration?.name ?? "" },
                  )}
                </p>
                <p>
                  {instance.enabled
                    ? t(
                        "sms.byo.onDeleteFallback",
                        "Delete it and this organization falls back to the SMS provided by this instance.",
                      )
                    : t(
                        "sms.byo.onDeleteUnavailable",
                        "Delete it and SMS becomes unavailable for this organization — this instance provides no SMS of its own.",
                      )}
                </p>
              </>
            )}

            {mode === "unavailable" && (
              <>
                <p data-testid="sms-mode-explanation">
                  {t(
                    "sms.unavailable.body",
                    "This instance provides no SMS, and this organization has no Twilio integration of its own, so SMS and voice alerts cannot be sent.",
                  )}
                </p>
                <p>
                  {t(
                    "sms.unavailable.fix",
                    "Add a Twilio integration to use your own account, or ask the operator to configure SMS for this instance.",
                  )}
                </p>
              </>
            )}

            <p className="flex items-center gap-2">
              <Phone className="h-3.5 w-3.5 shrink-0" />
              {mode === "bring-your-own"
                ? t(
                    "sms.voice.byo",
                    "Alert calls use the same Twilio account, when it has a caller ID configured.",
                  )
                : instance.voiceEnabled
                  ? t(
                      "sms.voice.server",
                      "Alert calls are provided by this instance.",
                    )
                  : t(
                      "sms.voice.none",
                      "Alert calls are not available — no voice provider is configured.",
                    )}
            </p>

            {mode !== "bring-your-own" && (
              <Button variant="outline" size="sm" asChild>
                <Link
                  to="/orgs/$org/integrations/new"
                  params={{ org }}
                  search={{ type: "twilio" }}
                  data-testid="sms-add-own-twilio"
                >
                  {t("sms.addOwn", "Use my own Twilio account")}
                </Link>
              </Button>
            )}
          </CardContent>
        </CollapsibleContent>
      </Collapsible>
    </Card>
  );
}
