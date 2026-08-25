import { useState, type ReactNode } from "react";
import { useTranslation } from "react-i18next";
import {
  AlertCircle,
  AlertTriangle,
  Check,
  Copy,
  Loader2,
  Mail,
  Plus,
} from "lucide-react";
import { Alert, AlertDescription } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
  DialogTrigger,
} from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { ApiError } from "@/api/client";
import {
  useCreateInvitation,
  type CreateInvitationResponse,
} from "@/api/hooks";

interface CreateInvitationDialogProps {
  org: string;
  trigger?: ReactNode;
  onCreated?: (inviteUrl: string) => void;
}

export function CreateInvitationDialog({
  org,
  trigger,
  onCreated,
}: CreateInvitationDialogProps) {
  const { t } = useTranslation("org");
  const { t: tc } = useTranslation("common");
  const createInvitation = useCreateInvitation(org);

  const [dialogOpen, setDialogOpen] = useState(false);
  const [email, setEmail] = useState("");
  const [role, setRole] = useState("user");
  const [error, setError] = useState<string | null>(null);
  const [result, setResult] = useState<CreateInvitationResponse | null>(null);
  // The recipient email survives independently of the `email` field above,
  // which is cleared on success so a re-opened dialog starts blank — the
  // success state still needs it to render "sent to alice@acme.com".
  const [sentEmail, setSentEmail] = useState("");
  const [copied, setCopied] = useState(false);

  const handleCreate = async (e: React.FormEvent) => {
    e.preventDefault();
    setError(null);
    setResult(null);

    try {
      const created = await createInvitation.mutateAsync({ email, role });
      setResult(created);
      setSentEmail(email);
      setEmail("");
      setRole("user");
      onCreated?.(created.inviteUrl);
    } catch (err) {
      if (err instanceof ApiError) {
        setError(err.message);
      } else {
        setError(t("invitations.unexpectedError"));
      }
    }
  };

  const handleCopy = async (url: string) => {
    await navigator.clipboard.writeText(url);
    setCopied(true);
    setTimeout(() => setCopied(false), 2000);
  };

  const handleDialogClose = () => {
    setDialogOpen(false);
    setError(null);
    setResult(null);
    setSentEmail("");
    setEmail("");
    setRole("user");
  };

  return (
    <Dialog
      open={dialogOpen}
      onOpenChange={(open) => {
        if (!open) handleDialogClose();
        else setDialogOpen(true);
      }}
    >
      <DialogTrigger asChild>
        {trigger ?? (
          <Button>
            <Plus className="mr-2 h-4 w-4" />
            {t("invitations.invite")}
          </Button>
        )}
      </DialogTrigger>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{t("invitations.inviteMember")}</DialogTitle>
          <DialogDescription>
            {t("invitations.sendDescription")}
          </DialogDescription>
        </DialogHeader>

        {result ? (
          <div className="space-y-4">
            {result.emailSent ? (
              <>
                <Alert variant="success">
                  <Mail className="h-4 w-4" />
                  <AlertDescription>
                    {t("invitations.emailSentTo", { email: sentEmail })}
                  </AlertDescription>
                </Alert>
                <div className="space-y-2">
                  <p className="text-sm text-muted-foreground">
                    {t("invitations.shareLinkFallback")}
                  </p>
                  <div className="flex gap-2">
                    <Input
                      value={result.inviteUrl}
                      readOnly
                      className="font-mono text-xs"
                    />
                    <Button
                      variant="outline"
                      size="icon"
                      onClick={() => handleCopy(result.inviteUrl)}
                    >
                      {copied ? (
                        <Check className="h-4 w-4" />
                      ) : (
                        <Copy className="h-4 w-4" />
                      )}
                    </Button>
                  </div>
                </div>
              </>
            ) : (
              <>
                <Alert variant="warning">
                  <AlertTriangle className="h-4 w-4" />
                  <AlertDescription>
                    {t("invitations.emailNotConfigured")}
                  </AlertDescription>
                </Alert>
                <div className="flex gap-2">
                  <Input
                    value={result.inviteUrl}
                    readOnly
                    className="font-mono text-xs"
                  />
                  <Button
                    variant="outline"
                    size="icon"
                    onClick={() => handleCopy(result.inviteUrl)}
                  >
                    {copied ? (
                      <Check className="h-4 w-4" />
                    ) : (
                      <Copy className="h-4 w-4" />
                    )}
                  </Button>
                </div>
              </>
            )}
            <DialogFooter>
              <Button variant="outline" onClick={handleDialogClose}>
                {tc("close")}
              </Button>
            </DialogFooter>
          </div>
        ) : (
          <form onSubmit={handleCreate}>
            <div className="space-y-4">
              {error && (
                <Alert variant="destructive">
                  <AlertCircle className="h-4 w-4" />
                  <AlertDescription>{error}</AlertDescription>
                </Alert>
              )}

              <div className="space-y-2">
                <Label htmlFor="inviteEmail">{tc("email")}</Label>
                <Input
                  id="inviteEmail"
                  type="email"
                  placeholder={t("invitations.emailPlaceholder")}
                  value={email}
                  onChange={(e) => setEmail(e.target.value)}
                  required
                  disabled={createInvitation.isPending}
                />
              </div>

              <div className="space-y-2">
                <Label htmlFor="inviteRole">{t("invitations.role")}</Label>
                <Select value={role} onValueChange={setRole}>
                  <SelectTrigger>
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    <SelectItem value="user">{tc("user")}</SelectItem>
                    <SelectItem value="admin">{tc("administrator")}</SelectItem>
                  </SelectContent>
                </Select>
              </div>
            </div>

            <DialogFooter className="mt-4">
              <Button
                type="button"
                variant="outline"
                onClick={handleDialogClose}
              >
                {tc("cancel")}
              </Button>
              <Button type="submit" disabled={createInvitation.isPending}>
                {createInvitation.isPending ? (
                  <>
                    <Loader2 className="mr-2 h-4 w-4 animate-spin" />
                    {t("invitations.sending")}
                  </>
                ) : (
                  t("invitations.sendInvitation")
                )}
              </Button>
            </DialogFooter>
          </form>
        )}
      </DialogContent>
    </Dialog>
  );
}
