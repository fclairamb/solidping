import { createFileRoute, useNavigate } from "@tanstack/react-router";
import { useState } from "react";
import { useTranslation } from "react-i18next";
import { ArrowLeft, Plus, Trash2 } from "lucide-react";

import {
  type EscalationPolicyStep,
  type EscalationTargetType,
  useCreateEscalationPolicy,
} from "@/api/hooks";
import { Button } from "@/components/ui/button";
import {
  Card,
  CardContent,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Textarea } from "@/components/ui/textarea";

export const Route = createFileRoute("/orgs/$org/escalation-policies/new")({
  component: NewEscalationPolicyPage,
});

function NewEscalationPolicyPage() {
  const { t } = useTranslation(["escalation", "common"]);
  const { org } = Route.useParams();
  const navigate = useNavigate();
  const create = useCreateEscalationPolicy(org);

  const [slug, setSlug] = useState("");
  const [name, setName] = useState("");
  const [description, setDescription] = useState("");
  const [repeatMax, setRepeatMax] = useState(0);
  const [repeatAfterMinutes, setRepeatAfterMinutes] = useState<number>(30);
  const [steps, setSteps] = useState<EscalationPolicyStep[]>([
    {
      position: 0,
      delayMinutes: 0,
      targets: [{ type: "all_admins" }],
    },
  ]);

  const addStep = () => {
    setSteps((prev) => [
      ...prev,
      {
        position: prev.length,
        delayMinutes: 5,
        targets: [{ type: "all_admins" }],
      },
    ]);
  };

  const removeStep = (idx: number) => {
    setSteps((prev) =>
      prev
        .filter((_, i) => i !== idx)
        .map((s, i) => ({ ...s, position: i })),
    );
  };

  const updateStepDelay = (idx: number, value: number) => {
    setSteps((prev) =>
      prev.map((s, i) => (i === idx ? { ...s, delayMinutes: value } : s)),
    );
  };

  const updateTargetType = (
    stepIdx: number,
    targetIdx: number,
    type: EscalationTargetType,
  ) => {
    setSteps((prev) =>
      prev.map((s, i) =>
        i === stepIdx
          ? {
              ...s,
              targets: s.targets.map((tar, ti) =>
                ti === targetIdx
                  ? { type, targetUid: type === "all_admins" ? "" : tar.targetUid }
                  : tar,
              ),
            }
          : s,
      ),
    );
  };

  const submit = async () => {
    await create.mutateAsync({
      slug,
      name,
      description: description || undefined,
      repeatMax,
      repeatAfterMinutes: repeatMax > 0 ? repeatAfterMinutes : null,
      steps: steps.map((s, i) => ({
        position: i,
        delayMinutes: s.delayMinutes,
        targets: s.targets.map((tar, ti) => ({
          ...tar,
          position: ti,
        })),
      })),
    });
    navigate({
      to: "/orgs/$org/escalation-policies",
      params: { org },
    });
  };

  return (
    <div className="space-y-6 max-w-3xl">
      <div className="flex items-center gap-2">
        <Button asChild variant="ghost" size="sm">
          <a href={`/dash0/orgs/${org}/escalation-policies`}>
            <ArrowLeft className="h-4 w-4 mr-1" />
            {t("common:back")}
          </a>
        </Button>
      </div>

      <h1 className="text-3xl font-bold tracking-tight">
        {t("escalation:new.title")}
      </h1>

      <Card>
        <CardHeader>
          <CardTitle>{t("escalation:editor.basics")}</CardTitle>
        </CardHeader>
        <CardContent className="space-y-4">
          <div className="grid grid-cols-2 gap-4">
            <div>
              <Label>{t("escalation:editor.slug")}</Label>
              <Input
                value={slug}
                onChange={(e) => setSlug(e.target.value)}
                placeholder="prod-paging"
              />
            </div>
            <div>
              <Label>{t("escalation:editor.name")}</Label>
              <Input
                value={name}
                onChange={(e) => setName(e.target.value)}
                placeholder="Production paging"
              />
            </div>
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
                  {t("escalation:editor.delayMinutes")}:
                </Label>
                <Input
                  type="number"
                  min={0}
                  className="w-20 h-8"
                  value={step.delayMinutes}
                  onChange={(e) =>
                    updateStepDelay(idx, parseInt(e.target.value, 10) || 0)
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
                  <div key={ti} className="flex gap-2 items-center text-sm">
                    <select
                      className="border rounded px-2 py-1 text-sm"
                      value={tar.type}
                      onChange={(e) =>
                        updateTargetType(
                          idx,
                          ti,
                          e.target.value as EscalationTargetType,
                        )
                      }
                    >
                      <option value="all_admins">
                        {t("escalation:targets.all_admins")}
                      </option>
                      <option value="schedule">
                        {t("escalation:targets.schedule")}
                      </option>
                      <option value="user">
                        {t("escalation:targets.user")}
                      </option>
                      <option value="connection">
                        {t("escalation:targets.connection")}
                      </option>
                    </select>
                    {tar.type !== "all_admins" && (
                      <Input
                        className="h-8 flex-1"
                        placeholder={t("escalation:editor.targetUidPlaceholder")}
                        value={tar.targetUid || ""}
                        onChange={(e) =>
                          setSteps((prev) =>
                            prev.map((s, i) =>
                              i === idx
                                ? {
                                    ...s,
                                    targets: s.targets.map((t2, j) =>
                                      j === ti
                                        ? { ...t2, targetUid: e.target.value }
                                        : t2,
                                    ),
                                  }
                                : s,
                            ),
                          )
                        }
                      />
                    )}
                  </div>
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
              onChange={(e) => setRepeatMax(parseInt(e.target.value, 10) || 0)}
            />
          </div>
          {repeatMax > 0 && (
            <div>
              <Label>{t("escalation:editor.repeatAfterMinutes")}</Label>
              <Input
                type="number"
                min={1}
                value={repeatAfterMinutes}
                onChange={(e) =>
                  setRepeatAfterMinutes(parseInt(e.target.value, 10) || 1)
                }
              />
            </div>
          )}
        </CardContent>
      </Card>

      <div className="flex justify-end gap-2">
        <Button
          variant="outline"
          onClick={() =>
            navigate({
              to: "/orgs/$org/escalation-policies",
              params: { org },
            })
          }
        >
          {t("common:cancel")}
        </Button>
        <Button
          onClick={submit}
          disabled={!slug || !name || create.isPending}
        >
          {create.isPending
            ? t("common:saving")
            : t("escalation:editor.create")}
        </Button>
      </div>
    </div>
  );
}
