import { useState, useEffect } from "react";
import { ArrowLeft, Loader2 } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Textarea } from "@/components/ui/textarea";
import { Switch } from "@/components/ui/switch";
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
import type { StatusPage } from "@/api/hooks";

interface StatusPageFormData {
  name: string;
  slug: string;
  description: string;
  visibility: "public" | "private";
  isDefault: boolean;
  enabled: boolean;
  showAvailability: boolean;
  showResponseTime: boolean;
  historyDays: number;
}

function slugify(name: string): string {
  return name
    .toLowerCase()
    .replace(/[^a-z0-9-]/g, "-")
    .replace(/-+/g, "-")
    .replace(/^-|-$/g, "")
    .slice(0, 40);
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
  const [historyDays, setHistoryDays] = useState(initialData?.historyDays ?? 90);

  useEffect(() => {
    if (!slugManuallyEdited && mode === "create") {
      setSlug(slugify(name));
    }
  }, [name, slugManuallyEdited, mode]);

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault();
    await onSubmit({ name, slug, description, visibility, isDefault, enabled, showAvailability, showResponseTime, historyDays });
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
              pattern="^[a-z][a-z0-9-]{2,39}$"
              title="3-40 characters, lowercase letters, digits, and hyphens"
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
            <Label htmlFor="historyDays">History Period</Label>
            <Select
              value={String(historyDays)}
              onValueChange={(v) => setHistoryDays(Number(v))}
            >
              <SelectTrigger>
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="7">7 days</SelectItem>
                <SelectItem value="30">30 days</SelectItem>
                <SelectItem value="90">90 days</SelectItem>
              </SelectContent>
            </Select>
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
