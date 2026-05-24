import { useState } from "react";
import { useTranslation } from "react-i18next";
import { createFileRoute, useNavigate, Link } from "@tanstack/react-router";
import { ArrowLeft, Loader2 } from "lucide-react";
import { toast } from "sonner";

import { Button } from "@/components/ui/button";
import { Label } from "@/components/ui/label";
import { Input } from "@/components/ui/input";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import {
  useListCandidateHosts,
  usePromoteCandidate,
  type DiscoveredHost,
  type SuggestedCheck,
} from "@/api/hooks";

export const Route = createFileRoute(
  "/orgs/$org/discovery/$jobUid/$hostUid/promote",
)({
  component: PromoteHostPage,
});

const CHECK_TYPES = ["http", "tcp", "ping", "ssl", "dns"] as const;

function getDefaultConfig(host: DiscoveredHost): { checkType: string; name: string; slug: string } {
  const first: SuggestedCheck | undefined = host.suggestedChecks?.[0];
  const checkType = first?.type ?? "tcp";
  const name = host.hostname || host.ip;
  const slug = (host.hostname || host.ip)
    .toLowerCase()
    .replace(/[^a-z0-9-]/g, "-")
    .replace(/-+/g, "-")
    .replace(/^-|-$/g, "")
    .slice(0, 48);

  return { checkType, name, slug };
}

function PromoteHostPage() {
  const { t } = useTranslation("discovery");
  const { org, jobUid, hostUid } = Route.useParams();
  const navigate = useNavigate();
  const promote = usePromoteCandidate(org);

  // Load the host list and find the matching host.
  const { data: hosts } = useListCandidateHosts(org, { jobUid });
  const host = hosts?.find((h) => h.uid === hostUid);

  const defaults = host ? getDefaultConfig(host) : { checkType: "tcp", name: "", slug: "" };
  const [checkType, setCheckType] = useState(defaults.checkType);
  const [name, setName] = useState(defaults.name);
  const [slug, setSlug] = useState(defaults.slug);
  const [period, setPeriod] = useState("1m");

  // Update defaults once host loads.
  const [initialized, setInitialized] = useState(false);
  if (host && !initialized) {
    const d = getDefaultConfig(host);
    setCheckType(d.checkType);
    setName(d.name);
    setSlug(d.slug);
    setInitialized(true);
  }

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault();

    try {
      await promote.mutateAsync({
        uid: hostUid,
        req: {
          checkType,
          name,
          slug,
          period,
        },
      });
      toast.success(t("promoted_success"));
      navigate({
        to: "/orgs/$org/discovery/$jobUid",
        params: { org, jobUid },
      });
    } catch {
      toast.error(t("promoted_failed"));
    }
  };

  return (
    <Card className="max-w-xl">
      <CardHeader>
        <div className="flex items-center gap-3">
          <Button asChild variant="ghost" size="icon">
            <Link
              to="/orgs/$org/discovery/$jobUid"
              params={{ org, jobUid }}
            >
              <ArrowLeft className="h-4 w-4" />
            </Link>
          </Button>
          <div>
            <CardTitle>{t("promoteTitle")}</CardTitle>
            {host && (
              <CardDescription className="font-mono text-xs">
                {host.ip}{host.hostname ? ` (${host.hostname})` : ""}
              </CardDescription>
            )}
          </div>
        </div>
      </CardHeader>
      <CardContent>
        <form onSubmit={handleSubmit} className="space-y-4">
          <div className="space-y-1.5">
            <Label htmlFor="checkType">{t("checkType")}</Label>
            <Select value={checkType} onValueChange={setCheckType}>
              <SelectTrigger id="checkType">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {CHECK_TYPES.map((type) => (
                  <SelectItem key={type} value={type}>
                    {type}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </div>

          <div className="space-y-1.5">
            <Label htmlFor="name">{t("name")}</Label>
            <Input
              id="name"
              value={name}
              onChange={(e) => setName(e.target.value)}
              required
            />
          </div>

          <div className="space-y-1.5">
            <Label htmlFor="slug">{t("slug")}</Label>
            <Input
              id="slug"
              value={slug}
              onChange={(e) => setSlug(e.target.value)}
            />
          </div>

          <div className="space-y-1.5">
            <Label htmlFor="period">{t("period")}</Label>
            <Input
              id="period"
              value={period}
              onChange={(e) => setPeriod(e.target.value)}
              placeholder="1m"
            />
          </div>

          <div className="flex justify-end gap-2">
            <Button asChild type="button" variant="outline">
              <Link to="/orgs/$org/discovery/$jobUid" params={{ org, jobUid }}>
                {t("cancel")}
              </Link>
            </Button>
            <Button type="submit" disabled={promote.isPending || !name}>
              {promote.isPending ? (
                <>
                  <Loader2 className="h-4 w-4 mr-1 animate-spin" />
                  {t("promote")}…
                </>
              ) : (
                t("promote")
              )}
            </Button>
          </div>
        </form>
      </CardContent>
    </Card>
  );
}
