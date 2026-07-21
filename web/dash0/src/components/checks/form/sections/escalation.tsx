import { useMemo } from "react";
import { Link } from "@tanstack/react-router";
import { Label } from "@/components/ui/label";
import {
  Select,
  SelectContent,
  SelectGroup,
  SelectItem,
  SelectLabel,
  SelectSeparator,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import {
  useEscalationPolicies,
  useCreateEscalationPolicy,
  useOrgSettings,
  type CheckGroup,
  type EscalationPolicy,
} from "@/api/hooks";

// Sentinels for the two non-UID options. "" (empty escalationPolicyUid) is the
// inherit state; Radix Select needs a non-empty value string, so we map it to
// INHERIT here and back to "" in form state.
const INHERIT = "__inherit__";
const SILENT_SHORTCUT = "__silent__";

// Suggested name for the auto-created zero-step "silent" policy.
const SILENT_POLICY_NAME = "No escalation";

function stepCountOf(policy: EscalationPolicy): number {
  // The list response carries stepCount; fall back to the expanded steps array
  // if a caller ever passes a detail-shaped policy.
  return policy.stepCount ?? policy.steps?.length ?? 0;
}

function isSilent(policy: EscalationPolicy): boolean {
  return stepCountOf(policy) === 0;
}

interface EscalationSelectProps {
  org: string;
  /** Current escalationPolicyUid: "" = inherit, or a policy UID. */
  value: string;
  onChange: (value: string) => void;
  /** The check's currently-selected group (for the inherit resolution). */
  checkGroupUid?: string;
  checkGroups?: CheckGroup[];
  /**
   * "check" (default): inherit resolves check's group → org default → none.
   * "group": this picker is editing a GROUP's own policy, which has no
   * further group to inherit from — inherit resolves straight to the org
   * default, skipping the group-resolution step. `checkGroupUid`/
   * `checkGroups` are ignored in this variant.
   */
  variant?: "check" | "group";
}

// EscalationSelect is the check form's escalation-policy picker (and, via
// variant="group", the check-group edit form's). It offers the inherit
// default (live-resolving down the chain so the choice is never blind), the
// org's policies (silent ones badged), and a "No escalation (silent)"
// shortcut that reuses or creates a zero-step policy.
export function EscalationSelect({
  org,
  value,
  onChange,
  checkGroupUid,
  checkGroups,
  variant = "check",
}: EscalationSelectProps) {
  const { data: policies } = useEscalationPolicies(org);
  // Org settings is admin-only; a non-admin editing a check simply won't see
  // the org-default half of the inherit label (the query errors and we treat
  // the default as unknown). The picker stays fully functional.
  const { data: orgSettings } = useOrgSettings(org);
  const createPolicy = useCreateEscalationPolicy(org);

  const policyByUid = useMemo(() => {
    const map = new Map<string, EscalationPolicy>();
    for (const p of policies ?? []) map.set(p.uid, p);
    return map;
  }, [policies]);

  // Resolve what "Inherit" currently means. For a check, the group's policy
  // wins, then the org default, then nothing — mirrors the server resolver
  // (minus the check's own policy, which is what this picker overrides). For
  // a group itself, there is no further group to resolve through: inherit
  // goes straight to the org default.
  const group =
    variant === "check" && checkGroupUid
      ? checkGroups?.find((g) => g.uid === checkGroupUid)
      : undefined;
  const inheritedUid =
    (group?.escalationPolicyUid ?? undefined) ||
    (orgSettings?.defaultEscalationPolicyUid ?? undefined) ||
    undefined;
  const inheritedPolicy = inheritedUid ? policyByUid.get(inheritedUid) : undefined;
  const inheritedName = inheritedPolicy
    ? `${inheritedPolicy.name}${isSilent(inheritedPolicy) ? " (silent)" : ""}`
    : "nothing";

  const selected = value ? policyByUid.get(value) : undefined;
  const selectValue = value ? value : INHERIT;

  const handleChange = async (next: string) => {
    if (next === "") {
      // Radix mirrors the controlled value onto a hidden native <select> for
      // form/accessibility compatibility. That mirror only knows about
      // policies whose SelectItem has rendered as a native <option> at least
      // once; assigning a uid that was never mounted (e.g. a policy created
      // by the silent shortcut below, after which the popover is already
      // closed and its item never gets a chance to render) leaves the native
      // element without a matching option, and it reports back an empty
      // change event that would otherwise stomp our selection back to "".
      // No real SelectItem in this list ever uses "" as its value — INHERIT
      // is the sentinel "__inherit__" — so this is always that spurious
      // echo, never a genuine user selection. Ignore it.
      return;
    }
    if (next === INHERIT) {
      onChange("");
      return;
    }
    if (next === SILENT_SHORTCUT) {
      // Reuse an existing zero-step policy if one exists; else create one.
      const existing = (policies ?? []).find(isSilent);
      if (existing) {
        onChange(existing.uid);
        return;
      }
      try {
        const created = await createPolicy.mutateAsync({
          name: SILENT_POLICY_NAME,
          repeatMax: 0,
          steps: [],
        });
        onChange(created.uid);
      } catch {
        // Leave the selection unchanged on failure; the form's own error
        // surfaces on submit if needed.
      }
      return;
    }
    onChange(next);
  };

  const showSilentNote = selected !== undefined && isSilent(selected);

  // Radix's SelectValue mirrors whichever SelectItem's text last MOUNTED
  // while the dropdown was open — it does not re-derive from `value` once
  // closed. Picking the silent shortcut can set `value` to a policy uid
  // whose SelectItem never rendered in that open session (freshly created,
  // not yet in `policies` when the menu was showing), which would otherwise
  // freeze the trigger on the previously-shown label forever. Compute the
  // label ourselves so it always reflects current state.
  const currentLabel =
    selectValue === INHERIT
      ? `Inherit — currently: ${inheritedName}`
      : selected
        ? `${selected.name}${isSilent(selected) ? " — silent" : ""}`
        : undefined;

  return (
    <div className="space-y-2">
      <Label htmlFor="escalation-policy-select">Escalation policy</Label>
      <Select value={selectValue} onValueChange={handleChange}>
        <SelectTrigger
          id="escalation-policy-select"
          data-testid="escalation-policy-select"
        >
          <SelectValue>{currentLabel}</SelectValue>
        </SelectTrigger>
        <SelectContent>
          <SelectItem value={INHERIT} data-testid="escalation-option-inherit">
            Inherit — currently: {inheritedName}
          </SelectItem>
          {(policies ?? []).length > 0 && (
            <SelectGroup>
              <SelectLabel>Policies</SelectLabel>
              {(policies ?? []).map((p) => (
                <SelectItem key={p.uid} value={p.uid}>
                  {p.name}
                  {isSilent(p) ? " — silent" : ""}
                </SelectItem>
              ))}
            </SelectGroup>
          )}
          <SelectSeparator />
          <SelectItem
            value={SILENT_SHORTCUT}
            data-testid="escalation-option-silent"
          >
            No escalation (silent)
          </SelectItem>
        </SelectContent>
      </Select>
      {showSilentNote ? (
        <p
          className="text-xs text-muted-foreground"
          data-testid="escalation-silent-note"
        >
          This policy has no steps — this check will never page anyone.
        </p>
      ) : (
        <p className="text-xs text-muted-foreground">
          Who gets paged, in order, when this check fails.{" "}
          <Link
            to="/orgs/$org/escalation-policies"
            params={{ org }}
            className="text-primary underline-offset-4 hover:underline"
          >
            Manage policies
          </Link>
        </p>
      )}
    </div>
  );
}
