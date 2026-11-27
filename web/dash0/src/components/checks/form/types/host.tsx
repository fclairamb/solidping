import { useTranslation } from "react-i18next";
import { cn } from "@/lib/utils";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { getFieldError } from "@/hooks/use-check-validation";
import type { CheckTypeModule } from "./index";
import type { CheckConfig, CheckTypeFieldsProps, FieldErrors } from "./common";
import { getConfigField, validationMessage } from "./common";

// ── host resources: disk / memory / CPU of the executing host ──

type Metric = "disk" | "memory" | "cpu";
const METRICS: readonly Metric[] = ["disk", "memory", "cpu"];

export interface HostState {
  diskPath: string;
  diskWarningPercent: string;
  diskCriticalPercent: string;
  memoryWarningPercent: string;
  memoryCriticalPercent: string;
  cpuWarningPercent: string;
  cpuCriticalPercent: string;
}

type ThresholdKey = Exclude<keyof HostState, "diskPath">;

const THRESHOLD_KEYS: readonly ThresholdKey[] = [
  "diskWarningPercent",
  "diskCriticalPercent",
  "memoryWarningPercent",
  "memoryCriticalPercent",
  "cpuWarningPercent",
  "cpuCriticalPercent",
];

/** Returns the validation message key for a percent, or null when valid. */
export function hostPercentError(raw: string): string | null {
  if (raw.trim() === "") return null;
  const value = Number(raw);
  if (!Number.isFinite(value) || value <= 0 || value > 100) {
    return "hostPercentInvalid";
  }
  return null;
}

/** Mirrors the backend rule: an absolute path (POSIX, or a Windows drive). */
export function hostDiskPathError(path: string): string | null {
  if (path === "") return null;
  if (path.startsWith("/") || /^[A-Za-z]:[\\/]/.test(path)) return null;
  return "hostDiskPathInvalid";
}

function readState(config: CheckConfig, key: string): string {
  const value = config[key];
  return typeof value === "number" ? String(value) : "";
}

export const hostModule: CheckTypeModule<HostState> = {
  types: ["host"],
  ownedKeys: ["diskPath", ...THRESHOLD_KEYS],
  fromConfig: (config) => ({
    diskPath: getConfigField(config, "diskPath"),
    diskWarningPercent: readState(config, "diskWarningPercent"),
    diskCriticalPercent: readState(config, "diskCriticalPercent"),
    memoryWarningPercent: readState(config, "memoryWarningPercent"),
    memoryCriticalPercent: readState(config, "memoryCriticalPercent"),
    cpuWarningPercent: readState(config, "cpuWarningPercent"),
    cpuCriticalPercent: readState(config, "cpuCriticalPercent"),
  }),
  toConfig: (state) => {
    const cfg: CheckConfig = {};
    const errors: FieldErrors = [];

    if (state.diskPath) cfg.diskPath = state.diskPath;
    const pathError = hostDiskPathError(state.diskPath);
    if (pathError) {
      errors.push({ name: "diskPath", message: validationMessage(pathError) });
    }

    for (const key of THRESHOLD_KEYS) {
      const raw = state[key].trim();
      if (raw === "") continue;
      const error = hostPercentError(raw);
      if (error) {
        errors.push({ name: key, message: validationMessage(error) });
      } else {
        cfg[key] = Number(raw);
      }
    }

    for (const metric of METRICS) {
      const warn = cfg[`${metric}WarningPercent`];
      const crit = cfg[`${metric}CriticalPercent`];
      if (typeof warn === "number" && typeof crit === "number" && warn >= crit) {
        errors.push({
          name: `${metric}WarningPercent`,
          message: validationMessage("hostOrderInvalid"),
        });
      }
    }

    return { config: cfg, errors };
  },
  Fields: HostFields,
};

function HostFields({ state, onChange, errors }: CheckTypeFieldsProps<HostState>) {
  const { t } = useTranslation("checks");
  const pathError = getFieldError(errors, "diskPath");

  return (
    <>
      <div className="space-y-2">
        <Label htmlFor="hostDiskPath">{t("host.diskPath")}</Label>
        <Input
          id="hostDiskPath"
          type="text"
          placeholder="/"
          value={state.diskPath}
          onChange={(e) => onChange({ ...state, diskPath: e.target.value })}
          className={cn("font-mono", pathError && "border-destructive")}
          data-testid="check-host-disk-path-input"
        />
        {pathError && <p className="text-xs text-destructive">{pathError}</p>}
        <p className="text-xs text-muted-foreground">{t("host.diskPathHelp")}</p>
      </div>
      {METRICS.map((metric) => {
        const warnKey = `${metric}WarningPercent` as ThresholdKey;
        const critKey = `${metric}CriticalPercent` as ThresholdKey;
        const warnError = getFieldError(errors, warnKey);
        const critError = getFieldError(errors, critKey);
        return (
          <div key={metric} className="space-y-2">
            <Label>{t(`host.${metric}`)}</Label>
            <div className="grid grid-cols-2 gap-2">
              <div className="space-y-1">
                <Label htmlFor={`host-${metric}-warning`} className="text-xs">
                  {t("host.warning")}
                </Label>
                <Input
                  id={`host-${metric}-warning`}
                  type="number"
                  min={0}
                  max={100}
                  value={state[warnKey]}
                  onChange={(e) => onChange({ ...state, [warnKey]: e.target.value })}
                  className={cn(warnError && "border-destructive")}
                  data-testid={`check-host-${metric}-warning-input`}
                />
                {warnError && <p className="text-xs text-destructive">{warnError}</p>}
              </div>
              <div className="space-y-1">
                <Label htmlFor={`host-${metric}-critical`} className="text-xs">
                  {t("host.critical")}
                </Label>
                <Input
                  id={`host-${metric}-critical`}
                  type="number"
                  min={0}
                  max={100}
                  value={state[critKey]}
                  onChange={(e) => onChange({ ...state, [critKey]: e.target.value })}
                  className={cn(critError && "border-destructive")}
                  data-testid={`check-host-${metric}-critical-input`}
                />
                {critError && <p className="text-xs text-destructive">{critError}</p>}
              </div>
            </div>
          </div>
        );
      })}
      <p className="text-xs text-muted-foreground">{t("host.help")}</p>
    </>
  );
}
