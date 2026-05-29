import { useState, useMemo } from "react";
import { useTranslation } from "react-i18next";
import { Loader2, Search, Wifi, WifiOff } from "lucide-react";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { Alert, AlertDescription } from "@/components/ui/alert";
import { cn } from "@/lib/utils";
import {
  useFreeboxLanHosts,
  type Integration,
  type FreeboxLanHost,
} from "@/api/hooks";

// FreeboxLanDiscoveryProps lists the input wiring the picker needs.
// onSelect is called with the chosen host so the parent (the check
// form) can pre-fill its name/host fields.
interface FreeboxLanDiscoveryProps {
  org: string;
  open: boolean;
  onOpenChange: (open: boolean) => void;
  // Granted Freebox connections available on the org. The picker
  // renders a connection selector when more than one is supplied.
  channels: Integration[];
  onSelect: (host: FreeboxLanHost) => void;
}

// FreeboxLanDiscovery is the "Discover from Freebox" modal. It queries
// the LAN-browser endpoint for the selected Freebox channel and lets
// the operator single-select a host to pre-fill an ICMP check with.
export function FreeboxLanDiscovery({
  org,
  open,
  onOpenChange,
  channels,
  onSelect,
}: FreeboxLanDiscoveryProps) {
  const { t } = useTranslation("checks");

  const [connectionUid, setConnectionUid] = useState<string>(() =>
    channels[0]?.uid ?? "",
  );
  const [selectedId, setSelectedId] = useState<string | null>(null);
  const [search, setSearch] = useState("");

  const {
    data: hosts,
    isLoading,
    isError,
    error,
    refetch,
  } = useFreeboxLanHosts(org, connectionUid, open && !!connectionUid);

  const filtered = useMemo(() => {
    if (!hosts) return [];
    const q = search.trim().toLowerCase();
    if (!q) return hosts;
    return hosts.filter(
      (h) =>
        h.name.toLowerCase().includes(q) ||
        h.ip.toLowerCase().includes(q) ||
        h.hostType.toLowerCase().includes(q),
    );
  }, [hosts, search]);

  const selected = useMemo(
    () => filtered.find((h) => h.id === selectedId) ?? null,
    [filtered, selectedId],
  );

  function handleConfirm() {
    if (selected) {
      onSelect(selected);
      onOpenChange(false);
    }
  }

  function hostTypeLabel(hostType: string): string {
    // i18n keys live in checks.freebox.hostType.<host_type>; missing
    // keys fall back to the type string itself rather than the spec
    // default so unknown types from a future Freebox release are at
    // least informative.
    return t(`freebox.hostType.${hostType}`, { defaultValue: hostType });
  }

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent
        className="max-w-xl"
        data-testid="freebox-discover-dialog"
      >
        <DialogHeader>
          <DialogTitle>{t("freebox.discoverTitle")}</DialogTitle>
          <DialogDescription>
            {t("freebox.discoverDescription")}
          </DialogDescription>
        </DialogHeader>

        {channels.length > 1 && (
          <div className="space-y-1">
            <Select value={connectionUid} onValueChange={(v) => {
              setConnectionUid(v);
              setSelectedId(null);
            }}>
              <SelectTrigger data-testid="freebox-discover-connection-select">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {channels.map((c) => (
                  <SelectItem key={c.uid} value={c.uid}>
                    {c.name}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </div>
        )}

        <div className="relative">
          <Search className="absolute left-2 top-2.5 h-4 w-4 text-muted-foreground" />
          <Input
            value={search}
            onChange={(e) => setSearch(e.target.value)}
            placeholder={t("freebox.searchPlaceholder")}
            className="pl-8"
            data-testid="freebox-discover-search-input"
          />
        </div>

        <div
          className="max-h-72 overflow-y-auto rounded-md border"
          data-testid="freebox-discover-host-list"
        >
          {isLoading && (
            <div className="flex items-center justify-center p-6 text-sm text-muted-foreground">
              <Loader2 className="mr-2 h-4 w-4 animate-spin" />
              {t("freebox.loading")}
            </div>
          )}
          {isError && (
            <Alert variant="destructive" className="m-3">
              <AlertDescription>
                {(error as Error)?.message ?? t("freebox.error")}{" "}
                <button
                  type="button"
                  className="ml-2 underline"
                  onClick={() => refetch()}
                >
                  {t("freebox.retry")}
                </button>
              </AlertDescription>
            </Alert>
          )}
          {!isLoading && !isError && filtered.length === 0 && (
            <p className="p-6 text-center text-sm text-muted-foreground">
              {hosts && hosts.length === 0
                ? t("freebox.noHosts")
                : t("freebox.noMatches")}
            </p>
          )}
          {!isLoading && !isError && filtered.length > 0 && (
            <ul className="divide-y">
              {filtered.map((host) => {
                const isSelected = host.id === selectedId;
                return (
                  <li key={host.id}>
                    <button
                      type="button"
                      onClick={() => setSelectedId(host.id)}
                      className={cn(
                        "flex w-full items-center justify-between gap-3 px-3 py-2 text-left text-sm hover:bg-muted",
                        isSelected && "bg-muted",
                      )}
                      data-testid={`freebox-discover-host-${host.id}`}
                    >
                      <div className="flex items-center gap-2">
                        <input
                          type="radio"
                          className="h-4 w-4"
                          checked={isSelected}
                          onChange={() => setSelectedId(host.id)}
                          aria-label={host.name}
                        />
                        <div>
                          <div className="font-medium">{host.name}</div>
                          <div className="text-xs text-muted-foreground">
                            {host.ip} · {hostTypeLabel(host.hostType)}
                          </div>
                        </div>
                      </div>
                      {host.reachable ? (
                        <Wifi
                          className="h-4 w-4 text-green-500"
                          aria-label={t("freebox.reachable")}
                        />
                      ) : (
                        <WifiOff
                          className="h-4 w-4 text-muted-foreground"
                          aria-label={t("freebox.unreachable")}
                        />
                      )}
                    </button>
                  </li>
                );
              })}
            </ul>
          )}
        </div>

        <DialogFooter>
          <Button
            type="button"
            variant="outline"
            onClick={() => onOpenChange(false)}
          >
            {t("freebox.cancel")}
          </Button>
          <Button
            type="button"
            onClick={handleConfirm}
            disabled={!selected}
            data-testid="freebox-discover-confirm-button"
          >
            {t("freebox.useSelected")}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
