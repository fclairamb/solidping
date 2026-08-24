import { useState } from "react";
import { useTranslation } from "react-i18next";
import { createFileRoute } from "@tanstack/react-router";
import { Monitor, Smartphone, Tablet, Trash2, LogOut } from "lucide-react";
import { toast } from "sonner";
import {
  useSessions,
  useRevokeSession,
  useSignOutOtherSessions,
  type SessionInfo,
} from "@/api/hooks";
import { ApiError } from "@/api/client";
import { useAuth } from "@/contexts/AuthContext";
import { parseUserAgent } from "@/lib/user-agent";
import { methodLabel } from "@/lib/auth-method-label";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { Card, CardContent } from "@/components/ui/card";
import { Skeleton } from "@/components/ui/skeleton";
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@/components/ui/alert-dialog";
import { QueryErrorView } from "@/components/shared/error-views";

export const Route = createFileRoute("/orgs/$org/account/sessions")({
  component: SessionsPage,
});

function formatRelativeTime(dateStr: string): string {
  const date = new Date(dateStr);
  const now = new Date();
  const diffMs = now.getTime() - date.getTime();
  const diffSec = Math.floor(diffMs / 1000);
  const diffMin = Math.floor(diffSec / 60);
  const diffHour = Math.floor(diffMin / 60);
  const diffDay = Math.floor(diffHour / 24);

  if (diffDay > 30) return date.toLocaleDateString();
  if (diffDay > 0) return `${diffDay}d ago`;
  if (diffHour > 0) return `${diffHour}h ago`;
  if (diffMin > 0) return `${diffMin}m ago`;
  return "just now";
}

function formatExpiry(dateStr: string | undefined, never: string, expired: string): string {
  if (!dateStr) return never;
  const date = new Date(dateStr);
  const now = new Date();
  if (date < now) return expired;
  return date.toLocaleDateString();
}

function DeviceIcon({ device, className }: { device: "mobile" | "tablet" | "desktop"; className?: string }) {
  if (device === "mobile") return <Smartphone className={className} />;
  if (device === "tablet") return <Tablet className={className} />;
  return <Monitor className={className} />;
}

function SessionRow({
  session,
  onRevoke,
}: {
  session: SessionInfo;
  onRevoke: (session: SessionInfo) => void;
}) {
  const { t } = useTranslation("account");
  const parsed = parseUserAgent(session.createdWith?.userAgent);
  const method = methodLabel(session.createdWith?.method, t);
  const browserLabel = [parsed.browser, parsed.browserVersion].filter(Boolean).join(" ");
  const deviceLabel =
    [browserLabel || null, parsed.os].filter(Boolean).join(" on ") || t("sessions.unknownDevice");

  return (
    <Card
      data-testid={`session-row-${session.uid}`}
      className={session.isCurrent ? "border-primary" : undefined}
    >
      <CardContent className="flex flex-col gap-3 p-4 sm:flex-row sm:items-start sm:justify-between">
        <div className="flex gap-3">
          <DeviceIcon device={parsed.device} className="mt-0.5 h-5 w-5 shrink-0 text-muted-foreground" />
          <div className="space-y-1">
            <div className="flex flex-wrap items-center gap-2">
              <span className="font-medium">{deviceLabel}</span>
              {session.isCurrent && (
                <Badge data-testid="session-current-badge" className="border-primary">
                  {t("sessions.currentSession")}
                </Badge>
              )}
              {method && <Badge variant="secondary">{method}</Badge>}
            </div>
            <div className="text-xs text-muted-foreground font-mono break-all">
              {session.createdWith?.userAgent || t("sessions.unknownAgent")}
            </div>
            <div className="text-xs text-muted-foreground space-y-0.5">
              <div>{t("sessions.connected", { time: formatRelativeTime(session.createdAt) })}</div>
              <div>
                {t("sessions.lastActive", {
                  time: session.lastActiveAt
                    ? formatRelativeTime(session.lastActiveAt)
                    : formatRelativeTime(session.createdAt),
                })}
              </div>
              <div>
                {t("sessions.expiresAt", {
                  time: formatExpiry(session.expiresAt, t("sessions.never"), t("sessions.expired")),
                })}
              </div>
              {session.createdWith?.remoteAddr && (
                <div>
                  {t("sessions.ipAddress")}: {session.createdWith.remoteAddr}
                </div>
              )}
            </div>
          </div>
        </div>
        <Button
          data-testid={`session-revoke-button-${session.uid}`}
          variant="ghost"
          size="icon"
          className="h-8 w-8 shrink-0 text-destructive hover:text-destructive"
          onClick={() => onRevoke(session)}
          aria-label={t("sessions.revoke")}
        >
          <Trash2 className="h-4 w-4" />
        </Button>
      </CardContent>
    </Card>
  );
}

function SessionsPage() {
  const { t } = useTranslation("account");
  const { org } = Route.useParams();
  const { logout } = useAuth();
  const [revokeSession, setRevokeSession] = useState<SessionInfo | null>(null);
  const [signOutOthersOpen, setSignOutOthersOpen] = useState(false);

  const { data: sessions, isLoading, error, refetch } = useSessions(org);
  const revokeSessionMutation = useRevokeSession(org);
  const signOutOthersMutation = useSignOutOtherSessions(org);

  const sorted = [...(sessions ?? [])].sort((a, b) => {
    if (a.isCurrent !== b.isCurrent) return a.isCurrent ? -1 : 1;
    const aTime = a.lastActiveAt ?? a.createdAt;
    const bTime = b.lastActiveAt ?? b.createdAt;
    return new Date(bTime).getTime() - new Date(aTime).getTime();
  });

  const otherSessionsCount = sorted.filter((s) => !s.isCurrent).length;

  const handleRevoke = async () => {
    if (!revokeSession) return;
    const wasCurrent = revokeSession.isCurrent;
    try {
      await revokeSessionMutation.mutateAsync(revokeSession.uid);
      setRevokeSession(null);
      if (wasCurrent) {
        // Revoking the current session's own row behaves as logout — the
        // access token it issued is now dead, so signing out client-side
        // matches server reality instead of leaving a doomed session
        // pretending to be logged in until its next 401.
        await logout();
        return;
      }
      toast.success(t("sessions.revoked"));
    } catch (err) {
      toast.error(err instanceof ApiError ? err.message : t("sessions.failedRevoke"));
    }
  };

  const handleSignOutOthers = async () => {
    try {
      const resp = await signOutOthersMutation.mutateAsync();
      setSignOutOthersOpen(false);
      toast.success(t("sessions.signOutOthersSuccess", { count: resp.tokensDeleted }));
    } catch (err) {
      toast.error(err instanceof ApiError ? err.message : t("sessions.failedSignOutOthers"));
    }
  };

  return (
    <div className="space-y-6">
      <div className="flex items-center justify-between gap-4">
        <div>
          <h2 className="text-lg font-semibold">{t("sessions.title")}</h2>
          <p className="text-sm text-muted-foreground">{t("sessions.subtitle")}</p>
        </div>
        <Button
          data-testid="sign-out-others-button"
          variant="destructive"
          disabled={otherSessionsCount === 0}
          onClick={() => setSignOutOthersOpen(true)}
        >
          <LogOut className="mr-2 h-4 w-4" />
          <span className="hidden sm:inline">{t("sessions.signOutOthers")}</span>
        </Button>
      </div>

      {error ? (
        <QueryErrorView error={error} org={org} onRetry={() => refetch()} />
      ) : isLoading ? (
        <div className="space-y-2">
          {[...Array(2)].map((_, i) => (
            <Skeleton key={i} className="h-24 rounded-lg" />
          ))}
        </div>
      ) : sorted.length > 0 ? (
        <div className="space-y-3">
          {sorted.map((session) => (
            <SessionRow key={session.uid} session={session} onRevoke={setRevokeSession} />
          ))}
        </div>
      ) : (
        <div className="text-center py-12 text-muted-foreground">
          <p>{t("sessions.noSessions")}</p>
        </div>
      )}

      <AlertDialog open={!!revokeSession} onOpenChange={() => setRevokeSession(null)}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>{t("sessions.revokeTitle")}</AlertDialogTitle>
            <AlertDialogDescription>
              {revokeSession?.isCurrent
                ? t("sessions.revokeCurrentDescription")
                : t("sessions.revokeDescription")}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>{t("cancel", { ns: "common" })}</AlertDialogCancel>
            <AlertDialogAction
              data-testid="session-revoke-confirm"
              onClick={handleRevoke}
              className="bg-destructive text-destructive-foreground hover:bg-destructive/90"
            >
              {t("sessions.revoke")}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>

      <AlertDialog open={signOutOthersOpen} onOpenChange={setSignOutOthersOpen}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>{t("sessions.signOutOthersTitle")}</AlertDialogTitle>
            <AlertDialogDescription>
              {t("sessions.signOutOthersDescription", { count: otherSessionsCount })}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>{t("cancel", { ns: "common" })}</AlertDialogCancel>
            <AlertDialogAction
              data-testid="sign-out-others-confirm"
              onClick={handleSignOutOthers}
              className="bg-destructive text-destructive-foreground hover:bg-destructive/90"
            >
              {t("sessions.signOutOthersConfirm")}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </div>
  );
}
