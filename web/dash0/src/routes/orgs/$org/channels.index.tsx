import { useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import { createFileRoute, Link, useNavigate } from "@tanstack/react-router";
import { Bell, MoreHorizontal, Plus, RefreshCw, Search, Star } from "lucide-react";
import { toast } from "sonner";

import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Badge } from "@/components/ui/badge";
import { Skeleton } from "@/components/ui/skeleton";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import {
  useConnections,
  useDeleteConnection,
  useUpdateConnection,
  type Connection,
  type ConnectionType,
} from "@/api/hooks";
import { ChannelIcon, channelLabel } from "@/components/channels/channel-icon";

export const Route = createFileRoute("/orgs/$org/channels/")({
  component: ChannelsListPage,
});

function ChannelsListPage() {
  const { t } = useTranslation("channels");
  const { org } = Route.useParams();
  const navigate = useNavigate();
  const {
    data: channels,
    isLoading,
    isRefetching,
    refetch,
  } = useConnections(org);
  const deleteMutation = useDeleteConnection(org);

  const [search, setSearch] = useState("");

  const filtered = useMemo(() => {
    const list = channels || [];
    if (!search.trim()) return list;
    const q = search.toLowerCase();
    return list.filter(
      (c) =>
        c.name.toLowerCase().includes(q) || c.type.toLowerCase().includes(q),
    );
  }, [channels, search]);

  const showEmptyState = !isLoading && channels && channels.length === 0;

  return (
    <div className="space-y-6">
      <div className="flex items-center justify-between">
        <div>
          <h1 className="text-3xl font-bold tracking-tight flex items-center gap-2">
            <Bell className="h-7 w-7 text-muted-foreground" />
            {t("title", "Channels")}
          </h1>
          <p className="text-muted-foreground">
            {t(
              "subtitle",
              "Slack, webhooks, email, and more — wire how SolidPing reaches you.",
            )}
          </p>
        </div>
        <Button asChild>
          <Link to="/orgs/$org/channels/new" params={{ org }}>
            <Plus className="h-4 w-4 mr-1" />
            {t("new", "New channel")}
          </Link>
        </Button>
      </div>

      {showEmptyState ? (
        <EmptyState org={org} />
      ) : (
        <>
          <div className="flex flex-wrap items-center gap-4">
            <div className="relative flex-1 min-w-[200px] max-w-sm">
              <Search className="absolute left-3 top-1/2 -translate-y-1/2 h-4 w-4 text-muted-foreground" />
              <Input
                placeholder={t("searchPlaceholder", "Search by name or type…")}
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
              data-testid="channels-refresh"
            >
              <RefreshCw
                className={`h-4 w-4 ${isRefetching ? "animate-spin" : ""}`}
              />
            </Button>
          </div>

          <div className="rounded-md border">
            {isLoading ? (
              <div className="space-y-2 p-2">
                {[...Array(6)].map((_, i) => (
                  <Skeleton key={i} className="h-12 rounded-lg" />
                ))}
              </div>
            ) : (
              <Table>
                <TableHeader>
                  <TableRow>
                    <TableHead>{t("col.name", "Name")}</TableHead>
                    <TableHead>{t("col.type", "Type")}</TableHead>
                    <TableHead>{t("col.status", "Status")}</TableHead>
                    <TableHead>{t("col.usedBy", "Used by")}</TableHead>
                    <TableHead>{t("col.updated", "Updated")}</TableHead>
                    <TableHead className="w-[60px]" />
                  </TableRow>
                </TableHeader>
                <TableBody>
                  {filtered.map((c) => (
                    <Row
                      key={c.uid}
                      org={org}
                      channel={c}
                      onDelete={() =>
                        deleteMutation.mutate(c.uid, {
                          onSuccess: () =>
                            toast.success(t("deleted", "Channel deleted")),
                          onError: () =>
                            toast.error(t("deleteFailed", "Delete failed")),
                        })
                      }
                      navigate={navigate}
                    />
                  ))}
                </TableBody>
              </Table>
            )}
          </div>
        </>
      )}
    </div>
  );
}

function EmptyState({ org }: { org: string }) {
  const { t } = useTranslation("channels");
  const quick: ConnectionType[] = ["slack", "discord", "email", "webhook"];

  return (
    <Card>
      <CardHeader>
        <CardTitle>{t("empty.title", "No channels yet")}</CardTitle>
        <CardDescription>
          {t(
            "empty.body",
            "Add a channel to start receiving notifications. Pick one to get going:",
          )}
        </CardDescription>
      </CardHeader>
      <CardContent className="flex flex-wrap gap-2">
        {quick.map((type) => (
          <Button key={type} variant="outline" size="sm" asChild>
            <Link
              to="/orgs/$org/channels/new"
              params={{ org }}
              search={{ type }}
            >
              <ChannelIcon type={type} className="h-4 w-4 mr-1" />
              {channelLabel(type)}
            </Link>
          </Button>
        ))}
      </CardContent>
    </Card>
  );
}

interface RowProps {
  org: string;
  channel: Connection;
  onDelete: () => void;
  navigate: ReturnType<typeof useNavigate>;
}

function Row({ org, channel, onDelete, navigate }: RowProps) {
  const { t } = useTranslation("channels");
  const update = useUpdateConnection(org, channel.uid);

  const toggleEnabled = () =>
    update.mutate(
      { enabled: !channel.enabled },
      {
        onSuccess: () =>
          toast.success(
            channel.enabled
              ? t("disabled", "Channel disabled")
              : t("enabled", "Channel enabled"),
          ),
      },
    );

  const toggleDefault = () =>
    update.mutate(
      { isDefault: !channel.isDefault },
      {
        onSuccess: () => toast.success(t("defaultUpdated", "Default updated")),
      },
    );

  return (
    <TableRow>
      <TableCell>
        <Link
          to="/orgs/$org/channels/$connectionUid"
          params={{ org, connectionUid: channel.uid }}
          className="flex items-center gap-2 font-medium hover:underline"
        >
          <ChannelIcon type={channel.type} className="h-4 w-4" />
          {channel.name}
        </Link>
      </TableCell>
      <TableCell>
        <Badge variant="outline">{channelLabel(channel.type)}</Badge>
      </TableCell>
      <TableCell className="text-sm">
        <div className="flex items-center gap-2">
          <Badge variant={channel.enabled ? "default" : "secondary"}>
            {channel.enabled ? t("status.enabled", "Enabled") : t("status.disabled", "Disabled")}
          </Badge>
          {channel.isDefault && (
            <span title={t("default", "Default")}>
              <Star className="h-3 w-3 fill-yellow-500 stroke-yellow-600" />
            </span>
          )}
        </div>
      </TableCell>
      <TableCell className="text-sm text-muted-foreground">—</TableCell>
      <TableCell className="text-sm text-muted-foreground">
        {new Date(channel.updatedAt).toLocaleDateString()}
      </TableCell>
      <TableCell>
        <DropdownMenu>
          <DropdownMenuTrigger asChild>
            <Button variant="ghost" size="icon">
              <MoreHorizontal className="h-4 w-4" />
            </Button>
          </DropdownMenuTrigger>
          <DropdownMenuContent align="end">
            <DropdownMenuItem
              onClick={() =>
                navigate({
                  to: "/orgs/$org/channels/$connectionUid",
                  params: { org, connectionUid: channel.uid },
                })
              }
            >
              {t("actions.edit", "Edit")}
            </DropdownMenuItem>
            <DropdownMenuItem onClick={toggleEnabled}>
              {channel.enabled
                ? t("actions.disable", "Disable")
                : t("actions.enable", "Enable")}
            </DropdownMenuItem>
            <DropdownMenuItem onClick={toggleDefault}>
              {channel.isDefault
                ? t("actions.unsetDefault", "Unset default")
                : t("actions.setDefault", "Set as default")}
            </DropdownMenuItem>
            <DropdownMenuItem
              className="text-destructive"
              onClick={onDelete}
            >
              {t("actions.delete", "Delete")}
            </DropdownMenuItem>
          </DropdownMenuContent>
        </DropdownMenu>
      </TableCell>
    </TableRow>
  );
}
