import type { StatusUpdatePublicResponse } from "@/api/hooks";
import { StatusUpdateCard } from "./status-update-card";

interface IncidentThread {
  incidentUid: string;
  updates: StatusUpdatePublicResponse[];
  latestPublishedAt: string;
}

interface TimelineEntry {
  type: "thread" | "standalone";
  latestPublishedAt: string;
  thread?: IncidentThread;
  update?: StatusUpdatePublicResponse;
}

function buildTimeline(updates: StatusUpdatePublicResponse[]): TimelineEntry[] {
  // Separate incident-linked updates from standalone updates
  const threadMap = new Map<string, StatusUpdatePublicResponse[]>();
  const standalones: StatusUpdatePublicResponse[] = [];

  for (const u of updates) {
    if (u.incidentUid) {
      const existing = threadMap.get(u.incidentUid) ?? [];
      existing.push(u);
      threadMap.set(u.incidentUid, existing);
    } else {
      standalones.push(u);
    }
  }

  const entries: TimelineEntry[] = [];

  // Build thread entries (sort updates within thread ascending = natural reading order)
  for (const [incidentUid, threadUpdates] of threadMap.entries()) {
    const sorted = [...threadUpdates].sort(
      (a, b) =>
        new Date(a.publishedAt).getTime() - new Date(b.publishedAt).getTime(),
    );
    const latestPublishedAt = sorted[sorted.length - 1].publishedAt;
    entries.push({
      type: "thread",
      latestPublishedAt,
      thread: { incidentUid, updates: sorted, latestPublishedAt },
    });
  }

  // Add standalone entries
  for (const u of standalones) {
    entries.push({
      type: "standalone",
      latestPublishedAt: u.publishedAt,
      update: u,
    });
  }

  // Sort entries by latest publishedAt descending (most recent first)
  entries.sort(
    (a, b) =>
      new Date(b.latestPublishedAt).getTime() -
      new Date(a.latestPublishedAt).getTime(),
  );

  return entries;
}

interface StatusUpdatesTimelineProps {
  updates: StatusUpdatePublicResponse[];
}

export function StatusUpdatesTimeline({ updates }: StatusUpdatesTimelineProps) {
  const entries = buildTimeline(updates);

  return (
    <div className="space-y-4">
      {entries.map((entry) => {
        if (entry.type === "standalone" && entry.update) {
          return (
            <StatusUpdateCard key={entry.update.uid} update={entry.update} />
          );
        }

        if (entry.type === "thread" && entry.thread) {
          const { incidentUid, updates: threadUpdates } = entry.thread;
          return (
            <section
              key={incidentUid}
              aria-label={`Incident thread ${incidentUid}`}
              className="rounded-lg border border-border bg-card overflow-hidden"
            >
              <div className="px-4 py-2 border-b border-border bg-muted/30">
                <span className="text-xs font-medium text-muted-foreground">
                  Incident thread
                </span>
              </div>
              <div className="divide-y divide-border">
                {threadUpdates.map((u) => (
                  <div key={u.uid} className="p-4">
                    <StatusUpdateCard update={u} />
                  </div>
                ))}
              </div>
            </section>
          );
        }

        return null;
      })}
    </div>
  );
}
