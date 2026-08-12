import { useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import { createFileRoute } from "@tanstack/react-router";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Badge } from "@/components/ui/badge";
import { Alert, AlertDescription } from "@/components/ui/alert";
import { Switch } from "@/components/ui/switch";
import {
  AlertCircle,
  Mail,
  MessageCircle,
  MessageSquare,
  MonitorSmartphone,
  Phone,
  ShieldCheck,
  Trash2,
  Send,
  Loader2,
  X,
} from "lucide-react";
import { toast } from "sonner";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { WebPushEnableButton } from "@/components/notifications/WebPushEnableButton";
import { deriveDeviceLabel } from "@/lib/browser-detection";
import {
  useNotificationRoutes,
  useCreateNotificationContact,
  useDeleteNotificationContact,
  usePatchNotificationRoute,
  useTestNotificationRoute,
  useVerifyContact,
  useConfirmVerifyContact,
  useCreateTelegramLink,
  useIntegrations,
  type NotificationRoute,
  type SlackSuggestion,
} from "@/api/hooks";
import { useTelegramEnabled, useWhatsAppEnabled } from "@/api/public-config";
import {
  DirectChannelIcon,
  directChannelLabel,
} from "@/components/integrations/integration-icon";

export const Route = createFileRoute("/orgs/$org/account/notifications")({
  component: NotificationsPage,
});

// Icons and labels for the direct channels come from the shared
// integration-icon module, so one file stays the answer to "what does channel X
// look like" across the dashboard.
function contactTypeIcon(type: string) {
  return <DirectChannelIcon type={type} className="h-4 w-4" />;
}

const contactTypeLabel = directChannelLabel;

/**
 * Code round-trip dialog shared by the phone (SMS) and WhatsApp contact types.
 * Only the copy differs: an SMS text vs a WhatsApp authentication template, so
 * the strings are keyed off `contactType` rather than duplicating the dialog.
 */
function VerifyPhoneDialog({
  org,
  contactUid,
  contactType,
  phone,
  open,
  onOpenChange,
  onVerified,
}: {
  org: string;
  contactUid: string;
  contactType: string;
  phone: string;
  open: boolean;
  onOpenChange: (open: boolean) => void;
  onVerified: () => void;
}) {
  const { t } = useTranslation("account");
  const verify = useVerifyContact(org);
  const confirm = useConfirmVerifyContact(org);
  const [code, setCode] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [sent, setSent] = useState(false);
  const isWhatsApp = contactType === "whatsapp";
  const copyPrefix = isWhatsApp ? "notifications.whatsapp.verify" : "notifications.verify";

  const sendCode = async () => {
    setError(null);
    try {
      await verify.mutateAsync(contactUid);
      setSent(true);
      toast.success(t("notifications.verify.codeSent"));
    } catch (err: unknown) {
      setError(err instanceof Error ? err.message : t("notifications.verify.sendFailed"));
    }
  };

  const handleConfirm = async (e: React.FormEvent) => {
    e.preventDefault();
    setError(null);
    try {
      await confirm.mutateAsync({ contactUid, code: code.trim() });
      toast.success(t("notifications.verify.verified"));
      onVerified();
      onOpenChange(false);
      setCode("");
      setSent(false);
    } catch (err: unknown) {
      setError(err instanceof Error ? err.message : t("notifications.verify.incorrectCode"));
    }
  };

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent data-testid="verify-phone-dialog">
        <DialogHeader>
          <DialogTitle>{t(`${copyPrefix}.dialogTitle`)}</DialogTitle>
          <DialogDescription>
            {t(`${copyPrefix}.dialogDescription`, { phone })}
          </DialogDescription>
        </DialogHeader>

        {error && (
          <Alert variant="destructive">
            <AlertCircle className="h-4 w-4" />
            <AlertDescription>{error}</AlertDescription>
          </Alert>
        )}

        {!sent ? (
          <Button
            onClick={sendCode}
            disabled={verify.isPending}
            data-testid="verify-send-code"
          >
            {verify.isPending ? (
              <Loader2 className="h-4 w-4 animate-spin mr-2" />
            ) : (
              <Send className="h-4 w-4 mr-2" />
            )}
            {t("notifications.verify.sendCode")}
          </Button>
        ) : (
          <form onSubmit={handleConfirm} className="space-y-3">
            <Input
              inputMode="numeric"
              maxLength={6}
              placeholder="000000"
              className="font-mono"
              value={code}
              onChange={(e) => setCode(e.target.value.replace(/\D/g, ""))}
              data-testid="verify-code-input"
            />
            <DialogFooter className="gap-2 sm:gap-0">
              <Button
                type="button"
                variant="outline"
                onClick={sendCode}
                disabled={verify.isPending}
                data-testid="verify-resend-code"
              >
                {t("notifications.verify.resend")}
              </Button>
              <Button
                type="submit"
                disabled={confirm.isPending || code.length !== 6}
                data-testid="verify-confirm"
              >
                {confirm.isPending ? (
                  <Loader2 className="h-4 w-4 animate-spin" />
                ) : (
                  t("notifications.verify.confirm")
                )}
              </Button>
            </DialogFooter>
          </form>
        )}
      </DialogContent>
    </Dialog>
  );
}

/** How often the pending-connect poller re-reads the contacts list, in ms. */
const TELEGRAM_POLL_INTERVAL_MS = 3000;

/** Formats a remaining duration in ms as m:ss, floored at 0:00. */
function formatCountdown(remainingMs: number): string {
  const total = Math.max(0, Math.floor(remainingMs / 1000));
  const minutes = Math.floor(total / 60);
  const seconds = total % 60;

  return `${minutes}:${seconds.toString().padStart(2, "0")}`;
}

/**
 * The Telegram connect affordance — a button, not an input, because there is
 * nothing for the user to type: the contact is created by the webhook when they
 * press Start in Telegram, never from anything typed here.
 *
 * While a link is outstanding it shows a live expiry countdown and keeps
 * polling the contacts list, so the new contact appears on its own the moment
 * the user presses Start. Once the link lapses the only offer is a fresh one —
 * an expired link that still looks clickable is worse than no link.
 */
function TelegramConnect({
  org,
  connected,
  reconnect,
  onPoll,
}: {
  org: string;
  connected: boolean;
  reconnect?: boolean;
  onPoll: () => void;
}) {
  const { t } = useTranslation("account");
  const createLink = useCreateTelegramLink(org);
  const [link, setLink] = useState<{
    url: string;
    expiresAt: number;
    /** Whether a Telegram contact was already connected when this link was minted. */
    wasConnected: boolean;
  } | null>(null);
  const [now, setNow] = useState(() => Date.now());
  const [error, setError] = useState<string | null>(null);

  // "Connected while we were waiting" is DERIVED, not stored: comparing against
  // the state captured when the link was minted means no effect has to write
  // state back, which is both simpler and avoids a cascading render.
  const justConnected = link !== null && connected && !link.wasConnected;
  const pending = link !== null && !justConnected;
  const remaining = link ? link.expiresAt - now : 0;
  const expired = pending && remaining <= 0;

  // Tick the countdown and poll the contacts list while a link is outstanding.
  useEffect(() => {
    if (!pending || expired) return;

    const tick = setInterval(() => setNow(Date.now()), 1000);
    const poll = setInterval(onPoll, TELEGRAM_POLL_INTERVAL_MS);

    return () => {
      clearInterval(tick);
      clearInterval(poll);
    };
  }, [pending, expired, onPoll]);

  // The contact showed up: say so once. No state is written here — `pending`
  // already went false the moment `justConnected` did.
  useEffect(() => {
    if (justConnected) {
      toast.success(t("notifications.telegram.connected"));
    }
  }, [justConnected, t]);

  const start = async () => {
    setError(null);
    try {
      const resp = await createLink.mutateAsync();
      const expiresAt = new Date(resp.expiresAt).getTime();
      setNow(Date.now());
      setLink({ url: resp.url, expiresAt, wasConnected: connected });
      window.open(resp.url, "_blank", "noopener,noreferrer");
    } catch (err: unknown) {
      setError(err instanceof Error ? err.message : t("notifications.telegram.linkFailed"));
    }
  };

  return (
    <div className="space-y-2">
      {error && (
        <Alert variant="destructive">
          <AlertCircle className="h-4 w-4" />
          <AlertDescription>{error}</AlertDescription>
        </Alert>
      )}

      {!pending && (
        <Button
          type="button"
          variant={reconnect ? "outline" : "default"}
          size="sm"
          onClick={start}
          disabled={createLink.isPending}
          data-testid="telegram-connect-button"
        >
          {createLink.isPending ? (
            <Loader2 className="h-4 w-4 animate-spin mr-2" />
          ) : (
            <Send className="h-4 w-4 mr-2" />
          )}
          {reconnect
            ? t("notifications.telegram.reconnectButton")
            : t("notifications.telegram.connectButton")}
        </Button>
      )}

      {pending && !expired && (
        <Alert data-testid="telegram-connect-pending">
          <Loader2 className="h-4 w-4 animate-spin" />
          <AlertDescription className="flex flex-wrap items-center gap-2">
            <span className="flex-1 min-w-0">
              {t("notifications.telegram.waiting", { countdown: formatCountdown(remaining) })}
            </span>
            <Button asChild variant="outline" size="sm">
              <a href={link.url} target="_blank" rel="noopener noreferrer">
                {t("notifications.telegram.openAgain")}
              </a>
            </Button>
            <Button variant="ghost" size="sm" onClick={() => setLink(null)}>
              {t("notifications.telegram.cancel")}
            </Button>
          </AlertDescription>
        </Alert>
      )}

      {expired && (
        <Alert data-testid="telegram-connect-expired">
          <AlertCircle className="h-4 w-4" />
          <AlertDescription className="flex flex-wrap items-center gap-2">
            <span className="flex-1 min-w-0">{t("notifications.telegram.expired")}</span>
            <Button
              variant="outline"
              size="sm"
              onClick={start}
              disabled={createLink.isPending}
              data-testid="telegram-connect-regenerate"
            >
              {t("notifications.telegram.regenerate")}
            </Button>
          </AlertDescription>
        </Alert>
      )}
    </div>
  );
}

function RouteRow({
  route,
  org,
  onTestSent,
}: {
  route: NotificationRoute;
  org: string;
  onTestSent: () => void;
}) {
  const { t } = useTranslation("account");
  const deleteContact = useDeleteNotificationContact(org);
  const patchRoute = usePatchNotificationRoute(org);
  const testRoute = useTestNotificationRoute(org);

  const [testPending, setTestPending] = useState(false);
  const [verifyOpen, setVerifyOpen] = useState(false);

  const handleToggle = async (enabled: boolean) => {
    try {
      await patchRoute.mutateAsync({ routeUid: route.uid, patch: { enabled } });
    } catch {
      toast.error("Failed to update notification route");
    }
  };

  const handleDelete = async () => {
    try {
      await deleteContact.mutateAsync(route.contact.uid);
      toast.success("Notification contact removed");
    } catch {
      toast.error("Failed to remove notification contact");
    }
  };

  const handleTest = async () => {
    setTestPending(true);
    try {
      await testRoute.mutateAsync(route.uid);
      toast.success("Test notification sent");
      onTestSent();
    } catch (err: unknown) {
      const msg =
        err instanceof Error ? err.message : "Failed to send test notification";
      toast.error(msg);
    } finally {
      setTestPending(false);
    }
  };

  // Both phone (SMS) and WhatsApp contacts go through the same code
  // round-trip before they may be paged, so the row affordances key off
  // "needs verification" rather than the phone type specifically.
  const needsVerification =
    route.contact.type === "phone" || route.contact.type === "whatsapp";
  const isVerified = !!route.contact.verifiedAt;

  // Telegram has no code round-trip: pressing Start in Telegram is the
  // verification. An UNverified telegram contact therefore means something took
  // the connection away — almost always the user blocking the bot — so it reads
  // as "reconnect needed" rather than as a generic unverified contact.
  const isTelegram = route.contact.type === "telegram";

  // A route is testable once its contact is ready to be paged. Types with a
  // setup round-trip (code verification for phone/WhatsApp, pressing Start for
  // Telegram) are testable only once verified; every other type — including
  // any future one — gets the Test button by default rather than by being
  // remembered here. The backend applies the same readiness rule.
  const canTest = (!needsVerification && !isTelegram) || isVerified;

  return (
    <div className="flex items-center gap-3 py-3 border-b last:border-0">
      <div className="flex-none text-muted-foreground">
        {contactTypeIcon(route.contact.type)}
      </div>
      <div className="flex-1 min-w-0">
        <div className="font-medium text-sm">
          {contactTypeLabel(route.contact.type)}
        </div>
        <div className="text-sm text-muted-foreground truncate">
          {route.contact.type === "webpush"
            ? (route.contact.label || "Browser")
            : isTelegram
              ? // A bare numeric chat id is unreadable; the label is the
                // Telegram @username captured when the chat was connected.
                route.contact.label || route.contact.value
              : route.contact.value}
        </div>
        {isTelegram && isVerified && (
          <Badge
            variant="outline"
            className="mt-1 text-xs text-green-600 border-green-400"
            data-testid={`telegram-connected-${route.contact.uid}`}
          >
            <ShieldCheck className="h-3 w-3 mr-1" /> {t("notifications.telegram.connectedBadge")}
          </Badge>
        )}
        {isTelegram && !isVerified && (
          <Badge
            variant="outline"
            className="mt-1 text-xs text-yellow-600 border-yellow-400"
            data-testid={`telegram-reconnect-${route.contact.uid}`}
          >
            {t("notifications.telegram.reconnectBadge")}
          </Badge>
        )}
        {needsVerification && isVerified && (
          <Badge
            variant="outline"
            className="mt-1 text-xs text-green-600 border-green-400"
            data-testid={`phone-verified-${route.contact.uid}`}
          >
            <ShieldCheck className="h-3 w-3 mr-1" /> {t("notifications.verifiedBadge")}
          </Badge>
        )}
        {needsVerification && !isVerified && (
          <Badge
            variant="outline"
            className="mt-1 text-xs text-yellow-600 border-yellow-400"
          >
            {route.contact.type === "whatsapp"
              ? t("notifications.whatsapp.unverifiedBadge")
              : t("notifications.unverifiedBadge")}
          </Badge>
        )}
      </div>
      <div className="flex items-center gap-2 flex-none">
        {needsVerification && !isVerified && (
          <Button
            variant="outline"
            size="sm"
            onClick={() => setVerifyOpen(true)}
            data-testid={`verify-phone-${route.contact.uid}`}
          >
            {t("notifications.verifyButton")}
          </Button>
        )}
        {needsVerification && (
          <VerifyPhoneDialog
            org={org}
            contactUid={route.contact.uid}
            contactType={route.contact.type}
            phone={route.contact.value}
            open={verifyOpen}
            onOpenChange={setVerifyOpen}
            onVerified={onTestSent}
          />
        )}
        {isTelegram && !isVerified && (
          <TelegramConnect org={org} connected={isVerified} reconnect onPoll={onTestSent} />
        )}
        {canTest && (
          <Button
            variant="ghost"
            size="sm"
            onClick={handleTest}
            disabled={testPending || !route.enabled}
            title="Send test notification"
            aria-label="Send test notification"
            data-testid={`test-route-${route.uid}`}
          >
            {testPending ? (
              <Loader2 className="h-4 w-4 animate-spin" />
            ) : (
              <Send className="h-4 w-4" />
            )}
          </Button>
        )}
        <Switch
          checked={route.enabled}
          onCheckedChange={handleToggle}
          disabled={patchRoute.isPending}
          data-testid={`toggle-route-${route.uid}`}
          aria-label={`Toggle ${contactTypeLabel(route.contact.type)} notifications`}
        />
        <Button
          variant="ghost"
          size="sm"
          className="text-destructive hover:text-destructive"
          onClick={handleDelete}
          disabled={deleteContact.isPending}
          data-testid={`delete-contact-${route.contact.uid}`}
        >
          <Trash2 className="h-4 w-4" />
        </Button>
      </div>
    </div>
  );
}

/**
 * First-class "Connect Slack" row (spec 2026-08-12-03, phase 2).
 *
 * Slack DMs used to be reachable only through a post-sign-in banner that a
 * single stray click dismissed forever. It now sits permanently alongside the
 * other methods, and names the workspace it would connect to — "Slack" alone
 * is ambiguous the moment an org has more than one.
 */
function SlackConnectRow({
  org,
  suggestion,
  connectedWorkspace,
}: {
  org: string;
  suggestion?: SlackSuggestion;
  connectedWorkspace?: string;
}) {
  const { t } = useTranslation("account");
  const createContact = useCreateNotificationContact(org);

  const handleAdd = async () => {
    if (!suggestion) return;

    try {
      await createContact.mutateAsync({
        type: "slack_user",
        value: suggestion.slackUserId,
        label: `Slack DM (${suggestion.workspaceName})`,
      });
      toast.success(t("notifications.slack.added", "Slack DM notifications added"));
    } catch {
      toast.error(t("notifications.slack.addFailed", "Failed to add Slack DM contact"));
    }
  };

  const connected = Boolean(connectedWorkspace);

  return (
    <div
      className="flex flex-col gap-2 border-t py-3 sm:flex-row sm:items-center sm:justify-between"
      data-testid="slack-connect-row"
    >
      <div className="flex items-start gap-3 min-w-0">
        <MessageSquare className="h-4 w-4 mt-0.5 flex-none" />
        <div className="min-w-0">
          <p className="text-sm font-medium">
            {t("notifications.slack.title", "Slack direct messages")}
          </p>
          <p className="text-xs text-muted-foreground">
            {connected
              ? t("notifications.slack.connected", {
                  defaultValue: "Connected to {{workspace}}",
                  workspace: connectedWorkspace,
                })
              : suggestion
                ? t("notifications.slack.available", {
                    defaultValue: "Available on {{workspace}}",
                    workspace: suggestion.workspaceName,
                  })
                : t(
                    "notifications.slack.unavailable",
                    "Sign in with Slack, and have an admin connect a Slack workspace, to receive alerts as direct messages.",
                  )}
          </p>
        </div>
      </div>
      {!connected && suggestion && (
        <Button
          size="sm"
          onClick={handleAdd}
          disabled={createContact.isPending}
          data-testid="slack-connect-button"
        >
          {createContact.isPending ? (
            <Loader2 className="h-4 w-4 animate-spin mr-2" />
          ) : (
            <MessageSquare className="h-4 w-4 mr-2" />
          )}
          {t("notifications.slack.connectButton", "Connect Slack")}
        </Button>
      )}
    </div>
  );
}

function SlackBanner({
  suggestion,
  org,
  onDismiss,
}: {
  suggestion: SlackSuggestion;
  org: string;
  onDismiss: () => void;
}) {
  const createContact = useCreateNotificationContact(org);

  const handleAdd = async () => {
    try {
      await createContact.mutateAsync({
        type: "slack_user",
        value: suggestion.slackUserId,
        label: `Slack DM (${suggestion.workspaceName})`,
      });
      toast.success("Slack DM notifications added");
      onDismiss();
    } catch {
      toast.error("Failed to add Slack DM contact");
    }
  };

  return (
    <Alert className="mb-4 flex items-center gap-3">
      <MessageSquare className="h-4 w-4 flex-none" />
      <AlertDescription className="flex-1">
        You signed in with Slack ({suggestion.workspaceName}).{" "}
        <strong>Add Slack DM notifications</strong> to receive incident alerts
        directly in Slack.
      </AlertDescription>
      <div className="flex items-center gap-2 flex-none">
        <Button
          size="sm"
          onClick={handleAdd}
          disabled={createContact.isPending}
          data-testid="add-slack-dm-button"
        >
          {createContact.isPending ? (
            <Loader2 className="h-4 w-4 animate-spin mr-2" />
          ) : (
            <MessageSquare className="h-4 w-4 mr-2" />
          )}
          Add Slack DM
        </Button>
        <Button variant="ghost" size="sm" onClick={onDismiss}>
          <X className="h-4 w-4" />
        </Button>
      </div>
    </Alert>
  );
}

/** E.164, mirroring the backend's whatsapp.ValidE164 regexp. */
const E164 = /^\+[1-9]\d{6,14}$/;

function AddContactForm({
  org,
  smsAvailable,
  whatsAppAvailable,
  telegramAvailable,
  telegramConnected,
  onPoll,
  onSuccess,
}: {
  org: string;
  smsAvailable: boolean;
  whatsAppAvailable: boolean;
  telegramAvailable: boolean;
  telegramConnected: boolean;
  onPoll: () => void;
  onSuccess: () => void;
}) {
  const { t } = useTranslation("account");
  const [type, setType] = useState<"email" | "phone" | "whatsapp">("email");
  const [value, setValue] = useState("");
  const [error, setError] = useState<string | null>(null);
  const createContact = useCreateNotificationContact(org);

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault();
    setError(null);

    if (!value.trim()) {
      setError(type === "email" ? "Email address is required" : "Phone number is required");
      return;
    }

    // The backend rejects non-E.164 WhatsApp numbers; catching it here gives a
    // useful message instead of a round-trip error.
    if (type === "whatsapp" && !E164.test(value.trim())) {
      setError(t("notifications.whatsapp.invalidNumber"));
      return;
    }

    try {
      await createContact.mutateAsync({ type, value: value.trim() });
      setValue("");
      toast.success("Notification contact added");
      onSuccess();
    } catch (err: unknown) {
      setError(
        err instanceof Error ? err.message : "Failed to add contact"
      );
    }
  };

  const handleWebPushSubscription = async (subscriptionJson: string) => {
    try {
      await createContact.mutateAsync({
        type: "webpush",
        value: subscriptionJson,
        label: deriveDeviceLabel(),
      });
      toast.success("Browser push notifications enabled");
      onSuccess();
    } catch (err: unknown) {
      // 409 CONFLICT means the browser is already subscribed — silently ignore.
      const msg = err instanceof Error ? err.message : "";
      if (!msg.includes("409") && !msg.toLowerCase().includes("conflict")) {
        toast.error("Failed to enable browser push notifications");
      } else {
        onSuccess();
      }
    }
  };

  return (
    <div className="space-y-4 pt-4 border-t mt-4">
      <form onSubmit={handleSubmit} className="space-y-3">
        <div className="flex items-center gap-2">
          <Button
            type="button"
            variant={type === "email" ? "default" : "outline"}
            size="sm"
            onClick={() => setType("email")}
          >
            <Mail className="h-3 w-3 mr-1" /> Email
          </Button>
          <Button
            type="button"
            variant={type === "phone" ? "default" : "outline"}
            size="sm"
            onClick={() => setType("phone")}
            data-testid="add-contact-type-phone"
          >
            <Phone className="h-3 w-3 mr-1" /> Phone
          </Button>
          {whatsAppAvailable && (
            <Button
              type="button"
              variant={type === "whatsapp" ? "default" : "outline"}
              size="sm"
              onClick={() => setType("whatsapp")}
              data-testid="add-contact-type-whatsapp"
            >
              <MessageCircle className="h-3 w-3 mr-1" /> WhatsApp
            </Button>
          )}
        </div>

        {type === "whatsapp" && (
          <p className="text-xs text-muted-foreground">
            {t("notifications.whatsapp.afterAddHint")}
          </p>
        )}

        {type === "phone" && !smsAvailable && (
          <Alert>
            <AlertCircle className="h-4 w-4" />
            <AlertDescription>
              {t("notifications.noProviderHint")}
            </AlertDescription>
          </Alert>
        )}

        {type === "phone" && smsAvailable && (
          <p className="text-xs text-muted-foreground">
            {t("notifications.phoneAfterAddHint")}
          </p>
        )}

        {error && (
          <Alert variant="destructive">
            <AlertCircle className="h-4 w-4" />
            <AlertDescription>{error}</AlertDescription>
          </Alert>
        )}

        <div className="flex gap-2">
          <Input
            type={type === "email" ? "email" : "tel"}
            placeholder={
              type === "email" ? "you@example.com" : "+1 555 123 4567"
            }
            aria-label={
              type === "email"
                ? "Email address"
                : type === "whatsapp"
                  ? "WhatsApp number"
                  : "Phone number"
            }
            value={value}
            onChange={(e) => setValue(e.target.value)}
            className="flex-1"
            data-testid="add-contact-input"
          />
          <Button
            type="submit"
            disabled={createContact.isPending}
            data-testid="add-contact-submit"
          >
            {createContact.isPending ? (
              <Loader2 className="h-4 w-4 animate-spin" />
            ) : (
              "Add"
            )}
          </Button>
        </div>
      </form>

      {telegramAvailable && (
        <div className="flex flex-wrap items-center gap-3 pt-2 border-t">
          <Send className="h-4 w-4 text-muted-foreground flex-none" />
          <div className="flex-1 min-w-[12rem]">
            <p className="text-sm font-medium">{t("notifications.telegram.addTitle")}</p>
            <p className="text-xs text-muted-foreground">
              {t("notifications.telegram.addHint")}
            </p>
          </div>
          <TelegramConnect org={org} connected={telegramConnected} onPoll={onPoll} />
        </div>
      )}

      <div className="flex flex-wrap items-center gap-3 pt-2 border-t">
        <MonitorSmartphone className="h-4 w-4 text-muted-foreground flex-none" />
        <div className="flex-1 min-w-[12rem]">
          <p className="text-sm font-medium">Add browser</p>
          <p className="text-xs text-muted-foreground">
            Receive notifications as browser push alerts on this device.
          </p>
        </div>
        <WebPushEnableButton
          org={org}
          onSubscription={handleWebPushSubscription}
          data-testid="add-browser-push-button"
        />
      </div>
    </div>
  );
}

function NotificationsPage() {
  const { org } = Route.useParams();
  const [dismissedSlack, setDismissedSlack] = useState(false);
  const [showAddForm, setShowAddForm] = useState(false);

  const { data, isLoading, isError, refetch } = useNotificationRoutes(org);
  const { data: integrations } = useIntegrations(org);
  // WhatsApp is an *instance*-level capability (one WABA for the whole
  // deployment), unlike SMS which is a per-org Twilio connection — hence the
  // public-config flag rather than the org integrations list.
  const whatsAppAvailable = useWhatsAppEnabled();
  // Telegram is instance-level too: one bot for the whole deployment, so the
  // capability comes from the public config rather than the org's integrations.
  const telegramAvailable = useTelegramEnabled();

  const routes = data?.data ?? [];
  const slackSuggestion = !dismissedSlack ? data?.slackSuggestion : undefined;
  // SMS/voice is available once the org has an enabled Twilio integration.
  const smsAvailable = (integrations ?? []).some(
    (i) => i.type === "twilio" && i.enabled,
  );
  // A *verified* telegram contact is what "connected" means: a contact whose
  // verification was cleared by a block still needs reconnecting.
  const telegramConnected = routes.some(
    (route: NotificationRoute) =>
      route.contact.type === "telegram" && !!route.contact.verifiedAt,
  );
  // A Slack DM contact already exists → the row reports the workspace instead
  // of offering to connect it. The label carries the workspace name written at
  // connect time; the live suggestion is the fallback for older rows.
  const slackRoute = routes.find(
    (route: NotificationRoute) => route.contact.type === "slack_user",
  );
  const connectedSlackWorkspace = slackRoute
    ? slackRoute.contact.label || data?.slackSuggestion?.workspaceName || "Slack"
    : undefined;

  if (isLoading) {
    return (
      <Card>
        <CardContent className="py-8 text-center text-muted-foreground">
          <Loader2 className="h-6 w-6 animate-spin mx-auto mb-2" />
          Loading notification settings…
        </CardContent>
      </Card>
    );
  }

  if (isError) {
    return (
      <Alert variant="destructive">
        <AlertCircle className="h-4 w-4" />
        <AlertDescription>
          Failed to load notification settings. Please refresh the page.
        </AlertDescription>
      </Alert>
    );
  }

  return (
    <div className="space-y-4">
      {slackSuggestion && (
        <SlackBanner
          suggestion={slackSuggestion}
          org={org}
          onDismiss={() => setDismissedSlack(true)}
        />
      )}

      <Card>
        <CardHeader>
          <CardTitle>Notification methods</CardTitle>
          <CardDescription>
            Configure how you receive incident alerts. All enabled methods fire
            when an escalation policy targets you.
          </CardDescription>
        </CardHeader>
        <CardContent>
          {routes.length === 0 ? (
            <p className="text-sm text-muted-foreground py-4 text-center">
              No notification methods configured. Add one below.
            </p>
          ) : (
            <div data-testid="notification-routes-list">
              {routes.map((route: NotificationRoute) => (
                <RouteRow
                  key={route.uid}
                  route={route}
                  org={org}
                  onTestSent={() => refetch()}
                />
              ))}
            </div>
          )}

          <SlackConnectRow
            org={org}
            suggestion={data?.slackSuggestion}
            connectedWorkspace={connectedSlackWorkspace}
          />

          {showAddForm ? (
            <AddContactForm
              org={org}
              smsAvailable={smsAvailable}
              whatsAppAvailable={whatsAppAvailable}
              telegramAvailable={telegramAvailable}
              telegramConnected={telegramConnected}
              onPoll={() => refetch()}
              onSuccess={() => setShowAddForm(false)}
            />
          ) : (
            <Button
              variant="outline"
              size="sm"
              className="mt-4"
              onClick={() => setShowAddForm(true)}
              data-testid="add-contact-button"
            >
              + Add method
            </Button>
          )}
        </CardContent>
      </Card>
    </div>
  );
}
