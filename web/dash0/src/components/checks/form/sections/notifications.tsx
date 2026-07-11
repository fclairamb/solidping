import { Link } from "@tanstack/react-router";
import { Label } from "@/components/ui/label";
import { Checkbox } from "@/components/ui/checkbox";
import { Badge } from "@/components/ui/badge";
import { canNotify } from "@/api/hooks";
import type { Integration } from "@/api/hooks";
import {
  IntegrationIcon,
  integrationLabel,
} from "@/components/integrations/integration-icon";

interface NotifyViaSectionProps {
  org: string;
  connections: Integration[] | undefined;
  selected: string[];
  onToggle: (uid: string) => void;
}

// NotifyViaSection is the always-visible Notifications section of the check
// form: pick which notify-capable integrations get paged when the check fails.
export function NotifyViaSection({
  org,
  connections,
  selected,
  onToggle,
}: NotifyViaSectionProps) {
  // Only notify-capable integrations can be bound as notification targets —
  // data sources (e.g. Freebox) never appear here. This is the visible half of
  // the silent-no-op bug fix.
  const list = (connections ?? []).filter((c) => canNotify(c.type));
  // Disabled channels stay listed if currently bound so the user can unbind
  // them; otherwise they're hidden from the picker.
  const visible = list.filter((c) => c.enabled || selected.includes(c.uid));

  if (visible.length === 0) {
    return (
      <div className="space-y-2">
        <Label>Notify via</Label>
        <div className="rounded border border-dashed p-3 text-sm text-muted-foreground">
          No channels yet.{" "}
          <Link
            to="/orgs/$org/integrations/new"
            params={{ org }}
            className="font-medium text-primary underline-offset-4 hover:underline"
          >
            Create one
          </Link>{" "}
          to be paged when this check fails.
        </div>
      </div>
    );
  }

  return (
    <div className="space-y-2">
      <Label>Notify via</Label>
      <div className="grid gap-2 sm:grid-cols-2">
        {visible.map((c) => {
          const checked = selected.includes(c.uid);
          const cbId = `notify-via-${c.uid}`;
          return (
            <label
              key={c.uid}
              htmlFor={cbId}
              className="flex items-center gap-2 rounded-md border p-2 cursor-pointer hover:bg-muted/50"
            >
              <Checkbox
                id={cbId}
                checked={checked}
                onCheckedChange={() => onToggle(c.uid)}
              />
              <IntegrationIcon
                type={c.type}
                className="h-4 w-4 text-muted-foreground"
              />
              <span className="text-sm flex-1 truncate">{c.name}</span>
              <span className="text-xs text-muted-foreground">
                {integrationLabel(c.type)}
              </span>
              {!c.enabled && (
                <Badge variant="outline" className="text-xs">
                  disabled
                </Badge>
              )}
            </label>
          );
        })}
      </div>
      <p className="text-xs text-muted-foreground">
        Channels selected here are notified on incident events.{" "}
        <Link
          to="/orgs/$org/integrations"
          params={{ org }}
          className="text-primary underline-offset-4 hover:underline"
        >
          Manage channels
        </Link>
      </p>
    </div>
  );
}
