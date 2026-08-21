import { useEffect, useMemo, useState } from "react";
import { useStatusPages, useStatusPage, type StatusUpdate } from "@/api/hooks";
import { Button } from "@/components/ui/button";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import { Label } from "@/components/ui/label";
import { Input } from "@/components/ui/input";
import { Textarea } from "@/components/ui/textarea";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";

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
  sectionUid: string; // "none" = not set
  checkUid: string;   // "none" = not set
  kind: string;
  title: string;
  bodyMarkdown: string;
  linkUrl: string;
  publishedAt: string;
}

export function emptyFormData(defaultPageUid = ""): StatusUpdateFormData {
  return {
    statusPageUid: defaultPageUid,
    sectionUid: "none",
    checkUid: "none",
    kind: "info",
    title: "",
    bodyMarkdown: "",
    linkUrl: "",
    publishedAt: new Date().toISOString().slice(0, 16),
  };
}

export function formDataFromUpdate(update: StatusUpdate): StatusUpdateFormData {
  return {
    statusPageUid: update.statusPageUid,
    sectionUid: update.sectionUid ?? "none",
    checkUid: update.checkUid ?? "none",
    kind: update.kind,
    title: update.title,
    bodyMarkdown: update.bodyMarkdown,
    linkUrl: update.linkUrl ?? "",
    publishedAt: new Date(update.publishedAt).toISOString().slice(0, 16),
  };
}

interface StatusUpdateFormProps {
  org: string;
  mode: "create" | "edit";
  initialData?: StatusUpdate;
  isPending: boolean;
  onSubmit: (data: StatusUpdateFormData) => Promise<void>;
  onCancel: () => void;
}

export function StatusUpdateForm({
  org,
  mode,
  initialData,
  isPending,
  onSubmit,
  onCancel,
}: StatusUpdateFormProps) {
  const { data: pages } = useStatusPages(org);

  const [form, setForm] = useState<StatusUpdateFormData>(() =>
    initialData ? formDataFromUpdate(initialData) : emptyFormData()
  );

  // In create mode, default the status page to the first available one once
  // the list loads — the API rejects an empty statusPageUid (404), so the form
  // must never submit without a page selected.
  useEffect(() => {
    if (mode !== "create") return;
    if (form.statusPageUid) return;
    const first = pages?.[0];
    if (first) setForm((f) => ({ ...f, statusPageUid: first.uid }));
  }, [mode, pages, form.statusPageUid]);

  // Fetch the selected page with sections for cascading dropdowns
  const { data: selectedPage } = useStatusPage(
    org,
    form.statusPageUid,
    { with: "sections" }
  );

  const sections = useMemo(() => selectedPage?.sections ?? [], [selectedPage]);

  // Build check options based on selected section.
  // A status update pins to an INDIVIDUAL check, so group-targeting resources
  // (which have no checkUid) are not selectable here — see spec 2026-08-01-03.
  const checkOptions = useMemo(
    () =>
      (
        form.sectionUid !== "none"
          ? (sections.find((s) => s.uid === form.sectionUid)?.resources ?? [])
          : sections.flatMap((s) => s.resources ?? [])
      ).filter((r): r is typeof r & { checkUid: string } => Boolean(r.checkUid)),
    [sections, form.sectionUid]
  );

  // Guard against an inconsistent section/check pairing. handleSectionChange
  // already resets checkUid on an interactive section change; this covers the
  // case that can't be validated up front — edit mode seeds form state
  // directly from the loaded update (formDataFromUpdate) before sections have
  // loaded. effectiveCheckUid is purely derived (no effect / no extra
  // setState + render cycle): once selectedPage has loaded, it falls back to
  // "none" if the stored checkUid no longer appears among the current
  // section's options (e.g. the check's resource was removed from the page
  // since the update was created) — used for both what the Select displays
  // and what gets submitted, so a stale pairing can never be shown or sent.
  const effectiveCheckUid =
    selectedPage &&
    form.checkUid !== "none" &&
    !checkOptions.some((r) => r.checkUid === form.checkUid)
      ? "none"
      : form.checkUid;

  const handlePageChange = (v: string) => {
    setForm((f) => ({ ...f, statusPageUid: v, sectionUid: "none", checkUid: "none" }));
  };

  const handleSectionChange = (v: string) => {
    setForm((f) => ({ ...f, sectionUid: v, checkUid: "none" }));
  };

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault();
    await onSubmit({ ...form, checkUid: effectiveCheckUid });
  };

  const cardDescription =
    mode === "create"
      ? "Publish a new update on your status page."
      : (initialData?.title ?? "Edit this status update.");

  return (
    <form onSubmit={handleSubmit} className="space-y-4">
      <Card>
        <CardHeader>
          <CardTitle>Details</CardTitle>
          <CardDescription>{cardDescription}</CardDescription>
        </CardHeader>
        <CardContent className="space-y-4">
          {/* Status Page field */}
          <div className="space-y-1">
            <Label htmlFor="statusPage">Status page</Label>
            {mode === "create" ? (
              <Select value={form.statusPageUid} onValueChange={handlePageChange}>
                <SelectTrigger id="statusPage">
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
            ) : (
              <Select value={form.statusPageUid} disabled>
                <SelectTrigger id="statusPage">
                  <SelectValue>
                    {pages?.find((p) => p.uid === form.statusPageUid)?.name ??
                      form.statusPageUid}
                  </SelectValue>
                </SelectTrigger>
                <SelectContent>
                  {(pages ?? []).map((p) => (
                    <SelectItem key={p.uid} value={p.uid}>
                      {p.name}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            )}
          </div>

          {/* Section field (optional) */}
          <div className="space-y-1">
            <Label htmlFor="section">Section (optional)</Label>
            <Select
              value={form.sectionUid}
              onValueChange={handleSectionChange}
              disabled={!form.statusPageUid}
            >
              <SelectTrigger id="section" data-testid="status-update-form-section">
                <SelectValue placeholder="No section" />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="none">No section</SelectItem>
                {sections.map((s) => (
                  <SelectItem key={s.uid} value={s.uid}>
                    {s.name}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </div>

          {/* Check field (optional) */}
          <div className="space-y-1">
            <Label htmlFor="check">Check (optional)</Label>
            <Select
              value={effectiveCheckUid}
              onValueChange={(v) => setForm((f) => ({ ...f, checkUid: v }))}
              disabled={!form.statusPageUid}
            >
              <SelectTrigger id="check" data-testid="status-update-form-check">
                <SelectValue placeholder="No check" />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="none">No check</SelectItem>
                {checkOptions.map((r) => (
                  <SelectItem key={r.checkUid} value={r.checkUid}>
                    {r.check?.name ?? r.checkUid.slice(0, 8)}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </div>

          {/* Kind field */}
          <div className="space-y-1">
            <Label htmlFor="kind">Kind</Label>
            <Select
              value={form.kind}
              onValueChange={(v) => setForm((f) => ({ ...f, kind: v }))}
            >
              <SelectTrigger id="kind">
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

          {/* Title field */}
          <div className="space-y-1">
            <Label htmlFor="title">Title</Label>
            <Input
              id="title"
              value={form.title}
              onChange={(e) => setForm((f) => ({ ...f, title: e.target.value }))}
              maxLength={200}
              required
              placeholder="Investigating elevated error rates"
              data-testid="status-update-form-title"
            />
          </div>

          {/* Body field */}
          <div className="space-y-1">
            <Label htmlFor="body">Body</Label>
            <Textarea
              id="body"
              value={form.bodyMarkdown}
              onChange={(e) => setForm((f) => ({ ...f, bodyMarkdown: e.target.value }))}
              required
              rows={4}
              placeholder="We are investigating an issue with..."
              data-testid="status-update-form-body"
            />
          </div>

          {/* Link URL field */}
          <div className="space-y-1">
            <Label htmlFor="linkUrl">Link URL (optional)</Label>
            <Input
              id="linkUrl"
              type="url"
              value={form.linkUrl}
              onChange={(e) => setForm((f) => ({ ...f, linkUrl: e.target.value }))}
              placeholder="https://status.example.com/incident/123"
              data-testid="status-update-form-link-url"
            />
          </div>

          {/* Published at field */}
          <div className="space-y-1">
            <Label htmlFor="publishedAt">Published at</Label>
            <Input
              id="publishedAt"
              type="datetime-local"
              value={form.publishedAt}
              onChange={(e) => setForm((f) => ({ ...f, publishedAt: e.target.value }))}
            />
          </div>
        </CardContent>
      </Card>

      {/* Submit row */}
      <div className="flex justify-end gap-2">
        <Button type="button" variant="outline" onClick={onCancel}>
          Cancel
        </Button>
        <Button type="submit" disabled={isPending} data-testid="status-update-form-submit">
          {isPending ? "Saving…" : mode === "create" ? "Create" : "Save changes"}
        </Button>
      </div>
    </form>
  );
}
