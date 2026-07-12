import { createFileRoute, useNavigate } from "@tanstack/react-router";
import { useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import { ArrowLeft, Plus, Trash2 } from "lucide-react";

import {
  type EscalationPolicyStep,
  type EscalationPolicyTarget,
  useDeleteEscalationPolicy,
  useEscalationPolicy,
  useUpdateEscalationPolicy,
} from "@/api/hooks";
import { StepTargetRow } from "@/components/escalation/step-target-row";
import { Button } from "@/components/ui/button";
import {
  Card,
  CardContent,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Skeleton } from "@/components/ui/skeleton";
import { Textarea } from "@/components/ui/textarea";

export const Route = createFileRoute("/orgs/$org/escalation-policies/$uid")({
  component: EscalationPolicyDetailPage,
});

function EscalationPolicyDetailPage() {
  const { t } = useTranslation(["escalation", "common"]);
  const { org, uid } = Route.useParams();
  const navigate = useNavigate();
  const { data: policy, isLoading } = useEscalationPolicy(org, uid);
  const update = useUpdateEscalationPolicy(org, uid);
  const del = useDeleteEscalationPolicy(org);

  const [name, setName] = useState("");
  const [description, setDescription] = useState("");
  const [repeatMax, setRepeatMax] = useState(0);
  const [repeatAfterSeconds, setRepeatAfterSeconds] = useState<number>(30);
  const [steps, setSteps] = useState<EscalationPolicyStep[]>([]);

  useEffect(() => {
    if (!policy) return;
    setName(policy.name);
    setDescription(policy.description || "");
    setRepeatMax(policy.repeatMax);
    setRepeatAfterSeconds(policy.repeatAfterSeconds || 30);
    setSteps(policy.steps || []);
  }, [policy]);

  if (isLoading || !policy) {
    return <Skeleton className="h-64 w-full" />;
  }

  const addStep = () =>
    setSteps((prev) => [
      ...prev,
      {
        position: prev.length,
        delaySeconds: 5,
        targets: [{ type: "all_admins" }],
      },
    ]);

  const removeStep = (idx: number) =>
    setSteps((prev) =>
      prev.filter((_, i) => i !== idx).map((s, i) => ({ ...s, position: i })),
    );

  const submit = async () => {
    await update.mutateAsync({
      name,
      description: description || undefined,
      repeatMax,
      repeatAfterSeconds: repeatMax > 0 ? repeatAfterSeconds : null,
      steps: steps.map((s, i) => ({
        position: i,
        delaySeconds: s.delaySeconds,
        targets: s.targets.map((tar, ti) => ({
          ...tar,
          position: ti,
        })),
      })),
    });
  };

  const handleDelete = async () => {
    if (!confirm(t("escalation:editor.deleteConfirm"))) return;
    try {
      await del.mutateAsync(uid);
      navigate({ to: "/orgs/$org/escalation-policies", params: { org } });
    } catch (err) {
      const msg = err instanceof Error ? err.message : String(err);
      alert(msg);
    }
  };

  return (
    <div className="space-y-6 max-w-3xl">
      <div className="flex items-center gap-3">
        <Button asChild variant="ghost" size="icon" aria-label={t("common:back")}>
          <a href={`/dash0/orgs/${org}/escalation-policies`}>
            <ArrowLeft className="h-4 w-4" />
          </a>
        </Button>
        <h1 className="text-3xl font-bold tracking-tight flex-1">{policy.name}</h1>
        <Button variant="destructive" size="sm" onClick={handleDelete}>
          <Trash2 className="h-4 w-4 mr-1" />
          {t("common:delete")}
        </Button>
      </div>

      <Card>
        <CardHeader>
          <CardTitle>{t("escalation:editor.basics")}</CardTitle>
        </CardHeader>
        <CardContent className="space-y-4">
          <div>
            <Label>{t("escalation:editor.name")}</Label>
            <Input value={name} onChange={(e) => setName(e.target.value)} />
          </div>
          <div>
            <Label>{t("escalation:editor.description")}</Label>
            <Textarea
              value={description}
              onChange={(e) => setDescription(e.target.value)}
              rows={2}
            />
          </div>
        </CardContent>
      </Card>

      <Card>
        <CardHeader>
          <CardTitle>{t("escalation:editor.steps")}</CardTitle>
        </CardHeader>
        <CardContent className="space-y-3">
          {steps.map((step, idx) => (
            <div
              key={idx}
              className="border rounded p-3 space-y-2 bg-muted/30"
            >
              <div className="flex items-center gap-3">
                <span className="font-medium text-sm">
                  {t("escalation:editor.step")} {idx + 1}
                </span>
                <Label className="text-xs">
                  {t("escalation:editor.delaySeconds")}:
                </Label>
                <Input
                  type="number"
                  min={0}
                  className="w-20 h-8"
                  value={step.delaySeconds}
                  onChange={(e) =>
                    setSteps((prev) =>
                      prev.map((s, i) =>
                        i === idx
                          ? {
                              ...s,
                              delaySeconds: parseInt(e.target.value, 10) || 0,
                            }
                          : s,
                      ),
                    )
                  }
                />
                <div className="flex-1" />
                {steps.length > 1 && (
                  <Button
                    type="button"
                    variant="ghost"
                    size="sm"
                    onClick={() => removeStep(idx)}
                  >
                    <Trash2 className="h-4 w-4" />
                  </Button>
                )}
              </div>
              <div className="space-y-1">
                {step.targets.map((tar, ti) => (
                  <StepTargetRow
                    key={ti}
                    org={org}
                    target={tar}
                    onChange={(next: EscalationPolicyTarget) =>
                      setSteps((prev) =>
                        prev.map((s, i) =>
                          i === idx
                            ? {
                                ...s,
                                targets: s.targets.map((t2, j) =>
                                  j === ti ? { ...t2, ...next } : t2,
                                ),
                              }
                            : s,
                        ),
                      )
                    }
                  />
                ))}
              </div>
            </div>
          ))}
          <Button type="button" variant="outline" size="sm" onClick={addStep}>
            <Plus className="h-4 w-4 mr-1" />
            {t("escalation:editor.addStep")}
          </Button>
        </CardContent>
      </Card>

      <Card>
        <CardHeader>
          <CardTitle>{t("escalation:editor.repeat")}</CardTitle>
        </CardHeader>
        <CardContent className="grid grid-cols-2 gap-4">
          <div>
            <Label>{t("escalation:editor.repeatMax")}</Label>
            <Input
              type="number"
              min={0}
              value={repeatMax}
              onChange={(e) =>
                setRepeatMax(parseInt(e.target.value, 10) || 0)
              }
            />
          </div>
          {repeatMax > 0 && (
            <div>
              <Label>{t("escalation:editor.repeatAfterSeconds")}</Label>
              <Input
                type="number"
                min={1}
                value={repeatAfterSeconds}
                onChange={(e) =>
                  setRepeatAfterSeconds(parseInt(e.target.value, 10) || 1)
                }
              />
            </div>
          )}
        </CardContent>
      </Card>

      <div className="flex justify-end gap-2">
        <Button onClick={submit} disabled={update.isPending}>
          {update.isPending ? t("common:saving") : t("common:save")}
        </Button>
      </div>
    </div>
  );
}
