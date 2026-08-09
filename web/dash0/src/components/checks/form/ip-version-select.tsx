// The shared "IP version" selector.
//
// Like the tunnel selector next to it, this is protocol-agnostic: it edits the
// well-known `ipVersion` config key rather than belonging to a per-type `Fields`
// module, and which types may show it is server-declared capability metadata
// (`CheckTypeInfo.supportsIpVersion`), never a hard-coded list here.
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
  return (
    <div className="space-y-2">
      <Label htmlFor="check-ip-version">IP version (optional)</Label>
      <Select
        value={value === "" ? IP_VERSION_AUTO : value}
        onValueChange={onChange}
        disabled={tunneled}
      >
        <SelectTrigger
          id="check-ip-version"
          data-testid="check-ip-version-select"
        >
          <SelectValue placeholder="Auto" />
        </SelectTrigger>
        <SelectContent>
          <SelectItem value={IP_VERSION_AUTO}>Auto (default)</SelectItem>
          <SelectItem value="ipv4">IPv4 only</SelectItem>
          <SelectItem value="ipv6">IPv6 only</SelectItem>
        </SelectContent>
      </Select>
      {tunneled ? (
        <p
          className="text-xs text-muted-foreground"
          data-testid="check-ip-version-tunnel-note"
        >
          Not available on a tunneled check: the SSH bastion resolves the
          hostname and dials it, so the address family is the tunnel&apos;s to
          choose, not this check&apos;s.
        </p>
      ) : (
        <p className="text-xs text-muted-foreground">
          Auto keeps today&apos;s behaviour — one address, whichever the target
          resolves to first. Pin a family to actually verify it: an IPv4 check
          never proves the target is reachable over IPv6, so a dual-stack host
          with a broken AAAA path still reports up. To watch both, create two
          checks.
        </p>
      )}
    </div>
  );
}
