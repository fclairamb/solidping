import { useState } from "react";
import { ArrowLeft, Loader2 } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Textarea } from "@/components/ui/textarea";
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
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import { useStatusPages, type StatusUpdate } from "@/api/hooks";

const STATUS_UPDATE_KINDS = [
  { value: "investigating", label: "Investigating" },
  { value: "identified", label: "Identified" },
  { value: "monitoring", label: "Monitoring" },
  { value: "resolved", label: "Resolved" },
  { value: "maintenance", label: "Maintenance" },
  { value: "info", label: "Info" },
];

export interface StatusUpdateFormData {
  statusPageUid: string;
  kind: string;
  title: string;
  bodyMarkdown: string;
  linkUrl: string;
  publishedAt: string;
}

export function StatusUpdateForm({
  org,
  mode,
  initialData,
  isPending,
  onSubmit,
  onCancel,
}: {
  org: string;
  mode: "create" | "edit";
  initialData?: StatusUpdate;
  isPending: boolean;
  onSubmit: (data: StatusUpdateFormData) => Promise<void>;
  onCancel: () => void;
}) {
  const { data: pages } = useStatusPages(org);

  const defaultPageUid =
    pages?.find((p) => p.isDefault)?.uid ?? pages?.[0]?.uid ?? "";

  const [form, setForm] = useState<StatusUpdateFormData>(() =>
    initialData
      ? {
          statusPageUid: initialData.statusPageUid,
          kind: initialData.kind,
          title: initialData.title,
          bodyMarkdown: initialData.bodyMarkdown,
          linkUrl: initialData.linkUrl ?? "",
          publishedAt: new Date(initialData.publishedAt)
            .toISOString()
            .slice(0, 16),
        }
      : {
          statusPageUid: defaultPageUid,
          kind: "info",
          title: "",
          bodyMarkdown: "",
          linkUrl: "",
          publishedAt: new Date().toISOString().slice(0, 16),
        }
  );

  // Once pages load, seed statusPageUid if still empty (create mode)
  const statusPageUid =
    form.statusPageUid || defaultPageUid;

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault();
    await onSubmit({ ...form, statusPageUid });
  };

  const title = mode === "create" ? "New status update" : "Edit status update";

  return (
    <form onSubmit={handleSubmit} className="space-y-6 max-w-2xl">
      <div className="flex items-center gap-4">
        <Button type="button" variant="ghost" size="icon" onClick={onCancel}>
          <ArrowLeft className="h-4 w-4" />
        </Button>
        <h1 className="text-3xl font-bold tracking-tight">{title}</h1>
      </div>

      <Card>
        <CardHeader>
          <CardTitle>Details</CardTitle>
        </CardHeader>
        <CardContent className="space-y-4">
          {mode === "create" && (
            <div className="space-y-1">
              <Label htmlFor="statusPage">Status page</Label>
              <Select
                value={statusPageUid}
                onValueChange={(v) =>
                  setForm((f) => ({ ...f, statusPageUid: v }))
                }
              >
                <SelectTrigger id="statusPage" data-testid="status-update-form-page">
                  <SelectValue placeholder="Select a status page" />
                </SelectTrigger>
                <SelectContent>
                  {(pages ?? []).map((p) => (
                    <SelectItem key={p.uid} value={p.uid}>
                      {p.name}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </div>
          )}

          <div className="space-y-1">
            <Label htmlFor="kind">Kind</Label>
            <Select
              value={form.kind}
              onValueChange={(v) => setForm((f) => ({ ...f, kind: v }))}
            >
              <SelectTrigger id="kind" data-testid="status-update-form-kind">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {STATUS_UPDATE_KINDS.map((k) => (
                  <SelectItem key={k.value} value={k.value}>
                    {k.label}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </div>

          <div className="space-y-1">
            <Label htmlFor="title">Title</Label>
            <Input
              id="title"
              data-testid="status-update-form-title"
              value={form.title}
              onChange={(e) =>
                setForm((f) => ({ ...f, title: e.target.value }))
              }
              maxLength={200}
              required
              placeholder="Investigating elevated error rates"
            />
          </div>

          <div className="space-y-1">
            <Label htmlFor="body">Body</Label>
            <Textarea
              id="body"
              data-testid="status-update-form-body"
              value={form.bodyMarkdown}
              onChange={(e) =>
                setForm((f) => ({ ...f, bodyMarkdown: e.target.value }))
              }
              required
              rows={4}
              placeholder="We are investigating an issue with..."
            />
          </div>

          <div className="space-y-1">
            <Label htmlFor="linkUrl">Link URL (optional)</Label>
            <Input
              id="linkUrl"
              type="url"
              value={form.linkUrl}
              onChange={(e) =>
                setForm((f) => ({ ...f, linkUrl: e.target.value }))
              }
              placeholder="https://status.example.com/incident/123"
            />
          </div>

          <div className="space-y-1">
            <Label htmlFor="publishedAt">Published at</Label>
            <Input
              id="publishedAt"
              type="datetime-local"
              value={form.publishedAt}
              onChange={(e) =>
                setForm((f) => ({ ...f, publishedAt: e.target.value }))
              }
            />
          </div>
        </CardContent>
      </Card>

      <div className="flex gap-2">
        <Button type="button" variant="outline" onClick={onCancel}>
          Cancel
        </Button>
        <Button type="submit" disabled={isPending} data-testid="status-update-form-submit">
          {isPending ? (
            <>
              <Loader2 className="mr-2 h-4 w-4 animate-spin" />
              Saving…
            </>
          ) : mode === "create" ? (
            "Create"
          ) : (
            "Save changes"
          )}
        </Button>
      </div>
    </form>
  );
}
