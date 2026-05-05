import { useState, useMemo } from "react";
import { createFileRoute, useNavigate } from "@tanstack/react-router";
import { useTranslation } from "react-i18next";

import {
  useCreateOnCallSchedule,
  useMembers,
  type OnCallRotationType,
} from "@/api/hooks";
import { Button } from "@/components/ui/button";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { Textarea } from "@/components/ui/textarea";

export const Route = createFileRoute("/orgs/$org/on-call/new")({
  component: NewOnCallSchedulePage,
});

// Default-from-browser timezone is the v1 simplification — orgs don't carry a
// timezone yet (see spec). Falls back to UTC if Intl is unavailable for any
// reason (older browsers, weirdly configured environments).
function defaultBrowserTimezone(): string {
  try {
    return Intl.DateTimeFormat().resolvedOptions().timeZone || "UTC";
  } catch {
    return "UTC";
  }
}

function nextMondayAt9amISO(tz: string): string {
  const now = new Date();
  // Compute Monday in *local* time then format as RFC3339 with offset.
  const dayDiff = (8 - now.getDay()) % 7 || 7; // 1..7 forward to next Monday
  const mon = new Date(now);
  mon.setDate(now.getDate() + dayDiff);
  mon.setHours(9, 0, 0, 0);
  // Best-effort "ISO with the user's local offset" — the backend stores it
  // and the resolver re-anchors in `tz`, so a slightly off offset is harmless.
  void tz;
  return mon.toISOString();
}

function NewOnCallSchedulePage() {
  const { t } = useTranslation(["oncall", "common"]);
  const { org } = Route.useParams();
  const navigate = useNavigate();
  const tz = defaultBrowserTimezone();

  const [name, setName] = useState("");
  const [slug, setSlug] = useState("");
  const [description, setDescription] = useState("");
  const [timezone, setTimezone] = useState(tz);
  const [rotationType, setRotationType] = useState<OnCallRotationType>("weekly");
  const [handoffTime, setHandoffTime] = useState("09:00");
  const [handoffWeekday, setHandoffWeekday] = useState<number>(0);
  const [startAt, setStartAt] = useState(nextMondayAt9amISO(tz));
  const [selectedUsers, setSelectedUsers] = useState<string[]>([]);
  const [error, setError] = useState<string | null>(null);

  const { data: membersResp } = useMembers(org);
  const members = useMemo(() => membersResp?.data ?? [], [membersResp]);
  const create = useCreateOnCallSchedule(org);

  const handleSubmit = async (event: React.FormEvent) => {
    event.preventDefault();
    setError(null);
    try {
      const created = await create.mutateAsync({
        slug,
        name,
        description: description || undefined,
        timezone,
        rotationType,
        handoffTime,
        handoffWeekday: rotationType === "weekly" ? handoffWeekday : undefined,
        startAt,
        userUids: selectedUsers,
      });
      navigate({
        to: "/orgs/$org/on-call/$slug",
        params: { org, slug: created.slug },
      });
    } catch (err) {
      setError(err instanceof Error ? err.message : t("oncall:errors.generic"));
    }
  };

  const toggleUser = (uid: string) => {
    setSelectedUsers((prev) =>
      prev.includes(uid) ? prev.filter((u) => u !== uid) : [...prev, uid],
    );
  };

  return (
    <div className="space-y-6 max-w-2xl">
      <div>
        <h1 className="text-3xl font-bold tracking-tight">
          {t("oncall:form.create")}
        </h1>
      </div>

      <Card>
        <CardHeader>
          <CardTitle>{t("oncall:form.create")}</CardTitle>
          <CardDescription>{t("oncall:detail.v1Notes")}</CardDescription>
        </CardHeader>
        <CardContent>
          <form className="space-y-4" onSubmit={handleSubmit}>
            <div className="space-y-2">
              <Label htmlFor="oncall-name">{t("oncall:form.name")}</Label>
              <Input
                id="oncall-name"
                value={name}
                onChange={(e) => setName(e.target.value)}
                placeholder={t("oncall:form.namePlaceholder")}
                required
              />
            </div>

            <div className="space-y-2">
              <Label htmlFor="oncall-slug">{t("oncall:form.slug")}</Label>
              <Input
                id="oncall-slug"
                value={slug}
                onChange={(e) => setSlug(e.target.value)}
                placeholder={t("oncall:form.slugPlaceholder")}
                required
                pattern="[a-z0-9-]+"
              />
            </div>

            <div className="space-y-2">
              <Label htmlFor="oncall-desc">{t("oncall:form.description")}</Label>
              <Textarea
                id="oncall-desc"
                value={description}
                onChange={(e) => setDescription(e.target.value)}
                placeholder={t("oncall:form.descriptionPlaceholder")}
              />
            </div>

            <div className="space-y-2">
              <Label htmlFor="oncall-tz">{t("oncall:form.timezone")}</Label>
              <Input
                id="oncall-tz"
                value={timezone}
                onChange={(e) => setTimezone(e.target.value)}
                placeholder={t("oncall:form.timezonePlaceholder")}
                required
              />
            </div>

            <div className="grid grid-cols-2 gap-4">
              <div className="space-y-2">
                <Label>{t("oncall:form.rotationType")}</Label>
                <Select
                  value={rotationType}
                  onValueChange={(v) => setRotationType(v as OnCallRotationType)}
                >
                  <SelectTrigger>
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    <SelectItem value="daily">{t("oncall:detail.rotationDaily")}</SelectItem>
                    <SelectItem value="weekly">{t("oncall:detail.rotationWeekly")}</SelectItem>
                  </SelectContent>
                </Select>
              </div>

              <div className="space-y-2">
                <Label htmlFor="oncall-hot">{t("oncall:form.handoffTime")}</Label>
                <Input
                  id="oncall-hot"
                  value={handoffTime}
                  onChange={(e) => setHandoffTime(e.target.value)}
                  placeholder={t("oncall:form.handoffTimePlaceholder")}
                  pattern="[0-2][0-9]:[0-5][0-9]"
                  required
                />
              </div>
            </div>

            {rotationType === "weekly" && (
              <div className="space-y-2">
                <Label>{t("oncall:form.handoffWeekday")}</Label>
                <Select
                  value={String(handoffWeekday)}
                  onValueChange={(v) => setHandoffWeekday(Number(v))}
                >
                  <SelectTrigger>
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    {(["Mon", "Tue", "Wed", "Thu", "Fri", "Sat", "Sun"] as const).map(
                      (day, idx) => (
                        <SelectItem key={day} value={String(idx)}>
                          {t(`oncall:form.weekday${day}`)}
                        </SelectItem>
                      ),
                    )}
                  </SelectContent>
                </Select>
              </div>
            )}

            <div className="space-y-2">
              <Label htmlFor="oncall-start">{t("oncall:form.startAt")}</Label>
              <Input
                id="oncall-start"
                value={startAt}
                onChange={(e) => setStartAt(e.target.value)}
                required
              />
              <p className="text-xs text-muted-foreground">
                {t("oncall:form.startAtHelp")}
              </p>
            </div>

            <div className="space-y-2">
              <Label>{t("oncall:form.userUids")}</Label>
              <p className="text-xs text-muted-foreground">
                {t("oncall:form.userUidsHelp")}
              </p>
              <div className="border rounded p-2 max-h-64 overflow-y-auto">
                {members.length === 0 ? (
                  <p className="text-sm text-muted-foreground py-2">
                    {t("oncall:detail.noUsers")}
                  </p>
                ) : (
                  members.map((m) => (
                    <label
                      key={m.userUid}
                      className="flex items-center gap-2 py-1 cursor-pointer"
                    >
                      <input
                        type="checkbox"
                        checked={selectedUsers.includes(m.userUid)}
                        onChange={() => toggleUser(m.userUid)}
                      />
                      <span className="text-sm">
                        {m.name || m.email} <span className="text-muted-foreground">({m.email})</span>
                      </span>
                    </label>
                  ))
                )}
              </div>
            </div>

            {error && (
              <p className="text-sm text-destructive" role="alert">
                {error}
              </p>
            )}

            <div className="flex gap-2">
              <Button type="submit" disabled={create.isPending}>
                {t("oncall:form.submit")}
              </Button>
              <Button
                type="button"
                variant="outline"
                onClick={() => navigate({ to: "/orgs/$org/on-call", params: { org } })}
              >
                {t("oncall:form.cancel")}
              </Button>
            </div>
          </form>
        </CardContent>
      </Card>
    </div>
  );
}
