import { useState } from "react";
import { useTranslation } from "react-i18next";
import { createFileRoute, Link } from "@tanstack/react-router";
import { ChevronLeft, ChevronRight, Loader2, Search } from "lucide-react";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import { TimeAgo } from "@/components/ui/time-ago";
import { ApiError } from "@/api/client";
import { PermissionDenied } from "@/components/shared/error-views";
import { useAdminUsersList } from "@/api/hooks";
import type { AdminUserRow } from "@/api/hooks";
import { useDebounce } from "@/lib/use-debounce";

const PAGE_SIZE = 50;

export const Route = createFileRoute("/orgs/$org/server/users")({
  component: UsersListPage,
});

function UsersListPage() {
  const { t } = useTranslation("server");
  const { org } = Route.useParams();
  const [search, setSearch] = useState("");
  const [offset, setOffset] = useState(0);
  const debouncedSearch = useDebounce(search, 300);

  const { data, isLoading, error } = useAdminUsersList({
    q: debouncedSearch,
    limit: PAGE_SIZE,
    offset,
  });

  const rows = data?.data ?? [];
  const total = data?.total ?? 0;
  const rangeStart = total === 0 ? 0 : offset + 1;
  const rangeEnd = Math.min(offset + rows.length, total);

  return (
    <Card>
      <CardHeader>
        <CardTitle>{t("users.title")}</CardTitle>
        <p className="text-sm text-muted-foreground">
          {t("users.description")}
        </p>
      </CardHeader>
      <CardContent className="space-y-4">
        <div className="flex flex-wrap items-center gap-4">
          <div className="relative min-w-[200px] max-w-sm flex-1">
            <Search className="absolute left-3 top-1/2 h-4 w-4 -translate-y-1/2 text-muted-foreground" />
            <Input
              className="pl-9"
              placeholder={t("users.search")}
              value={search}
              onChange={(event) => {
                setSearch(event.target.value);
                setOffset(0);
              }}
              data-testid="users-search"
            />
          </div>
        </div>

        {isLoading ? (
          <div className="flex items-center justify-center py-8">
            <Loader2 className="h-6 w-6 animate-spin text-muted-foreground" />
          </div>
        ) : error instanceof ApiError && error.status === 403 ? (
          // 403 renders Permission Denied in place — never a redirect, which
          // would loop an authenticated-but-unauthorized user.
          <PermissionDenied org={org} />
        ) : error ? (
          <p className="text-sm text-destructive">{t("users.loadError")}</p>
        ) : rows.length === 0 ? (
          <p className="py-6 text-sm text-muted-foreground">
            {t("users.empty")}
          </p>
        ) : (
          <>
            <div className="overflow-x-auto">
              <Table data-testid="users-table">
                <TableHeader>
                  <TableRow>
                    <TableHead>{t("users.columns.email")}</TableHead>
                    <TableHead>{t("users.columns.name")}</TableHead>
                    <TableHead>{t("users.columns.organizations")}</TableHead>
                    <TableHead>{t("users.columns.flags")}</TableHead>
                    <TableHead>{t("users.columns.lastActive")}</TableHead>
                    <TableHead>{t("users.columns.created")}</TableHead>
                  </TableRow>
                </TableHeader>
                <TableBody>
                  {rows.map((row) => (
                    <UserRow key={row.uid} org={org} row={row} />
                  ))}
                </TableBody>
              </Table>
            </div>
            <div className="flex flex-wrap items-center justify-between gap-2 pt-2">
              <p className="text-sm text-muted-foreground">
                {t("users.showing", { start: rangeStart, end: rangeEnd, total })}
              </p>
              <div className="flex items-center gap-2">
                <Button
                  variant="outline"
                  size="sm"
                  disabled={offset === 0}
                  onClick={() => setOffset(Math.max(0, offset - PAGE_SIZE))}
                  data-testid="users-prev-page"
                >
                  <ChevronLeft className="mr-1 h-4 w-4" />
                  {t("users.previous")}
                </Button>
                <Button
                  variant="outline"
                  size="sm"
                  disabled={offset + rows.length >= total}
                  onClick={() => setOffset(offset + PAGE_SIZE)}
                  data-testid="users-next-page"
                >
                  {t("users.next")}
                  <ChevronRight className="ml-1 h-4 w-4" />
                </Button>
              </div>
            </div>
          </>
        )}
      </CardContent>
    </Card>
  );
}

function UserRow({ org, row }: { org: string; row: AdminUserRow }) {
  const { t } = useTranslation("server");

  return (
    <TableRow data-testid={`users-row-${row.uid}`}>
      <TableCell className="font-medium">{row.email}</TableCell>
      <TableCell className="text-sm text-muted-foreground">
        {row.name || "—"}
      </TableCell>
      <TableCell>
        {row.orgs.length === 0 ? (
          <span className="text-sm text-muted-foreground">
            {t("users.noOrgs")}
          </span>
        ) : (
          <div className="flex flex-wrap gap-1">
            {row.orgs.map((membership) => (
              <Link
                key={membership.uid}
                to="/orgs/$org/server/entitlements/$targetOrg"
                params={{ org, targetOrg: membership.slug }}
              >
                <Badge variant="outline" className="hover:bg-accent">
                  {membership.slug} · {membership.role}
                </Badge>
              </Link>
            ))}
          </div>
        )}
      </TableCell>
      <TableCell>
        <div className="flex flex-wrap gap-1">
          {row.superAdmin ? (
            <Badge variant="secondary">{t("users.flags.superAdmin")}</Badge>
          ) : null}
          {row.totpEnabled ? (
            <Badge variant="outline">{t("users.flags.twoFactor")}</Badge>
          ) : null}
          {row.demo ? (
            <Badge variant="outline">{t("users.flags.demo")}</Badge>
          ) : null}
          {!row.emailVerified ? (
            <Badge variant="warning">{t("users.flags.unverified")}</Badge>
          ) : null}
          {row.mustChangePassword ? (
            <Badge variant="warning">
              {t("users.flags.passwordResetPending")}
            </Badge>
          ) : null}
          {!row.hasPassword ? (
            <Badge variant="outline">{t("users.flags.ssoOnly")}</Badge>
          ) : null}
        </div>
      </TableCell>
      <TableCell className="text-sm">
        {row.lastActiveAt ? (
          <TimeAgo date={row.lastActiveAt} />
        ) : (
          <span className="text-muted-foreground">—</span>
        )}
      </TableCell>
      <TableCell className="text-sm">
        <TimeAgo date={row.createdAt} />
      </TableCell>
    </TableRow>
  );
}
