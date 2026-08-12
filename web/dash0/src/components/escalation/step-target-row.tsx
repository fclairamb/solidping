// Type-aware target editor for one row of an escalation policy step. Replaces
// the raw "Target UID" textbox previously rendered inline in
// escalation-policies.{$uid,new}.tsx — operators can now pick from a
// dropdown of named entities instead of memorizing UUIDs.

import { useTranslation } from "react-i18next";
import { AlertTriangle } from "lucide-react";

import {
  type EscalationPolicyTarget,
  type EscalationTargetType,
  useIntegrations,
  useMembers,
  useOnCallSchedules,
} from "@/api/hooks";
import {
  IntegrationIcon,
  integrationLabel,
} from "@/components/integrations/integration-icon";
import {
  EmailOnlyBadge,
  useEmailOnlyUserUids,
} from "@/components/notifications/member-coverage";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";

interface Props {
  org: string;
  target: EscalationPolicyTarget;
  onChange: (next: EscalationPolicyTarget) => void;
}

export function StepTargetRow({ org, target, onChange }: Props) {
  const { t } = useTranslation(["escalation"]);

  const onTypeChange = (next: EscalationTargetType) => {
    onChange({
      ...target,
      type: next,
      // Switching type clears the selected UID so the new selector starts
      // unselected. all_admins has no UID at all.
      targetUid: next === "all_admins" ? "" : "",
    });
  };

  const onUidChange = (next: string) => {
    onChange({ ...target, targetUid: next });
  };

  return (
    <div className="flex gap-2 items-center text-sm">
      <Select value={target.type} onValueChange={(v) => onTypeChange(v as EscalationTargetType)}>
        <SelectTrigger className="h-8 w-[180px]">
          <SelectValue />
        </SelectTrigger>
        <SelectContent>
          <SelectItem value="all_admins">{t("escalation:targets.all_admins")}</SelectItem>
          <SelectItem value="schedule">{t("escalation:targets.schedule")}</SelectItem>
          <SelectItem value="user">{t("escalation:targets.user")}</SelectItem>
          <SelectItem value="connection">{t("escalation:targets.connection")}</SelectItem>
        </SelectContent>
      </Select>

      {target.type === "schedule" ? (
        <ScheduleSelect org={org} value={target.targetUid ?? ""} onChange={onUidChange} />
      ) : target.type === "user" ? (
        <UserSelect org={org} value={target.targetUid ?? ""} onChange={onUidChange} />
      ) : target.type === "connection" ? (
        <ConnectionSelect org={org} value={target.targetUid ?? ""} onChange={onUidChange} />
      ) : null}
    </div>
  );
}

function ScheduleSelect({
  org,
  value,
  onChange,
}: {
  org: string;
  value: string;
  onChange: (uid: string) => void;
}) {
  const { t } = useTranslation(["escalation"]);
  const { data: schedules, isLoading } = useOnCallSchedules(org);

  if (isLoading) {
    return <SelectShell placeholder={t("escalation:editor.loadingTargets")} />;
  }

  const list = (schedules ?? []).slice().sort((a, b) => a.name.localeCompare(b.name));
  if (list.length === 0) {
    return (
      <span className="text-xs text-muted-foreground">
        {t("escalation:editor.noSchedules")}{" "}
        <a
          href={`/dash0/orgs/${org}/on-call/new`}
          className="underline text-primary hover:text-primary/80"
        >
          {t("escalation:editor.createSchedule")}
        </a>
      </span>
    );
  }

  const missing = value && !list.some((s) => s.uid === value);

  return (
    <div className="flex flex-1 items-center gap-2">
      <Select value={value || undefined} onValueChange={onChange}>
        <SelectTrigger className="h-8 flex-1">
          <SelectValue placeholder={t("escalation:editor.pickSchedule")} />
        </SelectTrigger>
        <SelectContent>
          {list.map((s) => (
            <SelectItem key={s.uid} value={s.uid}>
              {s.name}
            </SelectItem>
          ))}
        </SelectContent>
      </Select>
      {missing ? <MissingTargetWarning kind="schedule" /> : null}
    </div>
  );
}

function UserSelect({
  org,
  value,
  onChange,
}: {
  org: string;
  value: string;
  onChange: (uid: string) => void;
}) {
  const { t } = useTranslation(["escalation"]);
  const { data, isLoading } = useMembers(org);
  // Paging a person who can only be reached by email is the quiet failure this
  // badge surfaces: the escalation job still "delivers", by email, which is not
  // a page.
  const emailOnlyUsers = useEmailOnlyUserUids(org);

  if (isLoading) {
    return <SelectShell placeholder={t("escalation:editor.loadingTargets")} />;
  }

  const list = (data?.data ?? [])
    .slice()
    .sort(
      (a, b) =>
        (a.name || a.email).localeCompare(b.name || b.email) ||
        a.email.localeCompare(b.email),
    );
  if (list.length === 0) {
    return (
      <span className="text-xs text-muted-foreground">
        {t("escalation:editor.noMembers")}
      </span>
    );
  }

  const missing = value && !list.some((m) => m.userUid === value);

  return (
    <div className="flex min-w-0 flex-1 items-center gap-2">
      <Select value={value || undefined} onValueChange={onChange}>
        <SelectTrigger className="h-8 min-w-0 flex-1">
          <SelectValue placeholder={t("escalation:editor.pickUser")} />
        </SelectTrigger>
        <SelectContent>
          {list.map((m) => (
            <SelectItem key={m.userUid} value={m.userUid}>
              <span className="truncate">
                {m.name || m.email}
                {m.name ? (
                  <span className="text-muted-foreground"> ({m.email})</span>
                ) : null}
              </span>
            </SelectItem>
          ))}
        </SelectContent>
      </Select>
      {missing ? <MissingTargetWarning kind="user" /> : null}
      {!missing && value && emailOnlyUsers.has(value) ? <EmailOnlyBadge /> : null}
    </div>
  );
}

function ConnectionSelect({
  org,
  value,
  onChange,
}: {
  org: string;
  value: string;
  onChange: (uid: string) => void;
}) {
  const { t } = useTranslation(["escalation"]);
  const { data: connections, isLoading } = useIntegrations(org);

  if (isLoading) {
    return <SelectShell placeholder={t("escalation:editor.loadingTargets")} />;
  }

  const list = (connections ?? []).slice().sort((a, b) => a.name.localeCompare(b.name));
  if (list.length === 0) {
    return (
      <span className="text-xs text-muted-foreground">
        {t("escalation:editor.noConnections")}{" "}
        <a
          href={`/dash0/orgs/${org}/integrations/new`}
          className="underline text-primary hover:text-primary/80"
        >
          {t("escalation:editor.createConnection")}
        </a>
      </span>
    );
  }

  const missing = value && !list.some((c) => c.uid === value);

  return (
    <div className="flex flex-1 items-center gap-2">
      <Select value={value || undefined} onValueChange={onChange}>
        <SelectTrigger className="h-8 flex-1">
          <SelectValue placeholder={t("escalation:editor.pickConnection")} />
        </SelectTrigger>
        <SelectContent>
          {list.map((c) => (
            <SelectItem
              key={c.uid}
              value={c.uid}
              className={c.enabled ? "" : "opacity-60"}
            >
              <span className="inline-flex items-center gap-2">
                <IntegrationIcon type={c.type} className="h-4 w-4" />
                <span>{c.name}</span>
                <span className="text-xs text-muted-foreground">
                  {integrationLabel(c.type)}
                </span>
              </span>
            </SelectItem>
          ))}
        </SelectContent>
      </Select>
      {missing ? <MissingTargetWarning kind="connection" /> : null}
    </div>
  );
}

function SelectShell({ placeholder }: { placeholder: string }) {
  return (
    <Select disabled value="">
      <SelectTrigger className="h-8 flex-1">
        <SelectValue placeholder={placeholder} />
      </SelectTrigger>
      <SelectContent />
    </Select>
  );
}

function MissingTargetWarning({ kind }: { kind: "schedule" | "user" | "connection" }) {
  const { t } = useTranslation(["escalation"]);
  return (
    <span
      className="inline-flex items-center gap-1 text-xs text-yellow-700 dark:text-yellow-400"
      title={t("escalation:editor.targetMissing", { kind })}
    >
      <AlertTriangle className="h-3.5 w-3.5" />
      {t("escalation:editor.targetMissingShort")}
    </span>
  );
}
