import { useTranslation } from "react-i18next";
import { Link } from "@tanstack/react-router";
import { FlaskConical } from "lucide-react";
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import { useAuth } from "@/contexts/AuthContext";

interface DemoBannerProps {
  /** Org slug, for the sign-up link. */
  org: string;
}

/**
 * DemoBanner tells a visitor that they are inside the shared public live demo
 * (spec 2026-09-06-02).
 *
 * It says the two things a visitor has to know before they trust anything they
 * see: everything here is visible to every other visitor, and anything they
 * create is deleted within the hour. That is not a warning about a malfunction
 * — it is the deal — so it is `info`, not `warning` or `destructive`.
 *
 * NOT DISMISSABLE, deliberately. A dismissable banner is one a visitor closes
 * in the first ten seconds and then spends twenty minutes not knowing their
 * work is temporary. It renders above the content on every page of the org for
 * exactly that reason.
 *
 * Renders nothing for an ordinary session, so this is inert on every
 * installation that has no demo.
 */
export function DemoBanner({ org }: DemoBannerProps) {
  const { t } = useTranslation(["org"]);
  const { user } = useAuth();

  if (!user?.isDemo) {
    return null;
  }

  return (
    <Alert className="mb-3" data-testid="demo-banner">
      <FlaskConical />
      <AlertTitle>{t("org:demo.title")}</AlertTitle>
      <AlertDescription className="space-y-2">
        <p>{t("org:demo.description")}</p>
        <div className="pt-1">
          {/* The in-app registration route on the same host, per the spec's
              resolved open question — not the marketing site. */}
          <Button asChild size="sm">
            <Link
              to="/orgs/$org/register"
              params={{ org }}
              data-testid="demo-banner-signup"
            >
              {t("org:demo.signUp")}
            </Link>
          </Button>
        </div>
      </AlertDescription>
    </Alert>
  );
}
