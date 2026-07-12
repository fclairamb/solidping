import { useMemo } from "react";
import { X } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Label } from "@/components/ui/label";
import { CheckPicker } from "@/components/shared/check-picker";

interface DependsOnFormSectionProps {
  org: string;
  checkUid: string | undefined;
  parents: { uid: string; label: string }[];
  onAdd: (uid: string, label: string) => void;
  onRemove: (uid: string) => void;
}

// DependsOnFormSection is the body of the check form's Dependencies collapsible:
// pick parent checks whose downtime suppresses incident alerts on this check.
export function DependsOnFormSection({
  org,
  checkUid,
  parents,
  onAdd,
  onRemove,
}: DependsOnFormSectionProps) {
  const excludeUids = useMemo(() => {
    const set = new Set<string>(parents.map((p) => p.uid));
    if (checkUid) set.add(checkUid);
    return set;
  }, [parents, checkUid]);

  return (
    <div className="space-y-2">
      <Label>Dependencies</Label>
      <p className="text-xs text-muted-foreground">
        Parents whose downtime should suppress incident alerts on this check.
        Edit kind/description on the check detail page after save.
      </p>
      <div className="space-y-2">
        {parents.map((p) => (
          <div
            key={p.uid}
            className="flex items-center gap-2 rounded-md border p-2"
          >
            <span className="flex-1 truncate text-sm">{p.label}</span>
            <Button
              type="button"
              size="sm"
              variant="ghost"
              onClick={() => onRemove(p.uid)}
              aria-label="Remove parent"
            >
              <X className="h-3.5 w-3.5" />
            </Button>
          </div>
        ))}
        <CheckPicker
          org={org}
          excludeUids={excludeUids}
          onChange={(uid, c) => {
            if (uid) onAdd(uid, c?.name || c?.slug || uid);
          }}
          placeholder="Add a parent check…"
        />
      </div>
    </div>
  );
}
