import { createFileRoute, useNavigate } from "@tanstack/react-router";
import { useTranslation } from "react-i18next";
import { toast } from "sonner";
import { useState } from "react";
import { Plus, Loader2 } from "lucide-react";
import { useCreateCheck, type CreateCheckRequest } from "@/api/hooks";
import { Button } from "@/components/ui/button";
import {
  Card,
  CardContent,
  CardDescription,
  CardFooter,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";

export const Route = createFileRoute("/orgs/$org/test/templates")({
  component: TemplatesTab,
});

interface CheckTemplate {
  name: string;
  slug: string;
  description: string;
  period: string;
  fakeParams: string;
  behavior: string;
}

// Template names/descriptions/behavior are kept in English on purpose: they
// describe fixed test fixtures and would be confusing to half-translate.
const checkTemplates: CheckTemplate[] = [
  {
    name: "Fake API (Stable)",
    slug: "http-fake-stable",
    description: "Always up with a 24-hour cycle",
    period: "00:00:10",
    fakeParams: "period=86400",
    behavior: "Should never fail during normal testing",
  },
  {
    name: "Fake API (Flaky)",
    slug: "http-fake-flaky",
    description: "Up 60s, down 60s",
    period: "00:00:15",
    fakeParams: "period=120",
    behavior: "Triggers incidents and recoveries regularly",
  },
  {
    name: "Fake API (Unstable)",
    slug: "http-fake-unstable",
    description: "Up 20s, down 20s",
    period: "00:00:15",
    fakeParams: "period=40",
    behavior: "Frequent status changes",
  },
  {
    name: "Fake API (Slow)",
    slug: "http-fake-slow",
    description: "Always up but with 2s response delay",
    period: "00:00:20",
    fakeParams: "period=86400&delay=2000",
    behavior: "Tests duration metrics and timeout behavior",
  },
  {
    name: "Fake API (503)",
    slug: "http-fake-503",
    description: "Returns 503 when down (30s cycles)",
    period: "00:00:15",
    fakeParams: "period=60&statusDown=503",
    behavior: "Tests status code handling",
  },
];

function TemplatesTab() {
  const { t } = useTranslation("nav");
  const { org } = Route.useParams();
  const navigate = useNavigate();
  const createCheck = useCreateCheck(org);

  const [creatingSlug, setCreatingSlug] = useState<string | null>(null);

  // Custom form state
  const [customCycle, setCustomCycle] = useState("120");
  const [customPeriod, setCustomPeriod] = useState("15");
  const [customStatusDown, setCustomStatusDown] = useState("500");
  const [customDelay, setCustomDelay] = useState("0");
  const [creatingCustom, setCreatingCustom] = useState(false);

  const buildRequest = (template: CheckTemplate): CreateCheckRequest => ({
    name: template.name,
    slug: template.slug,
    type: "http",
    config: {
      url: `${window.location.origin}/api/v1/fake?${template.fakeParams}`,
      method: "GET",
      expected_status: 200,
    },
    period: template.period,
    enabled: true,
  });

  const handleCreate = async (template: CheckTemplate) => {
    setCreatingSlug(template.slug);
    try {
      const check = await createCheck.mutateAsync(buildRequest(template));
      toast.success(t("test.templates.createdToast", { name: template.name }), {
        action: {
          label: t("test.templates.viewAction"),
          onClick: () =>
            navigate({
              to: "/orgs/$org/checks/$checkUid",
              params: { org, checkUid: check.uid },
              search: { graphPeriod: undefined, graphFull: undefined },
            }),
        },
      });
    } catch (err) {
      const message =
        err instanceof Error ? err.message : t("test.templates.createFailed");
      toast.error(message);
    } finally {
      setCreatingSlug(null);
    }
  };

  const handleCreateAll = async () => {
    setCreatingSlug("all");
    let created = 0;
    for (const template of checkTemplates) {
      try {
        await createCheck.mutateAsync(buildRequest(template));
        created++;
      } catch {
        // Skip duplicates silently
      }
    }
    toast.success(t("test.templates.createdAllToast", { count: created }));
    setCreatingSlug(null);
  };

  const handleCreateCustom = async () => {
    setCreatingCustom(true);
    const params = new URLSearchParams();
    params.set("period", customCycle);
    if (customStatusDown !== "500") params.set("statusDown", customStatusDown);
    if (customDelay !== "0") params.set("delay", customDelay);

    try {
      const check = await createCheck.mutateAsync({
        name: `Fake API (Custom ${customCycle}s)`,
        type: "http",
        config: {
          url: `${window.location.origin}/api/v1/fake?${params.toString()}`,
          method: "GET",
          expected_status: 200,
        },
        period: `00:00:${customPeriod.padStart(2, "0")}`,
        enabled: true,
      });
      toast.success(t("test.templates.createdCustom"), {
        action: {
          label: t("test.templates.viewAction"),
          onClick: () =>
            navigate({
              to: "/orgs/$org/checks/$checkUid",
              params: { org, checkUid: check.uid },
              search: { graphPeriod: undefined, graphFull: undefined },
            }),
        },
      });
    } catch (err) {
      const message =
        err instanceof Error ? err.message : t("test.templates.createFailed");
      toast.error(message);
    } finally {
      setCreatingCustom(false);
    }
  };

  return (
    <div className="space-y-6">
      <div className="flex items-center justify-end">
        <Button
          onClick={handleCreateAll}
          disabled={creatingSlug === "all"}
          variant="outline"
        >
          {creatingSlug === "all" ? (
            <Loader2 className="mr-2 h-4 w-4 animate-spin" />
          ) : (
            <Plus className="mr-2 h-4 w-4" />
          )}
          {t("test.templates.createAll")}
        </Button>
      </div>

      <div className="grid grid-cols-1 md:grid-cols-2 lg:grid-cols-3 gap-4">
        {checkTemplates.map((template) => (
          <Card key={template.slug}>
            <CardHeader>
              <CardTitle className="text-base">{template.name}</CardTitle>
              <CardDescription>{template.description}</CardDescription>
            </CardHeader>
            <CardContent>
              <div className="space-y-1 text-sm">
                <div className="flex justify-between">
                  <span className="text-muted-foreground">{t("test.templates.checkInterval")}</span>
                  <span className="font-mono">{template.period}</span>
                </div>
                <div className="flex justify-between">
                  <span className="text-muted-foreground">
                    {t("test.templates.fakeApiParams")}
                  </span>
                  <span className="font-mono text-xs">
                    {template.fakeParams}
                  </span>
                </div>
              </div>
              <p className="mt-2 text-xs text-muted-foreground">
                {template.behavior}
              </p>
            </CardContent>
            <CardFooter>
              <Button
                size="sm"
                className="w-full"
                onClick={() => handleCreate(template)}
                disabled={creatingSlug !== null}
              >
                {creatingSlug === template.slug ? (
                  <Loader2 className="mr-2 h-4 w-4 animate-spin" />
                ) : (
                  <Plus className="mr-2 h-4 w-4" />
                )}
                {t("test.templates.create")}
              </Button>
            </CardFooter>
          </Card>
        ))}
      </div>

      <Card>
        <CardHeader>
          <CardTitle className="text-base">{t("test.templates.customTitle")}</CardTitle>
          <CardDescription>{t("test.templates.customDescription")}</CardDescription>
        </CardHeader>
        <CardContent>
          <div className="grid grid-cols-2 md:grid-cols-4 gap-4">
            <div className="space-y-2">
              <Label htmlFor="custom-cycle">{t("test.templates.customCycle")}</Label>
              <Input
                id="custom-cycle"
                type="number"
                min="1"
                max="86400"
                value={customCycle}
                onChange={(e) => setCustomCycle(e.target.value)}
              />
              <p className="text-xs text-muted-foreground">
                {t("test.templates.customCycleHelp")}
              </p>
            </div>
            <div className="space-y-2">
              <Label htmlFor="custom-period">{t("test.templates.customPeriod")}</Label>
              <Input
                id="custom-period"
                type="number"
                min="5"
                max="3600"
                value={customPeriod}
                onChange={(e) => setCustomPeriod(e.target.value)}
              />
            </div>
            <div className="space-y-2">
              <Label htmlFor="custom-status">{t("test.templates.customStatus")}</Label>
              <Input
                id="custom-status"
                type="number"
                min="400"
                max="599"
                value={customStatusDown}
                onChange={(e) => setCustomStatusDown(e.target.value)}
              />
            </div>
            <div className="space-y-2">
              <Label htmlFor="custom-delay">{t("test.templates.customDelay")}</Label>
              <Input
                id="custom-delay"
                type="number"
                min="0"
                max="30000"
                value={customDelay}
                onChange={(e) => setCustomDelay(e.target.value)}
              />
            </div>
          </div>
        </CardContent>
        <CardFooter>
          <Button onClick={handleCreateCustom} disabled={creatingCustom}>
            {creatingCustom ? (
              <Loader2 className="mr-2 h-4 w-4 animate-spin" />
            ) : (
              <Plus className="mr-2 h-4 w-4" />
            )}
            {t("test.templates.createCustom")}
          </Button>
        </CardFooter>
      </Card>
    </div>
  );
}
