import { useState } from "react";
import { useTranslation } from "react-i18next";
import { createFileRoute } from "@tanstack/react-router";
import {
  Plus,
  Search,
  RefreshCw,
  Trash2,
  Copy,
  Check,
  KeyRound,
} from "lucide-react";
import { toast } from "sonner";
import {
  useTokens,
  useCreateToken,
  useRevokeToken,
  type TokenInfo,
  type CreateTokenResponse,
} from "@/api/hooks";
import { ApiError } from "@/api/client";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Skeleton } from "@/components/ui/skeleton";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
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
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { QueryErrorView } from "@/components/shared/error-views";
import { Alert, AlertDescription } from "@/components/ui/alert";

export const Route = createFileRoute("/orgs/$org/account/tokens")({
  component: TokensPage,
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

function TokenRow({
  token,
  onRevoke,
}: {
  token: TokenInfo;
  onRevoke: (uid: string) => void;
}) {
  const { t } = useTranslation("account");
  return (
    <TableRow>
      <TableCell className="font-medium">{token.name || t("tokens.unnamed")}</TableCell>
      <TableCell className="text-muted-foreground">
        {formatRelativeTime(token.createdAt)}
      </TableCell>
      <TableCell className="text-muted-foreground">
        {token.lastUsedAt ? formatRelativeTime(token.lastUsedAt) : t("tokens.never")}
      </TableCell>
      <TableCell data-testid="token-expiry" className="text-muted-foreground">
        {formatExpiry(token.expiresAt, t("tokens.never"), t("tokens.expired"))}
      </TableCell>
      <TableCell>
        <Button
          data-testid="token-revoke-button"
          variant="ghost"
          size="icon"
          className="h-8 w-8 text-destructive hover:text-destructive"
          onClick={() => onRevoke(token.uid)}
        >
          <Trash2 className="h-4 w-4" />
        </Button>
      </TableCell>
    </TableRow>
  );
}

const EXPIRY_OPTIONS = [
  { labelKey: "tokens.expiry7days", value: "7" },
  { labelKey: "tokens.expiry30days", value: "30" },
  { labelKey: "tokens.expiry90days", value: "90" },
  { labelKey: "tokens.expiry1year", value: "365" },
  { labelKey: "tokens.expiryNone", value: "none" },
];

function TokensPage() {
  const { t } = useTranslation("account");
  const { t: tc } = useTranslation("common");
  const { org } = Route.useParams();
  const [search, setSearch] = useState("");
  const [revokeUid, setRevokeUid] = useState<string | null>(null);
  const [createOpen, setCreateOpen] = useState(false);
  const [tokenName, setTokenName] = useState("");
  const [expiryDays, setExpiryDays] = useState("90");
  const [createdToken, setCreatedToken] = useState<CreateTokenResponse | null>(
    null
  );
  const [copied, setCopied] = useState(false);

  const {
    data: tokens,
    isLoading,
    error,
    refetch,
    isRefetching,
  } = useTokens(org);

  const createToken = useCreateToken(org);
  const revokeToken = useRevokeToken();

  const filteredTokens =
    tokens?.filter((token) =>
      token.name?.toLowerCase().includes(search.toLowerCase())
    ) ?? [];

  const handleRevoke = async () => {
    if (!revokeUid) return;
    try {
      await revokeToken.mutateAsync(revokeUid);
      toast.success(t("tokens.revoked"));
      setRevokeUid(null);
    } catch (err) {
      toast.error(
        err instanceof ApiError ? err.message : t("tokens.failedRevoke")
      );
    }
  };

  const handleCreate = async () => {
    if (!tokenName.trim()) return;

    const request: { name: string; expiresAt?: string } = {
      name: tokenName.trim(),
    };

    if (expiryDays !== "none") {
      const date = new Date();
      date.setDate(date.getDate() + parseInt(expiryDays));
      request.expiresAt = date.toISOString();
    }

    try {
      const result = await createToken.mutateAsync(request);
      setCreatedToken(result);
    } catch (err) {
      toast.error(
        err instanceof ApiError ? err.message : t("tokens.failedCreate")
      );
    }
  };

  const handleCopy = async (text: string) => {
    await navigator.clipboard.writeText(text);
    setCopied(true);
    setTimeout(() => setCopied(false), 2000);
  };

  const handleCloseCreate = () => {
    setCreateOpen(false);
    setTokenName("");
    setExpiryDays("90");
    setCreatedToken(null);
    setCopied(false);
  };

  return (
    <div className="space-y-6">
      <div className="flex items-center gap-4">
        <div className="relative flex-1 max-w-sm">
          <Search className="absolute left-3 top-1/2 -translate-y-1/2 h-4 w-4 text-muted-foreground" />
          <Input
            placeholder={t("tokens.searchPlaceholder")}
            value={search}
            onChange={(e) => setSearch(e.target.value)}
            className="pl-9"
          />
        </div>
        <Button
          variant="outline"
          size="icon"
          onClick={() => refetch()}
          disabled={isRefetching}
        >
          <RefreshCw
            className={`h-4 w-4 ${isRefetching ? "animate-spin" : ""}`}
          />
        </Button>
        <Button data-testid="new-token-button" onClick={() => setCreateOpen(true)}>
          <Plus className="mr-2 h-4 w-4" />
          {t("tokens.newToken")}
        </Button>
      </div>

      {error ? (
        <QueryErrorView error={error} org={org} onRetry={() => refetch()} />
      ) : isLoading ? (
        <div className="space-y-2">
          {[...Array(4)].map((_, i) => (
            <Skeleton key={i} className="h-12 rounded-lg" />
          ))}
        </div>
      ) : filteredTokens.length > 0 ? (
        <div className="rounded-md border">
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>{tc("name")}</TableHead>
                <TableHead>{t("tokens.createdColumn")}</TableHead>
                <TableHead>{t("tokens.lastUsed")}</TableHead>
                <TableHead>{t("tokens.expiresAt")}</TableHead>
                <TableHead className="w-[50px]" />
              </TableRow>
            </TableHeader>
            <TableBody>
              {filteredTokens.map((token) => (
                <TokenRow
                  key={token.uid}
                  token={token}
                  onRevoke={setRevokeUid}
                />
              ))}
            </TableBody>
          </Table>
        </div>
      ) : tokens && tokens.length > 0 ? (
        <div className="text-center py-12 text-muted-foreground">
          <Search className="h-8 w-8 mx-auto mb-2 opacity-50" />
          <p>{t("tokens.noMatch")}</p>
        </div>
      ) : (
        <div className="text-center py-12 text-muted-foreground">
          <KeyRound className="h-8 w-8 mx-auto mb-2 opacity-50" />
          <p className="mb-2">{t("tokens.noTokens")}</p>
          <Button onClick={() => setCreateOpen(true)}>
            <Plus className="mr-2 h-4 w-4" />
            {t("tokens.createFirst")}
          </Button>
        </div>
      )}

      {/* Revoke confirmation */}
      <AlertDialog open={!!revokeUid} onOpenChange={() => setRevokeUid(null)}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>{t("tokens.revokeTitle")}</AlertDialogTitle>
            <AlertDialogDescription>
              {t("tokens.revokeDescription")}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>{tc("cancel")}</AlertDialogCancel>
            <AlertDialogAction
              data-testid="token-revoke-confirm"
              onClick={handleRevoke}
              className="bg-destructive text-destructive-foreground hover:bg-destructive/90"
            >
              {t("tokens.revoke")}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>

      {/* Create token dialog */}
      <Dialog open={createOpen} onOpenChange={handleCloseCreate}>
        <DialogContent>
          {createdToken ? (
            <>
              <DialogHeader>
                <DialogTitle>{t("tokens.tokenCreatedTitle")}</DialogTitle>
                <DialogDescription>
                  {t("tokens.tokenCreatedDescription")}
                </DialogDescription>
              </DialogHeader>
              <Alert>
                <AlertDescription className="flex items-center gap-2">
                  <code data-testid="token-created-value" className="flex-1 break-all text-sm">
                    {createdToken.token}
                  </code>
                  <Button
                    variant="ghost"
                    size="icon"
                    className="shrink-0"
                    onClick={() => handleCopy(createdToken.token)}
                  >
                    {copied ? (
                      <Check className="h-4 w-4" />
                    ) : (
                      <Copy className="h-4 w-4" />
                    )}
                  </Button>
                </AlertDescription>
              </Alert>
              <DialogFooter>
                <Button data-testid="token-created-done" onClick={handleCloseCreate}>{t("tokens.done")}</Button>
              </DialogFooter>
            </>
          ) : (
            <>
              <DialogHeader>
                <DialogTitle>{t("tokens.createTitle")}</DialogTitle>
                <DialogDescription>
                  {t("tokens.createDescription")}
                </DialogDescription>
              </DialogHeader>
              <div className="space-y-4">
                <div className="space-y-2">
                  <Label htmlFor="token-name">{tc("name")}</Label>
                  <Input
                    id="token-name"
                    data-testid="token-name-input"
                    placeholder={t("tokens.namePlaceholder")}
                    value={tokenName}
                    onChange={(e) => setTokenName(e.target.value)}
                  />
                </div>
                <div className="space-y-2">
                  <Label>{t("tokens.expiration")}</Label>
                  <Select value={expiryDays} onValueChange={setExpiryDays}>
                    <SelectTrigger data-testid="token-expiry-select">
                      <SelectValue />
                    </SelectTrigger>
                    <SelectContent>
                      {EXPIRY_OPTIONS.map((opt) => (
                        <SelectItem key={opt.value} value={opt.value}>
                          {t(opt.labelKey)}
                        </SelectItem>
                      ))}
                    </SelectContent>
                  </Select>
                </div>
              </div>
              <DialogFooter>
                <Button variant="outline" onClick={handleCloseCreate}>
                  {tc("cancel")}
                </Button>
                <Button
                  data-testid="token-create-button"
                  onClick={handleCreate}
                  disabled={!tokenName.trim() || createToken.isPending}
                >
                  {createToken.isPending ? t("tokens.creating") : tc("create")}
                </Button>
              </DialogFooter>
            </>
          )}
        </DialogContent>
      </Dialog>
    </div>
  );
}
