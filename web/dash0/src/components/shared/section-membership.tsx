import { AlertTriangle } from "lucide-react";
import { useTranslation } from "react-i18next";

import { LabelInput } from "@/components/shared/label-input";
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert";
import { Label } from "@/components/ui/label";
import {
  SegmentedControl,
  type SegmentedControlOption,
} from "@/components/ui/segmented-control";
import type { StatusPageSectionSelector } from "@/api/hooks";

/**
 * How a status page section decides what it contains.
 *
 * - `manual` — today's behaviour, and the default for every section. The
 *   operator adds each component by hand.
 * - `all` — every check in the organization, now and in the future.
 * - `labels` — every check carrying ALL of the given key=value labels.
 *
 * The two dynamic modes MATERIALIZE real components, so a check created later
 * appears on the page with no manual action. That is the point: a board that
 * silently omits a new service — and therefore stays green while it is down —
 * is worse than no board.
 */
export type MembershipMode = "manual" | "all" | "labels";

export type SectionMembershipValue = {
  mode: MembershipMode;
  labels: Record<string, string>;
};

/**
 * Reads a section's stored selector back into editor state.
 *
 * An absent selector is `manual` — never guess otherwise. Auto-inclusion is
 * never a default.
 */
export function membershipFromSelector(
  selector: StatusPageSectionSelector | null | undefined,
): SectionMembershipValue {
  if (selector?.all) return { mode: "all", labels: {} };
  if (selector?.labels && Object.keys(selector.labels).length > 0) {
    return { mode: "labels", labels: selector.labels };
  }
  return { mode: "manual", labels: {} };
}

/**
 * Renders editor state as the request's `selector` field.
 *
 * `manual` returns `null` rather than `undefined`: on an update, null is what
 * CLEARS an existing selector, while an omitted key would leave it in place.
 * Callers that create a section drop the null themselves.
 */
export function selectorFromMembership(
  value: SectionMembershipValue,
): StatusPageSectionSelector | null {
  if (value.mode === "all") return { all: true };
  if (value.mode === "labels" && Object.keys(value.labels).length > 0) {
    return { labels: value.labels };
  }
  return null;
}

/** Whether the current editor state is a submittable membership rule. */
export function membershipIsComplete(value: SectionMembershipValue): boolean {
  return value.mode !== "labels" || Object.keys(value.labels).length > 0;
}

/**
 * Membership-mode picker for a status page section.
 *
 * `visibility` is not decoration. On a PUBLIC page a selector means every
 * future matching check reaches the public internet the moment it is created —
 * a scratch check named after an internal hostname included. The warning below
 * is the only thing standing between an operator and that, so it is shown
 * unconditionally for public pages, with the strongest wording reserved for
 * "all checks". Private and password pages get no warning: there is nothing to
 * disclose.
 */
export function SectionMembership({
  org,
  value,
  onChange,
  visibility,
  disabled,
}: {
  org: string;
  value: SectionMembershipValue;
  onChange: (next: SectionMembershipValue) => void;
  visibility?: string;
  disabled?: boolean;
}) {
  const { t } = useTranslation("statusPages");

  const options: SegmentedControlOption<MembershipMode>[] = [
    {
      value: "manual",
      label: t("sections.membership.manual"),
      testId: "section-membership-manual",
    },
    {
      value: "all",
      label: t("sections.membership.all"),
      testId: "section-membership-all",
    },
    {
      value: "labels",
      label: t("sections.membership.labels"),
      testId: "section-membership-labels",
    },
  ];

  const isPublic = visibility === "public";
  const showWarning = isPublic && value.mode !== "manual";

  return (
    <div className="space-y-3" data-testid="section-membership">
      <div className="space-y-2">
        <Label>{t("sections.membership.title")}</Label>
        <SegmentedControl
          value={value.mode}
          onValueChange={(mode) => onChange({ ...value, mode })}
          options={options}
          aria-label={t("sections.membership.title")}
          className="w-full"
        />
        <p className="text-xs text-muted-foreground">
          {t(`sections.membership.hint.${value.mode}`)}
        </p>
      </div>

      {value.mode === "labels" && (
        <div className="space-y-2">
          <Label>{t("sections.membership.labelsField")}</Label>
          <LabelInput
            org={org}
            value={value.labels}
            onChange={(labels) => onChange({ ...value, labels })}
            disabled={disabled}
          />
          <p className="text-xs text-muted-foreground">
            {t("sections.membership.labelsHint")}
          </p>
        </div>
      )}

      {showWarning && (
        <Alert
          variant="warning"
          data-testid="section-membership-public-warning"
        >
          <AlertTriangle />
          <AlertTitle>{t("sections.membership.publicWarningTitle")}</AlertTitle>
          <AlertDescription>
            {value.mode === "all"
              ? t("sections.membership.publicWarningAll")
              : t("sections.membership.publicWarningLabels")}
          </AlertDescription>
        </Alert>
      )}
    </div>
  );
}
