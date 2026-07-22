import { useState } from "react";
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
  BellRing,
  Mail,
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
  useIntegrations,
  type NotificationRoute,
  type SlackSuggestion,
} from "@/api/hooks";

export const Route = createFileRoute("/orgs/$org/account/notifications")({
  component: NotificationsPage,
});

function contactTypeIcon(type: string) {
  switch (type) {
    case "email":
      return <Mail className="h-4 w-4" />;
    case "phone":
      return <Phone className="h-4 w-4" />;
    case "slack_user":
      return <MessageSquare className="h-4 w-4" />;
    case "webpush":
      return <MonitorSmartphone className="h-4 w-4" />;
    default:
      return <BellRing className="h-4 w-4" />;
  }
}

function contactTypeLabel(type: string) {
  switch (type) {
    case "email":
      return "Email";
    case "phone":
      return "Phone (SMS)";
    case "slack_user":
      return "Slack DM";
    case "webpush":
      return "Browser push";
    default:
      return type;
  }
}

function VerifyPhoneDialog({
  org,
  contactUid,
  phone,
  open,
  onOpenChange,
  onVerified,
}: {
  org: string;
  contactUid: string;
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
          <DialogTitle>{t("notifications.verify.dialogTitle")}</DialogTitle>
          <DialogDescription>
            {t("notifications.verify.dialogDescription", { phone })}
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

  const isPhone = route.contact.type === "phone";
  const isVerified = !!route.contact.verifiedAt;

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
            : route.contact.value}
        </div>
        {isPhone && isVerified && (
          <Badge
            variant="outline"
            className="mt-1 text-xs text-green-600 border-green-400"
            data-testid={`phone-verified-${route.contact.uid}`}
          >
            <ShieldCheck className="h-3 w-3 mr-1" /> {t("notifications.verifiedBadge")}
          </Badge>
        )}
        {isPhone && !isVerified && (
          <Badge
            variant="outline"
            className="mt-1 text-xs text-yellow-600 border-yellow-400"
          >
            {t("notifications.unverifiedBadge")}
          </Badge>
        )}
      </div>
      <div className="flex items-center gap-2 flex-none">
        {isPhone && !isVerified && (
          <Button
            variant="outline"
            size="sm"
            onClick={() => setVerifyOpen(true)}
            data-testid={`verify-phone-${route.contact.uid}`}
          >
            {t("notifications.verifyButton")}
          </Button>
        )}
        {isPhone && (
          <VerifyPhoneDialog
            org={org}
            contactUid={route.contact.uid}
            phone={route.contact.value}
            open={verifyOpen}
            onOpenChange={setVerifyOpen}
            onVerified={onTestSent}
          />
        )}
        {!isPhone && (
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

function AddContactForm({
  org,
  smsAvailable,
  onSuccess,
}: {
  org: string;
  smsAvailable: boolean;
  onSuccess: () => void;
}) {
  const { t } = useTranslation("account");
  const [type, setType] = useState<"email" | "phone">("email");
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
        </div>

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

      <div className="flex items-center gap-3 pt-2 border-t">
        <MonitorSmartphone className="h-4 w-4 text-muted-foreground flex-none" />
        <div className="flex-1">
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

  const routes = data?.data ?? [];
  const slackSuggestion = !dismissedSlack ? data?.slackSuggestion : undefined;
  // SMS/voice is available once the org has an enabled Twilio integration.
  const smsAvailable = (integrations ?? []).some(
    (i) => i.type === "twilio" && i.enabled,
  );

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

          {showAddForm ? (
            <AddContactForm
              org={org}
              smsAvailable={smsAvailable}
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
