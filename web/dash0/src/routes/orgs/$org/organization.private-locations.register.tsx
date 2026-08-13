import { useEffect, useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import { createFileRoute, Link, useNavigate } from "@tanstack/react-router";
import { z } from "zod";
import {
  AlertTriangle,
  ArrowLeft,
  ArrowRight,
  Check,
  CheckCircle2,
  KeyRound,
  Loader2,
  Plus,
} from "lucide-react";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Badge } from "@/components/ui/badge";
import { Skeleton } from "@/components/ui/skeleton";
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { CollapsibleSection } from "@/components/ui/collapsible-section";
import { Stepper } from "@/components/ui/stepper";
import { CopyableCode } from "@/components/shared/copyable-code";
import { DocsLink } from "@/components/shared/docs-link";
import { slugify } from "@/lib/utils";
import {
  useAgents,
  useCreatePrivateRegion,
  useEnrollmentTokens,
  useMintEnrollmentToken,
  usePrivateRegions,
  type AgentInfo,
  type EnrollmentToken,
  type MintedEnrollmentToken,
  type PrivateRegion,
} from "@/api/hooks";

export const Route = createFileRoute(
  "/orgs/$org/organization/private-locations/register",
)({
  // Only the chosen location is a URL search param — it's the one piece of
  // wizard state worth deep-linking/bookmarking. The step index and the
  // minted secret stay in component state: a reload always loses the
  // one-shot secret anyway (it can never be redisplayed), so encoding "step"
  // in the URL would only produce a broken deep link, and the secret must
  // never be serialized into the URL/history regardless.
  validateSearch: z.object({
    regionSlug: z.string().optional(),
  }),
  component: RegisterAgentPage,
});

// The check-creation route's search schema has no optional/partial form on
// <Link>, so preselecting just `region` (per "target this location from a
// check") requires spelling out every other field as unset.
const NEW_CHECK_SEARCH_DEFAULTS = {
  checkType: undefined,
  checkPeriod: undefined,
  checkName: undefined,
  checkSlug: undefined,
  httpUrl: undefined,
  httpMethod: undefined,
  host: undefined,
  port: undefined,
  url: undefined,
  domain: undefined,
  username: undefined,
  database: undefined,
  expectedStatus: undefined,
  timeout: undefined,
  label: undefined,
  region: undefined,
  group: undefined,
  confirmationPeriod: undefined,
  recoveryPeriod: undefined,
  section: undefined,
};

const WIZARD_STEPS = [
  { label: "Pick location" },
  { label: "Mint token" },
  { label: "Run the agent" },
  { label: "Wait for connection" },
];

function RegisterAgentPage() {
  const { t } = useTranslation(["org"]);
  const { org } = Route.useParams();
  const { regionSlug } = Route.useSearch();
  const navigate = useNavigate();

  const { data: regions, isLoading: regionsLoading } = usePrivateRegions(org);
  const selectedRegion = useMemo(
    () => regions?.find((r) => r.slug === regionSlug) ?? null,
    [regions, regionSlug],
  );

  const [step, setStep] = useState<1 | 2 | 3 | 4>(regionSlug ? 2 : 1);
  const [minted, setMinted] = useState<MintedEnrollmentToken | null>(null);
  const [waitBaseline, setWaitBaseline] = useState<Set<string> | null>(null);

  // Derived, not stored: if we arrived (or landed after a mint reset) on
  // step >= 2 without a resolvable region — an invalid ?regionSlug=, or the
  // region was deleted meanwhile — render step 1 once regions have actually
  // loaded, without a redundant setState-in-effect round trip.
  const effectiveStep = step >= 2 && !regionsLoading && !selectedRegion ? 1 : step;

  // Polled continuously once a region is picked (not just on step 4) so the
  // "wait for connection" step's new-agent baseline — captured at the moment
  // the user leaves step 3 — reflects an already-loaded list rather than a
  // query that only starts fetching once step 4 mounts.
  const { data: agents } = useAgents(org, {
    enabled: !!selectedRegion,
    refetchInterval: step === 4 ? 4000 : 30_000,
  });
  const { data: tokens } = useEnrollmentTokens(org, {
    enabled: !!selectedRegion,
    refetchInterval: step === 4 ? 5000 : undefined,
  });

  const pickRegion = (slug: string) => {
    void navigate({
      to: ".",
      search: (prev) => ({ ...prev, regionSlug: slug }),
      replace: true,
    });
    setMinted(null);
    setStep(2);
  };

  const backToPickLocation = () => {
    setMinted(null);
    setStep(1);
  };

  return (
    <div className="mx-auto max-w-2xl space-y-6">
      <div className="flex items-start justify-between gap-3">
        <div className="min-w-0 flex-1">
          <h1 className="truncate text-2xl font-bold tracking-tight sm:text-3xl">
            {t("privateLocations.wizard.title", "Register an agent")}
          </h1>
          <p className="mt-1 text-muted-foreground">
            {t(
              "privateLocations.wizard.subtitle",
              "A guided walkthrough for enrolling a deported agent into one of your private locations.",
            )}
          </p>
        </div>
        <div className="flex shrink-0 gap-2">
          <Button asChild variant="ghost" size="icon" aria-label={t("privateLocations.wizard.back", "Back")}>
            <Link to="/orgs/$org/organization/private-locations" params={{ org }}>
              <ArrowLeft className="h-4 w-4" />
            </Link>
          </Button>
        </div>
      </div>

      <Stepper steps={WIZARD_STEPS} current={effectiveStep} />

      {effectiveStep === 1 && (
        <StepPickLocation
          org={org}
          regions={regions}
          regionsLoading={regionsLoading}
          onPicked={pickRegion}
        />
      )}

      {effectiveStep >= 2 && regionsLoading && <Skeleton className="h-32 w-full" />}

      {effectiveStep >= 2 && selectedRegion && (
        <>
          <LocationBanner region={selectedRegion} onChange={backToPickLocation} />

          {effectiveStep === 2 && (
            <StepMintToken
              org={org}
              region={selectedRegion}
              minted={minted}
              onMinted={setMinted}
              onContinue={() => setStep(3)}
            />
          )}

          {effectiveStep === 3 && minted && (
            <StepRunAgent
              minted={minted}
              agentsLoaded={agents !== undefined}
              onContinue={() => {
                setWaitBaseline(
                  new Set(
                    (agents ?? [])
                      .filter((a) => a.region === selectedRegion.region)
                      .map((a) => a.uid),
                  ),
                );
                setStep(4);
              }}
            />
          )}

          {effectiveStep === 4 && minted && (
            <StepWaitForAgent
              org={org}
              region={selectedRegion}
              minted={minted}
              agents={agents}
              tokens={tokens}
              baseline={waitBaseline}
              onRetry={() => {
                setMinted(null);
                setWaitBaseline(null);
                setStep(2);
              }}
            />
          )}
        </>
      )}
    </div>
  );
}

function LocationBanner({
  region,
  onChange,
}: {
  region: PrivateRegion;
  onChange: () => void;
}) {
  const { t } = useTranslation(["org"]);
  return (
    <div
      className="flex flex-wrap items-center justify-between gap-2 rounded-md border bg-muted/30 px-3 py-2 text-sm"
      data-testid="wizard-location-banner"
    >
      <span className="min-w-0 truncate">
        <span aria-hidden className="mr-1.5">
          {region.emoji}
        </span>
        <strong>{region.name}</strong>{" "}
        <code className="rounded bg-muted px-1 py-0.5 text-xs">{region.region}</code>
      </span>
      <Button variant="ghost" size="sm" onClick={onChange} data-testid="wizard-change-location">
        {t("privateLocations.wizard.changeLocation", "Change")}
      </Button>
    </div>
  );
}

// ---------------------------------------------------------------------------
// Step 1: pick or create the private location
// ---------------------------------------------------------------------------

function StepPickLocation({
  org,
  regions,
  regionsLoading,
  onPicked,
}: {
  org: string;
  regions: PrivateRegion[] | undefined;
  regionsLoading: boolean;
  onPicked: (slug: string) => void;
}) {
  const { t } = useTranslation(["org"]);
  const createRegion = useCreatePrivateRegion(org);

  const [name, setName] = useState("");
  const [slug, setSlug] = useState("");
  const [slugManuallyEdited, setSlugManuallyEdited] = useState(false);
  const [formError, setFormError] = useState<string | null>(null);

  const hasRegions = (regions?.length ?? 0) > 0;

  const onCreate = async () => {
    setFormError(null);
    try {
      const region = await createRegion.mutateAsync({
        slug: slug.trim(),
        name: name.trim() || undefined,
      });
      onPicked(region.slug);
    } catch (err) {
      setFormError(err instanceof Error ? err.message : String(err));
    }
  };

  return (
    <Card data-testid="wizard-step-pick-location">
      <CardHeader>
        <CardTitle>{t("privateLocations.wizard.step1.title", "1. Pick or create a location")}</CardTitle>
        <CardDescription>
          {t(
            "privateLocations.wizard.step1.description",
            "The agent enrolls into one org-private location. Pick an existing one or create a new one below.",
          )}
        </CardDescription>
      </CardHeader>
      <CardContent className="space-y-4">
        {regionsLoading ? (
          <Skeleton className="h-16 w-full" />
        ) : hasRegions ? (
          <div className="space-y-2">
            {regions?.map((region) => (
              <button
                key={region.slug}
                type="button"
                onClick={() => onPicked(region.slug)}
                data-testid={`wizard-region-${region.slug}`}
                className="flex w-full items-center justify-between gap-2 rounded-md border bg-card p-3 text-left transition-colors hover:bg-accent"
              >
                <span className="min-w-0 truncate">
                  <span aria-hidden className="mr-1.5">
                    {region.emoji}
                  </span>
                  <span className="font-medium">{region.name}</span>{" "}
                  <code className="rounded bg-muted px-1 py-0.5 text-xs">{region.region}</code>
                </span>
                <Badge variant={region.agentCount > 0 ? "default" : "secondary"} className="shrink-0">
                  {t("privateLocations.wizard.step1.agentCount", "{{count}} agents", {
                    count: region.agentCount,
                  })}
                </Badge>
              </button>
            ))}
          </div>
        ) : (
          <p className="text-sm text-muted-foreground" data-testid="wizard-no-regions">
            {t(
              "privateLocations.wizard.step1.empty",
              "No private locations yet — create one below to get started.",
            )}
          </p>
        )}

        <CollapsibleSection
          id="wizard-create-location"
          title={t("privateLocations.wizard.step1.createTitle", "Or create a new location")}
          defaultOpen={!hasRegions}
          data-testid="wizard-create-location-section"
        >
          <div className="space-y-4">
            <div className="space-y-2">
              <Label htmlFor="wizard-region-name">
                {t("privateLocations.regions.displayName", "Display name")}
              </Label>
              <Input
                id="wizard-region-name"
                data-testid="wizard-new-region-name"
                placeholder="Paris DC"
                value={name}
                onChange={(e) => {
                  setName(e.target.value);
                  if (!slugManuallyEdited) setSlug(slugify(e.target.value));
                }}
              />
            </div>
            <div className="space-y-2">
              <Label htmlFor="wizard-region-slug">{t("privateLocations.regions.slug", "Slug")}</Label>
              <Input
                id="wizard-region-slug"
                data-testid="wizard-new-region-slug"
                placeholder="dc1"
                value={slug}
                onChange={(e) => {
                  setSlug(e.target.value);
                  setSlugManuallyEdited(true);
                }}
              />
            </div>
            <div className="flex justify-end">
              <Button
                onClick={onCreate}
                disabled={!slug.trim() || createRegion.isPending}
                data-testid="wizard-create-region"
              >
                <Plus className="mr-2 h-4 w-4" />
                {t("privateLocations.wizard.step1.create", "Create & continue")}
              </Button>
            </div>
            {formError && (
              <p className="text-sm text-destructive" data-testid="wizard-region-error">
                {formError}
              </p>
            )}
          </div>
        </CollapsibleSection>
      </CardContent>
    </Card>
  );
}

// ---------------------------------------------------------------------------
// Step 2: mint the enrollment token
// ---------------------------------------------------------------------------

function StepMintToken({
  org,
  region,
  minted,
  onMinted,
  onContinue,
}: {
  org: string;
  region: PrivateRegion;
  minted: MintedEnrollmentToken | null;
  onMinted: (token: MintedEnrollmentToken) => void;
  onContinue: () => void;
}) {
  const { t } = useTranslation(["org"]);
  const mintToken = useMintEnrollmentToken(org);
  const [error, setError] = useState<string | null>(null);
  const [copied, setCopied] = useState(false);

  const onMint = async () => {
    setError(null);
    try {
      const token = await mintToken.mutateAsync({ regionSlug: region.slug });
      onMinted(token);
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    }
  };

  const onCopy = async () => {
    if (!minted) return;
    await navigator.clipboard.writeText(minted.token);
    setCopied(true);
    window.setTimeout(() => setCopied(false), 1500);
  };

  return (
    <Card data-testid="wizard-step-mint-token">
      <CardHeader>
        <CardTitle>{t("privateLocations.wizard.step2.title", "2. Mint an enrollment token")}</CardTitle>
        <CardDescription>
          {t(
            "privateLocations.wizard.step2.description",
            "A single-use spe_ token that lets exactly one agent enroll into this location.",
          )}
        </CardDescription>
      </CardHeader>
      <CardContent className="space-y-4">
        {!minted ? (
          <>
            <p className="text-sm text-muted-foreground">
              {t(
                "privateLocations.wizard.step2.explain",
                "The token is shown exactly once — the server stores only its hash — and expires after 24 hours by default.",
              )}
            </p>
            <Button onClick={() => void onMint()} disabled={mintToken.isPending} data-testid="wizard-mint-token">
              <KeyRound className="mr-2 h-4 w-4" />
              {t("privateLocations.wizard.step2.mint", "Mint enrollment token")}
            </Button>
            {error && (
              <p className="text-sm text-destructive" data-testid="wizard-mint-error">
                {error}
              </p>
            )}
          </>
        ) : (
          <>
            <Alert variant="warning">
              <AlertTriangle />
              <AlertTitle>
                {t("privateLocations.wizard.step2.onceTitle", "Copy it now — you won't see this again")}
              </AlertTitle>
              <AlertDescription>
                {t(
                  "privateLocations.wizard.step2.onceDescription",
                  "This token expires {{expiry}} and enrolls exactly one agent. If you lose it, mint a new one.",
                  { expiry: formatExpiry(minted.expiresAt) },
                )}
              </AlertDescription>
            </Alert>
            <div className="flex items-center gap-2">
              <code
                className="flex-1 overflow-x-auto rounded bg-muted px-2 py-1.5 text-xs"
                data-testid="wizard-minted-token"
              >
                {minted.token}
              </code>
              <Button variant="outline" size="icon" onClick={() => void onCopy()} data-testid="wizard-copy-token">
                {copied ? <Check className="h-4 w-4" /> : <KeyRound className="h-4 w-4" />}
              </Button>
            </div>
            <div className="flex justify-end">
              <Button onClick={onContinue} data-testid="wizard-continue-to-run">
                {t("privateLocations.wizard.step2.continue", "Continue")}
                <ArrowRight className="ml-2 h-4 w-4" />
              </Button>
            </div>
          </>
        )}
      </CardContent>
    </Card>
  );
}

function formatExpiry(expiresAt: string): string {
  const date = new Date(expiresAt);
  const hours = Math.max(1, Math.round((date.getTime() - Date.now()) / (60 * 60 * 1000)));
  return `in ~${hours}h (${date.toLocaleString()})`;
}

// Plain helper (not a component/hook) so the current-time read doesn't trip
// the render-purity lint rule the way it would inside a component body.
function isTokenExpired(expiresAt: string): boolean {
  return Date.now() > new Date(expiresAt).getTime();
}

// ---------------------------------------------------------------------------
// Step 3: run the agent
// ---------------------------------------------------------------------------

function StepRunAgent({
  minted,
  agentsLoaded,
  onContinue,
}: {
  minted: MintedEnrollmentToken;
  agentsLoaded: boolean;
  onContinue: () => void;
}) {
  const { t } = useTranslation(["org"]);
  const [tab, setTab] = useState("docker-run");
  const serverUrl = window.location.origin;

  const dockerRun = `docker run -d --name solidping-agent \\
  -v agent-data:/data \\
  -e SP_NODE_ROLE=agent \\
  -e SP_AGENT_SERVER_URL=${serverUrl} \\
  -e SP_AGENT_ENROLLMENT_TOKEN=${minted.token} \\
  ghcr.io/fclairamb/solidping:latest`;

  const dockerCompose = `services:
  solidping-agent:
    image: ghcr.io/fclairamb/solidping:latest
    restart: unless-stopped
    volumes:
      - agent-data:/data
    environment:
      SP_NODE_ROLE: agent
      SP_AGENT_SERVER_URL: ${serverUrl}
      SP_AGENT_ENROLLMENT_TOKEN: ${minted.token}

volumes:
  agent-data:`;

  const kubernetes = `apiVersion: apps/v1
kind: Deployment
metadata:
  name: solidping-agent
spec:
  replicas: 1
  selector:
    matchLabels: { app: solidping-agent }
  template:
    metadata:
      labels: { app: solidping-agent }
    spec:
      containers:
        - name: agent
          image: ghcr.io/fclairamb/solidping:latest
          env:
            - name: SP_NODE_ROLE
              value: agent
            - name: SP_AGENT_SERVER_URL
              value: ${serverUrl}
            - name: SP_AGENT_ENROLLMENT_TOKEN
              value: ${minted.token}
          volumeMounts:
            - name: agent-data
              mountPath: /data
      volumes:
        - name: agent-data
          persistentVolumeClaim:
            claimName: solidping-agent-data
---
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: solidping-agent-data
spec:
  accessModes: ["ReadWriteOnce"]
  resources:
    requests:
      storage: 1Gi`;

  return (
    <Card data-testid="wizard-step-run-agent">
      <CardHeader className="flex flex-row items-start justify-between gap-3 space-y-0">
        <div>
          <CardTitle>{t("privateLocations.wizard.step3.title", "3. Run the agent")}</CardTitle>
          <CardDescription>
            {t(
              "privateLocations.wizard.step3.description",
              "The agent is the standard SolidPing container in agent mode — no separate binary, no database.",
            )}
          </CardDescription>
        </div>
        <DocsLink href="/docs/features/private-locations" />
      </CardHeader>
      <CardContent className="space-y-4">
        <Alert variant="warning">
          <AlertTriangle />
          <AlertTitle>{t("privateLocations.wizard.step3.gotchasTitle", "Two things to know")}</AlertTitle>
          <AlertDescription>
            <p>
              {t(
                "privateLocations.wizard.step3.gotcha1",
                "The token is single-use: it enrolls exactly one agent, then it's consumed.",
              )}
            </p>
            <p>
              {t(
                "privateLocations.wizard.step3.gotcha2",
                "The agent persists its identity to /data/agent-keys.json (SP_AGENT_KEYS_FILE) — that volume must survive restarts, or the agent will enroll again with a new identity.",
              )}
            </p>
          </AlertDescription>
        </Alert>

        <div className="overflow-x-auto">
          <Tabs value={tab} onValueChange={setTab}>
            <TabsList>
              <TabsTrigger value="docker-run" data-testid="wizard-tab-docker-run">
                docker run
              </TabsTrigger>
              <TabsTrigger value="docker-compose" data-testid="wizard-tab-docker-compose">
                docker compose
              </TabsTrigger>
              <TabsTrigger value="kubernetes" data-testid="wizard-tab-kubernetes">
                Kubernetes
              </TabsTrigger>
            </TabsList>
            <TabsContent value="docker-run">
              <CopyableCode code={dockerRun} data-testid="wizard-snippet-docker-run" />
            </TabsContent>
            <TabsContent value="docker-compose">
              <CopyableCode code={dockerCompose} data-testid="wizard-snippet-docker-compose" />
            </TabsContent>
            <TabsContent value="kubernetes">
              <p className="mb-2 text-xs text-muted-foreground">
                {t(
                  "privateLocations.wizard.step3.k8sNote",
                  "This first run persists the identity on a PVC, exactly like the docker run volume. For a no-persistent-volume setup, read the value out of the file the agent wrote (kubectl exec deploy/solidping-agent -- base64 -w0 /data/agent-keys.json) and store it as SP_AGENT_KEYS in a Secret. The agent never prints its private keys to the logs — see the docs link at the top of this card.",
                )}
              </p>
              <CopyableCode code={kubernetes} data-testid="wizard-snippet-kubernetes" />
            </TabsContent>
          </Tabs>
        </div>

        <div className="flex justify-end">
          <Button
            onClick={onContinue}
            disabled={!agentsLoaded}
            data-testid="wizard-continue-to-wait"
          >
            {t("privateLocations.wizard.step3.continue", "Continue — I started the agent")}
            <ArrowRight className="ml-2 h-4 w-4" />
          </Button>
        </div>
      </CardContent>
    </Card>
  );
}

// ---------------------------------------------------------------------------
// Step 4: wait for the agent to connect
// ---------------------------------------------------------------------------

function StepWaitForAgent({
  org,
  region,
  minted,
  agents,
  tokens,
  baseline,
  onRetry,
}: {
  org: string;
  region: PrivateRegion;
  minted: MintedEnrollmentToken;
  agents: AgentInfo[] | undefined;
  tokens: EnrollmentToken[] | undefined;
  /** Agent uids already enrolled in this region, snapshotted when step 3 was
   * left — so only a genuinely NEW agent flips this to success. */
  baseline: Set<string> | null;
  onRetry: () => void;
}) {
  const { t } = useTranslation(["org"]);

  const expired = isTokenExpired(minted.expiresAt);

  // Grace period before we treat "token no longer in the live list" as
  // consumed-elsewhere/cancelled — right after minting, the tokens query can
  // still be serving a pre-mint cached response for a moment.
  const [graceOver, setGraceOver] = useState(false);
  useEffect(() => {
    const id = window.setTimeout(() => setGraceOver(true), 6000);
    return () => window.clearTimeout(id);
  }, []);

  const newAgent: AgentInfo | null = useMemo(() => {
    if (!agents || !baseline) return null;
    return agents.find((a) => a.region === region.region && !baseline.has(a.uid)) ?? null;
  }, [agents, baseline, region.region]);

  // Primary success signal: used tokens stay listed for a viewing window and
  // name the agent that consumed them. This is race-free — it works even when
  // the agent enrolled before the user left step 3 (so it's already inside
  // the baseline and the "new agent" fallback below can't see it).
  const myToken = tokens?.find((tk) => tk.uid === minted.uid) ?? null;
  const usedByAgent: AgentInfo | null =
    (myToken?.status === "used" &&
      agents?.find((a) => a.uid === myToken.usedByAgentUid)) ||
    null;

  const connectedAgent = usedByAgent ?? newAgent;

  // The token vanishing from the list now genuinely means an admin cancelled
  // it — a used token keeps reporting its outcome instead of disappearing.
  const cancelledElsewhere =
    graceOver && tokens !== undefined && !myToken && !connectedAgent;

  if (connectedAgent) {
    return (
      <Card data-testid="wizard-step-success">
        <CardHeader>
          <CardTitle className="flex items-center gap-2 text-green-700 dark:text-green-400">
            <CheckCircle2 className="h-5 w-5" />
            {t("privateLocations.wizard.step4.successTitle", "Agent connected!")}
          </CardTitle>
          <CardDescription>
            {t("privateLocations.wizard.step4.successDescription", "{{name}} is now enrolled in {{region}}.", {
              name: connectedAgent.name,
              region: region.region,
            })}
          </CardDescription>
        </CardHeader>
        <CardContent>
          <Button asChild data-testid="wizard-create-check">
            <Link
              to="/orgs/$org/checks/new"
              params={{ org }}
              search={{ ...NEW_CHECK_SEARCH_DEFAULTS, region: [region.region] }}
            >
              {t("privateLocations.wizard.step4.nextStep", "Target this location from a check")}
              <ArrowRight className="ml-2 h-4 w-4" />
            </Link>
          </Button>
        </CardContent>
      </Card>
    );
  }

  if (expired || cancelledElsewhere) {
    return (
      <Card data-testid="wizard-step-error">
        <CardHeader>
          <CardTitle>{t("privateLocations.wizard.step4.errorTitle", "The agent hasn't connected")}</CardTitle>
        </CardHeader>
        <CardContent className="space-y-4">
          <Alert variant="destructive">
            <AlertTriangle />
            <AlertTitle>
              {expired
                ? t("privateLocations.wizard.step4.expiredTitle", "Token expired")
                : t("privateLocations.wizard.step4.consumedTitle", "Token no longer available")}
            </AlertTitle>
            <AlertDescription>
              {expired
                ? t(
                    "privateLocations.wizard.step4.expiredDescription",
                    "This enrollment token expired before an agent used it.",
                  )
                : t(
                    "privateLocations.wizard.step4.consumedDescription",
                    "This token was already used or was cancelled elsewhere.",
                  )}
            </AlertDescription>
          </Alert>
          <Button onClick={onRetry} data-testid="wizard-retry-mint">
            <KeyRound className="mr-2 h-4 w-4" />
            {t("privateLocations.wizard.step4.retry", "Mint a new token")}
          </Button>
        </CardContent>
      </Card>
    );
  }

  return (
    <Card data-testid="wizard-step-waiting">
      <CardHeader>
        <CardTitle className="flex items-center gap-2">
          <Loader2 className="h-4 w-4 animate-spin" />
          {t("privateLocations.wizard.step4.waitingTitle", "4. Waiting for the agent to connect")}
        </CardTitle>
        <CardDescription>
          {t(
            "privateLocations.wizard.step4.waitingDescription",
            "This updates automatically once the agent enrolls — no need to refresh.",
          )}
        </CardDescription>
      </CardHeader>
    </Card>
  );
}
