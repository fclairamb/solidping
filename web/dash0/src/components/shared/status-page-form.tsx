import { useState, useEffect } from "react";
import { ArrowLeft, Loader2 } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Textarea } from "@/components/ui/textarea";
import { Switch } from "@/components/ui/switch";
import { slugify } from "@/lib/utils";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import type { AvailabilitySettings, StatusPage, StatusPagePeriod } from "@/api/hooks";

interface StatusPageFormData {
  name: string;
  slug: string;
  description: string;
  visibility: "public" | "private";
  isDefault: boolean;
  enabled: boolean;
  showAvailability: boolean;
  showResponseTime: boolean;
  historyPeriod: StatusPagePeriod;
  // Incident auto-publication (spec 2026-08-19-08).
  autoPublish: boolean;
  autoPublishDelaySeconds: number;
  autoResolve: "always" | "if_untouched" | "never";
  // Edit mode only (see StatusPageForm's mode==="edit" gate below). No deep
  // merge: null resets the section to the platform defaults (99.9/99.0),
  // an object replaces it wholly.
  settings?: { availability: AvailabilitySettings | null };
}

export function StatusPageForm({
  mode,
  initialData,
  isPending,
  onSubmit,
  onCancel,
}: {
  mode: "create" | "edit";
  initialData?: StatusPage;
  isPending: boolean;
  onSubmit: (data: StatusPageFormData) => Promise<void>;
  onCancel: () => void;
}) {
  const [name, setName] = useState(initialData?.name || "");
  const [slug, setSlug] = useState(initialData?.slug || "");
  const [slugManuallyEdited, setSlugManuallyEdited] = useState(mode === "edit");
  const [description, setDescription] = useState(initialData?.description || "");
  const [visibility, setVisibility] = useState<"public" | "private">(
    initialData?.visibility || "public"
  );
  const [isDefault, setIsDefault] = useState(initialData?.isDefault || false);
  const [enabled, setEnabled] = useState(initialData?.enabled ?? true);
  const [showAvailability, setShowAvailability] = useState(initialData?.showAvailability ?? true);
  const [showResponseTime, setShowResponseTime] = useState(initialData?.showResponseTime ?? true);
  const [historyPeriod, setHistoryPeriod] = useState<StatusPagePeriod>(
    initialData?.historyPeriod ?? "90d"
  );
  // Auto-publish. A NEW page defaults to on; an existing page shows whatever
  // the server says, which for pages created before this feature is off — the
  // migration deliberately did not opt anyone in retroactively.
  const [autoPublish, setAutoPublish] = useState(
    initialData?.autoPublish ?? mode === "create"
  );
  const [autoPublishDelayInput, setAutoPublishDelayInput] = useState(
    (initialData?.autoPublishDelaySeconds ?? 60).toString()
  );
  const [autoResolve, setAutoResolve] = useState<
    "always" | "if_untouched" | "never"
  >(
    (initialData?.autoResolve as "always" | "if_untouched" | "never") ??
      "if_untouched"
  );
  // Availability thresholds (edit mode only — defaults are right for a new
  // page). Kept as raw strings so an empty field can mean "use the default"
  // without fighting a numeric input's own empty-value coercion.
  const [thresholdUpInput, setThresholdUpInput] = useState(
    initialData?.settings?.availability?.thresholdUp?.toString() ?? ""
  );
  const [thresholdDegradedInput, setThresholdDegradedInput] = useState(
    initialData?.settings?.availability?.thresholdDegraded?.toString() ?? ""
  );

  useEffect(() => {
    if (!slugManuallyEdited && mode === "create") {
      setSlug(slugify(name));
    }
  }, [name, slugManuallyEdited, mode]);

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault();

    const settings =
      mode === "edit"
        ? {
            availability:
              thresholdUpInput.trim() === "" && thresholdDegradedInput.trim() === ""
                ? null
                : {
                    thresholdUp:
                      thresholdUpInput.trim() === "" ? undefined : Number(thresholdUpInput),
                    thresholdDegraded:
                      thresholdDegradedInput.trim() === ""
                        ? undefined
                        : Number(thresholdDegradedInput),
                  },
          }
        : undefined;

    await onSubmit({
      name,
      slug,
      description,
      visibility,
      isDefault,
      enabled,
      showAvailability,
      showResponseTime,
      historyPeriod,
      autoPublish,
      // An unparseable or blank delay falls back to the documented default
      // rather than sending NaN, which the API would reject with a validation
      // error the operator cannot act on.
      autoPublishDelaySeconds: Number.isFinite(Number(autoPublishDelayInput))
        ? Math.max(0, Math.trunc(Number(autoPublishDelayInput)))
        : 60,
      autoResolve,
      settings,
    });
  };

  return (
    <form onSubmit={handleSubmit} className="space-y-6 max-w-2xl">
      <div className="flex items-center gap-4">
        <Button type="button" variant="ghost" size="icon" onClick={onCancel}>
          <ArrowLeft className="h-4 w-4" />
        </Button>
        <h1 className="text-3xl font-bold tracking-tight">
          {mode === "create" ? "New Status Page" : "Edit Status Page"}
        </h1>
      </div>

      <Card>
        <CardHeader>
          <CardTitle>Status Page Details</CardTitle>
          <CardDescription>
            {mode === "create"
              ? "Create a new public status page for your services"
              : "Update your status page settings"}
          </CardDescription>
        </CardHeader>
        <CardContent className="space-y-4">
          <div className="space-y-2">
            <Label htmlFor="name">Name</Label>
            <Input
              id="name"
              value={name}
              onChange={(e) => setName(e.target.value)}
              placeholder="Production Services"
              required
            />
          </div>

          <div className="space-y-2">
            <Label htmlFor="slug">Slug</Label>
            <Input
              id="slug"
              value={slug}
              onChange={(e) => {
                setSlug(e.target.value);
                setSlugManuallyEdited(true);
              }}
              placeholder="production-services"
              required
              pattern="^[a-z][a-z0-9-]{2,99}$"
              title="3-100 characters, lowercase letters, digits, and hyphens"
            />
            <p className="text-xs text-muted-foreground">
              Used in the public URL. Lowercase letters, digits, and hyphens only.
            </p>
          </div>

          <div className="space-y-2">
            <Label htmlFor="description">Description</Label>
            <Textarea
              id="description"
              value={description}
              onChange={(e) => setDescription(e.target.value)}
              placeholder="Current status of our production services"
              rows={3}
            />
          </div>

          <div className="space-y-2">
            <Label htmlFor="visibility">Visibility</Label>
            <Select value={visibility} onValueChange={(v) => setVisibility(v as "public" | "private")}>
              <SelectTrigger>
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="public">Public</SelectItem>
                <SelectItem value="private">Private</SelectItem>
              </SelectContent>
            </Select>
          </div>

          <div className="flex items-center justify-between">
            <div className="space-y-0.5">
              <Label>Default Status Page</Label>
              <p className="text-xs text-muted-foreground">
                The default page is shown when visiting the organization's status URL
              </p>
            </div>
            <Switch checked={isDefault} onCheckedChange={setIsDefault} />
          </div>

          {mode === "edit" && (
            <div className="flex items-center justify-between">
              <div className="space-y-0.5">
                <Label>Enabled</Label>
                <p className="text-xs text-muted-foreground">
                  Disabled pages are not accessible publicly
                </p>
              </div>
              <Switch checked={enabled} onCheckedChange={setEnabled} />
            </div>
          )}
        </CardContent>
      </Card>

      <Card>
        <CardHeader>
          <CardTitle>Display Options</CardTitle>
          <CardDescription>
            Configure what information is shown on the public status page
          </CardDescription>
        </CardHeader>
        <CardContent className="space-y-4">
          <div className="flex items-center justify-between">
            <div className="space-y-0.5">
              <Label>Show Availability</Label>
              <p className="text-xs text-muted-foreground">
                Display availability percentage and daily uptime bars
              </p>
            </div>
            <Switch checked={showAvailability} onCheckedChange={setShowAvailability} />
          </div>

          {mode === "edit" && (
            <div className="grid grid-cols-1 gap-4 sm:grid-cols-2" data-testid="availability-thresholds">
              <div className="space-y-2">
                <Label htmlFor="thresholdUp">Up threshold (%)</Label>
                <Input
                  id="thresholdUp"
                  type="number"
                  min={0}
                  max={100}
                  step="any"
                  inputMode="decimal"
                  value={thresholdUpInput}
                  onChange={(e) => setThresholdUpInput(e.target.value)}
                  placeholder="99.9 (default)"
                />
              </div>
              <div className="space-y-2">
                <Label htmlFor="thresholdDegraded">Degraded threshold (%)</Label>
                <Input
                  id="thresholdDegraded"
                  type="number"
                  min={0}
                  max={100}
                  step="any"
                  inputMode="decimal"
                  value={thresholdDegradedInput}
                  onChange={(e) => setThresholdDegradedInput(e.target.value)}
                  placeholder="99.0 (default)"
                />
                <p className="text-xs text-muted-foreground sm:col-span-2">
                  Availability bars render green at or above the up threshold, amber
                  between the two, and red below the degraded threshold. Leave a field
                  empty to use the platform default.
                </p>
              </div>
            </div>
          )}

          <div className="flex items-center justify-between">
            <div className="space-y-0.5">
              <Label>Show Response Time</Label>
              <p className="text-xs text-muted-foreground">
                Display response time chart for each check
              </p>
            </div>
            <Switch checked={showResponseTime} onCheckedChange={setShowResponseTime} />
          </div>

          <div className="space-y-2">
            <Label htmlFor="historyPeriod">History Period</Label>
            <Select
              value={historyPeriod}
              onValueChange={(v) => setHistoryPeriod(v as StatusPagePeriod)}
            >
              <SelectTrigger id="historyPeriod">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="24h">24 hours</SelectItem>
                <SelectItem value="7d">7 days</SelectItem>
                <SelectItem value="30d">30 days</SelectItem>
                <SelectItem value="90d">90 days</SelectItem>
              </SelectContent>
            </Select>
          </div>
        </CardContent>
      </Card>

      <Card data-testid="status-page-auto-publish-card">
        <CardHeader>
          <CardTitle>Incident publication</CardTitle>
          <CardDescription>
            Turn monitoring incidents into public incidents on this page,
            automatically.
          </CardDescription>
        </CardHeader>
        <CardContent className="space-y-4">
          <div className="flex items-center justify-between gap-4">
            <div className="space-y-0.5">
              <Label htmlFor="autoPublish">Auto-publish incidents</Label>
              <p className="text-xs text-muted-foreground">
                Publish an incident affecting this page's components as a public
                incident, with a templated title and a first update.
              </p>
            </div>
            <Switch
              id="autoPublish"
              checked={autoPublish}
              onCheckedChange={setAutoPublish}
              data-testid="status-page-auto-publish-switch"
            />
          </div>

          <div className="space-y-2">
            <Label htmlFor="autoPublishDelaySeconds">
              Publication delay (seconds)
            </Label>
            <Input
              id="autoPublishDelaySeconds"
              type="number"
              min={0}
              max={86400}
              value={autoPublishDelayInput}
              onChange={(e) => setAutoPublishDelayInput(e.target.value)}
              disabled={!autoPublish}
              data-testid="status-page-auto-publish-delay"
            />
            <p className="text-xs text-muted-foreground">
              An incident that recovers within this window is never published —
              a short blip stays private. 0 publishes immediately.
            </p>
          </div>

          <div className="space-y-2">
            <Label htmlFor="autoResolve">When the incident resolves</Label>
            <Select
              value={autoResolve}
              onValueChange={(v) =>
                setAutoResolve(v as "always" | "if_untouched" | "never")
              }
              disabled={!autoPublish}
            >
              <SelectTrigger
                id="autoResolve"
                data-testid="status-page-auto-resolve"
              >
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="if_untouched">
                  Resolve it, unless someone edited it
                </SelectItem>
                <SelectItem value="always">Always resolve it</SelectItem>
                <SelectItem value="never">Leave it open</SelectItem>
              </SelectContent>
            </Select>
            <p className="text-xs text-muted-foreground">
              Once you edit a published incident it is yours: the default posts
              a "component recovered" note and leaves the final word to you.
            </p>
          </div>
        </CardContent>
      </Card>

      <div className="flex gap-3">
        <Button type="button" variant="outline" onClick={onCancel}>
          Cancel
        </Button>
        <Button type="submit" disabled={isPending || !name || !slug}>
          {isPending && <Loader2 className="mr-2 h-4 w-4 animate-spin" />}
          {mode === "create" ? "Create Status Page" : "Save Changes"}
        </Button>
      </div>
    </form>
  );
}
