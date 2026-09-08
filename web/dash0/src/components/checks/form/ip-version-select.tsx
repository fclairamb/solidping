// The shared "IP version" selector.
//
// Like the tunnel selector next to it, this is protocol-agnostic: it edits the
// well-known `ipVersion` config key rather than belonging to a per-type `Fields`
// module, and which types may show it is server-declared capability metadata
// (`CheckTypeInfo.supportsIpVersion`), never a hard-coded list here.
import { useTranslation } from "react-i18next";
import { Label } from "@/components/ui/label";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";

// IP_VERSION_AUTO is both the default and the value submitted as "no
// constraint". It is a real (non-empty) string, so unlike the tunnel selector
// this needs no sentinel for Radix's Select — the form simply omits the key
// from the config when the value is "auto".
export const IP_VERSION_AUTO = "auto";

export interface IPVersionSelectProps {
  value: string;
  onChange: (ipVersion: string) => void;
  /** True when the check dials through an SSH tunnel. */
  tunneled?: boolean;
}

export function IPVersionSelect({
  value,
  onChange,
  tunneled = false,
}: IPVersionSelectProps) {
  const { t } = useTranslation("checks");
  return (
    <div className="space-y-2">
      <Label htmlFor="check-ip-version">{t("ipVersion.label")}</Label>
      <Select
        value={value === "" ? IP_VERSION_AUTO : value}
        onValueChange={onChange}
        disabled={tunneled}
      >
        <SelectTrigger
          id="check-ip-version"
          data-testid="check-ip-version-select"
        >
          <SelectValue placeholder={t("ipVersion.auto")} />
        </SelectTrigger>
        <SelectContent>
          <SelectItem value={IP_VERSION_AUTO}>{t("ipVersion.autoDefault")}</SelectItem>
          <SelectItem value="ipv4">{t("ipVersion.ipv4Only")}</SelectItem>
          <SelectItem value="ipv6">{t("ipVersion.ipv6Only")}</SelectItem>
        </SelectContent>
      </Select>
      {tunneled ? (
        <p
          className="text-xs text-muted-foreground"
          data-testid="check-ip-version-tunnel-note"
        >
          {t("ipVersion.tunneledHelp")}
        </p>
      ) : (
        <p className="text-xs text-muted-foreground">
          {t("ipVersion.autoHelp")}
        </p>
      )}
    </div>
  );
}
