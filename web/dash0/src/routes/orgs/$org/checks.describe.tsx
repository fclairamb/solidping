import { useState } from "react";
import { createFileRoute, Link, useNavigate } from "@tanstack/react-router";
import { useTranslation } from "react-i18next";
import { AlertCircle, Loader2, Sparkles } from "lucide-react";
import { toast } from "sonner";
import {
  useAIContract,
  useAIGenerate,
  useCreateCheck,
  type AIGenerateResponse,
  type AIRepairMode,
} from "@/api/hooks";
import { ApiError } from "@/api/client";
import { useAIChecksEnabled, usePublicConfigLoading } from "@/api/public-config";
import { PageHeader } from "@/components/shared/page-header";
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { KeyValueRows, type KeyValueRow } from "@/components/ui/key-value-rows";
import { Label } from "@/components/ui/label";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { Textarea } from "@/components/ui/textarea";

// "Describe it" (spec 2026-10-03-07): prompt -> confirm the contract -> watch
// the generation -> save. The script is written and tested once by the
// server's AI; the saved check is a plain js check. Hidden when the server has
// no AI provider.

export const Route = createFileRoute("/orgs/$org/checks/describe")({
  component: DescribeCheckPage,
});

const REPAIR_MODES: AIRepairMode[] = ["propose", "auto", "off"];

function rowsToMap(rows: KeyValueRow[]): Record<string, string> {
  const out: Record<string, string> = {};
  for (const row of rows) {
    const key = row.key.trim();
    if (key) out[key] = row.value;
  }
  return out;
}

function errorMessage(err: unknown): string {
  if (err instanceof ApiError) {
    return err.detail ? `${err.message}: ${err.detail}` : err.message;
  }
  return err instanceof Error ? err.message : String(err);
}

function DescribeCheckPage() {
  const { t } = useTranslation("checks");
  const navigate = useNavigate();
  const { org } = Route.useParams();
  const enabled = useAIChecksEnabled();
  const loading = usePublicConfigLoading();

  const contractMutation = useAIContract(org);
  const generateMutation = useAIGenerate(org);
  const createCheck = useCreateCheck(org);

  const [prompt, setPrompt] = useState("");
  const [envRows, setEnvRows] = useState<KeyValueRow[]>([]);
  const [secretRows, setSecretRows] = useState<KeyValueRow[]>([]);
  const [repair, setRepair] = useState<AIRepairMode>("propose");
  const [contract, setContract] = useState<string | null>(null);
  const [generated, setGenerated] = useState<AIGenerateResponse | null>(null);
  const [name, setName] = useState("");
  const [error, setError] = useState<string | null>(null);

  if (!loading && !enabled) {
    return (
      <Alert data-testid="ai-describe-disabled">
        <AlertCircle className="h-4 w-4" />
        <AlertTitle>{t("ai.disabled")}</AlertTitle>
      </Alert>
    );
  }

  const contractLines = (contract ?? "")
    .split("\n")
    .map((line) => line.trim())
    .filter(Boolean);

  async function proposeContract() {
    setError(null);
    setGenerated(null);
    try {
      const resp = await contractMutation.mutateAsync(prompt);
      setContract(resp.contract.join("\n"));
    } catch (err) {
      setError(errorMessage(err));
    }
  }

  async function generate() {
    setError(null);
    setGenerated(null);
    try {
      const resp = await generateMutation.mutateAsync({
        prompt,
        contract: contractLines,
        env: rowsToMap(envRows),
        secrets: rowsToMap(secretRows),
        repair,
      });
      setGenerated(resp);
      if (!name) setName(prompt.slice(0, 60));
    } catch (err) {
      setError(errorMessage(err));
    }
  }

  async function save() {
    if (!generated) return;
    setError(null);
    const secrets = rowsToMap(secretRows);
    try {
      const check = await createCheck.mutateAsync({
        name: name.trim() || t("ai.defaultName"),
        type: "js",
        config: {
          ...generated.config,
          ...(Object.keys(secrets).length > 0 && { secrets }),
        },
      });
      toast.success(t("ai.saved"));
      navigate({
        to: "/orgs/$org/checks/$checkUid",
        params: { org, checkUid: check.uid },
        search: { graphPeriod: undefined, graphFull: undefined, region: undefined },
      });
    } catch (err) {
      setError(errorMessage(err));
    }
  }

  const generating = generateMutation.isPending;

  return (
    <div className="space-y-6">
      <PageHeader icon={Sparkles} title={t("ai.title")} description={t("ai.subtitle")} />

      {error && (
        <Alert variant="destructive" data-testid="ai-describe-error">
          <AlertCircle className="h-4 w-4" />
          <AlertTitle>{t("ai.errorTitle")}</AlertTitle>
          <AlertDescription className="break-words">{error}</AlertDescription>
        </Alert>
      )}

      <Card>
        <CardHeader>
          <CardTitle>{t("ai.describeStep")}</CardTitle>
          <CardDescription>{t("ai.describeHelp")}</CardDescription>
        </CardHeader>
        <CardContent className="space-y-4">
          <div className="space-y-2">
            <Label htmlFor="ai-prompt">{t("ai.promptLabel")}</Label>
            <Textarea
              id="ai-prompt"
              data-testid="ai-prompt-input"
              rows={4}
              value={prompt}
              placeholder={t("ai.promptPlaceholder")}
              onChange={(e) => setPrompt(e.target.value)}
            />
          </div>
          <div className="space-y-2">
            <Label>{t("ai.envLabel")}</Label>
            <p className="text-sm text-muted-foreground">{t("ai.envHelp")}</p>
            <KeyValueRows
              rows={envRows}
              onChange={setEnvRows}
              addLabel={t("ai.addEnv")}
              keyPlaceholder="BASE_URL"
              valuePlaceholder="https://app.acme.com"
              testIdPrefix="ai-env"
            />
          </div>
          <div className="space-y-2">
            <Label>{t("ai.secretsLabel")}</Label>
            <p className="text-sm text-muted-foreground">{t("ai.secretsHelp")}</p>
            <KeyValueRows
              rows={secretRows}
              onChange={setSecretRows}
              addLabel={t("ai.addSecret")}
              keyPlaceholder="PASSWORD"
              secretValues
              testIdPrefix="ai-secret"
            />
          </div>
          <div className="space-y-2">
            <Label htmlFor="ai-repair">{t("ai.repairLabel")}</Label>
            <Select value={repair} onValueChange={(value) => setRepair(value as AIRepairMode)}>
              <SelectTrigger id="ai-repair" className="w-full sm:w-72" data-testid="ai-repair-select">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {REPAIR_MODES.map((mode) => (
                  <SelectItem key={mode} value={mode}>
                    {t(`ai.repair.${mode}`)}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </div>
          <div className="flex flex-wrap gap-2">
            <Button
              data-testid="ai-propose-contract"
              disabled={!prompt.trim() || contractMutation.isPending}
              onClick={proposeContract}
            >
              {contractMutation.isPending && <Loader2 className="mr-2 h-4 w-4 animate-spin" />}
              {t("ai.proposeContract")}
            </Button>
            <Button variant="outline" asChild>
              <Link to="/orgs/$org/checks" params={{ org }}>
                {t("ai.cancel")}
              </Link>
            </Button>
          </div>
        </CardContent>
      </Card>

      {contract !== null && (
        <Card data-testid="ai-contract-card">
          <CardHeader>
            <CardTitle>{t("ai.contractTitle")}</CardTitle>
            <CardDescription>{t("ai.contractHelp")}</CardDescription>
          </CardHeader>
          <CardContent className="space-y-4">
            <Textarea
              data-testid="ai-contract-input"
              rows={Math.max(3, contractLines.length + 1)}
              value={contract}
              onChange={(e) => setContract(e.target.value)}
            />
            <Button
              data-testid="ai-generate"
              disabled={contractLines.length === 0 || generating}
              onClick={generate}
            >
              {generating && <Loader2 className="mr-2 h-4 w-4 animate-spin" />}
              {generating ? t("ai.generating") : t("ai.generate")}
            </Button>
          </CardContent>
        </Card>
      )}

      {generated && (
        <Card data-testid="ai-generated-card">
          <CardHeader>
            <CardTitle className="flex flex-wrap items-center gap-2">
              {t("ai.scriptTitle")}
              <Badge variant={generated.lastRun.status === "up" ? "success" : "destructive"}>
                {t("ai.lastRun", { status: generated.lastRun.status })}
              </Badge>
            </CardTitle>
            <CardDescription>
              {t("ai.writtenBy", { model: generated.model, turns: generated.turns })}
            </CardDescription>
          </CardHeader>
          <CardContent className="space-y-4">
            <pre
              data-testid="ai-generated-script"
              className="max-h-96 overflow-auto rounded-md bg-muted p-3 text-xs"
            >
              {generated.script}
            </pre>
            {generated.lastRun.output && (
              <pre
                data-testid="ai-generated-output"
                className="max-h-48 overflow-auto rounded-md bg-muted p-3 text-xs"
              >
                {JSON.stringify(generated.lastRun.output, null, 2)}
              </pre>
            )}
            <div className="space-y-2">
              <Label htmlFor="ai-name">{t("ai.nameLabel")}</Label>
              <Input
                id="ai-name"
                data-testid="ai-name-input"
                value={name}
                onChange={(e) => setName(e.target.value)}
              />
            </div>
            <Button data-testid="ai-save" disabled={createCheck.isPending} onClick={save}>
              {t("ai.save")}
            </Button>
          </CardContent>
        </Card>
      )}
    </div>
  );
}
