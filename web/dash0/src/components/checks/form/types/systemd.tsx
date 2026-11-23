import { useTranslation } from "react-i18next";
import { cn } from "@/lib/utils";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { getFieldError } from "@/hooks/use-check-validation";
import type { CheckTypeModule } from "./index";
import type { CheckConfig, CheckTypeFieldsProps, FieldErrors } from "./common";
import { getConfigField, validationMessage } from "./common";

// ── systemd unit (issue #1000) ──

/** `active` (default) means the unit must run, `inactive` that it must not. */
export type SystemdExpectedState = "active" | "inactive";
const SYSTEMD_STATES: readonly SystemdExpectedState[] = ["active", "inactive"];

/** Mirrors the backend rule (checksystemd/config/validate.go). */
const UNIT_PATTERN = /^[A-Za-z0-9:_.@-]+$/;
const MAX_UNIT_LENGTH = 256;

export interface SystemdState {
  unit: string;
  expectedState: SystemdExpectedState;
}

/** Returns the validation message key for a unit name, or null when valid. */
export function systemdUnitError(unit: string): string | null {
  if (!unit) return "systemdUnitRequired";
  if (
    unit.length > MAX_UNIT_LENGTH ||
    unit.startsWith("-") ||
    !UNIT_PATTERN.test(unit)
  ) {
    return "systemdUnitInvalid";
  }
  return null;
}

export const systemdModule: CheckTypeModule<SystemdState> = {
  types: ["systemd"],
  ownedKeys: ["unit", "expected_state"],
  fromConfig: (config) => ({
    unit: getConfigField(config, "unit"),
    expectedState:
      getConfigField(config, "expected_state") === "inactive"
        ? "inactive"
        : "active",
  }),
  toConfig: (state) => {
    const cfg: CheckConfig = {};
    if (state.unit) cfg.unit = state.unit;
    if (state.expectedState === "inactive") cfg.expected_state = "inactive";
    const errors: FieldErrors = [];
    const unitError = systemdUnitError(state.unit);
    if (unitError) {
      errors.push({ name: "unit", message: validationMessage(unitError) });
    }
    return { config: cfg, errors };
  },
  Fields: SystemdFields,
};

function SystemdFields({
  state,
  onChange,
  errors,
}: CheckTypeFieldsProps<SystemdState>) {
  const { t } = useTranslation("checks");
  const unitError = getFieldError(errors, "unit");
  return (
    <>
      <div className="space-y-2">
        <Label htmlFor="systemdUnit">{t("systemd.unit")}</Label>
        <Input
          id="systemdUnit"
          type="text"
          placeholder="nginx.service"
          value={state.unit}
          onChange={(e) => onChange({ ...state, unit: e.target.value })}
          className={cn("font-mono", unitError && "border-destructive")}
          data-testid="check-systemd-unit-input"
        />
        {unitError && <p className="text-xs text-destructive">{unitError}</p>}
        <p className="text-xs text-muted-foreground">{t("systemd.unitHelp")}</p>
      </div>
      <div className="space-y-2">
        <Label htmlFor="systemdExpectedState">
          {t("systemd.expectedState")}
        </Label>
        <Select
          value={state.expectedState}
          onValueChange={(value) =>
            onChange({
              ...state,
              expectedState: value as SystemdExpectedState,
            })
          }
        >
          <SelectTrigger
            id="systemdExpectedState"
            data-testid="check-systemd-expected-state-select"
          >
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            {SYSTEMD_STATES.map((value) => (
              <SelectItem
                key={value}
                value={value}
                data-testid={`check-systemd-expected-state-${value}`}
              >
                {t(`systemd.state_${value}`)}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
      </div>
    </>
  );
}
