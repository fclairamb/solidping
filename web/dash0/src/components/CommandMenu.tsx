import { useEffect, useState } from "react";
import { useNavigate, useParams } from "@tanstack/react-router";
import { useTranslation } from "react-i18next";
import { Command } from "cmdk";
import {
  LayoutDashboard,
  ListChecks,
  AlertTriangle,
  Calendar,
  Globe,
  Activity,
  User2,
  KeyRound,
  Mail,
  Settings,
} from "lucide-react";
import { useChecks } from "@/api/hooks";

type GroupKey = "pages" | "account" | "organization";

interface PageEntry {
  titleKey: string;
  path: string;
  icon: typeof LayoutDashboard;
  group: GroupKey;
}

const pages: PageEntry[] = [
  { titleKey: "dashboard", path: "/orgs/$org", icon: LayoutDashboard, group: "pages" },
  { titleKey: "checks", path: "/orgs/$org/checks", icon: ListChecks, group: "pages" },
  { titleKey: "incidents", path: "/orgs/$org/incidents", icon: AlertTriangle, group: "pages" },
  { titleKey: "events", path: "/orgs/$org/events", icon: Calendar, group: "pages" },
  { titleKey: "statusPages", path: "/orgs/$org/status-pages", icon: Globe, group: "pages" },
  { titleKey: "profile", path: "/orgs/$org/account/profile", icon: User2, group: "account" },
  { titleKey: "tokens", path: "/orgs/$org/account/tokens", icon: KeyRound, group: "account" },
  { titleKey: "invitations", path: "/orgs/$org/organization/invitations", icon: Mail, group: "organization" },
  { titleKey: "settings", path: "/orgs/$org/organization/settings", icon: Settings, group: "organization" },
];

const groupLabelKey: Record<GroupKey, string> = {
  pages: "command.groupPages",
  account: "command.groupAccount",
  organization: "command.groupOrganization",
};

export function CommandMenu() {
  const { t } = useTranslation("nav");
  const [open, setOpen] = useState(false);
  const [search, setSearch] = useState("");
  const [debouncedSearch, setDebouncedSearch] = useState("");
  const navigate = useNavigate();
  const params = useParams({ strict: false });
  const org = (params as { org?: string }).org || "";
  const { data: checks } = useChecks(org, {
    q: debouncedSearch || undefined,
    limit: 10,
  });

  useEffect(() => {
    const timer = setTimeout(() => setDebouncedSearch(search), 300);
    return () => clearTimeout(timer);
  }, [search]);

  useEffect(() => {
    function onKeyDown(e: KeyboardEvent) {
      if (e.key === "k" && (e.metaKey || e.ctrlKey)) {
        e.preventDefault();
        setOpen((prev) => !prev);
      }
    }
    document.addEventListener("keydown", onKeyDown);
    return () => document.removeEventListener("keydown", onKeyDown);
  }, []);

  function goTo(path: string) {
    setOpen(false);
    navigate({ to: path, params: { org } });
  }

  const enrichedPages = pages.map((p) => ({ ...p, title: t(p.titleKey) }));
  const filteredPages = enrichedPages.filter((p) =>
    !search || p.title.toLowerCase().includes(search.toLowerCase())
  );

  const groupedPages = filteredPages.reduce<Record<GroupKey, typeof enrichedPages>>(
    (groups, page) => {
      (groups[page.group] ??= []).push(page);
      return groups;
    },
    {} as Record<GroupKey, typeof enrichedPages>
  );

  return (
    <Command.Dialog
      open={open}
      onOpenChange={(v) => {
        setOpen(v);
        if (!v) setSearch("");
      }}
      shouldFilter={false}
      label={t("command.searchPlaceholder")}
      className="fixed inset-0 z-50"
    >
      <div
        className="fixed inset-0 bg-black/50"
        onClick={() => setOpen(false)}
      />

      <div className="fixed left-1/2 top-[20%] z-50 w-full max-w-lg -translate-x-1/2 overflow-hidden rounded-lg border bg-popover shadow-lg">
        <Command.Input
          placeholder={t("command.searchPlaceholder")}
          value={search}
          onValueChange={setSearch}
          className="w-full border-b bg-transparent px-4 py-3 text-sm outline-none placeholder:text-muted-foreground"
        />
        <Command.List className="max-h-72 overflow-y-auto p-2">
          <Command.Empty className="px-4 py-6 text-center text-sm text-muted-foreground">
            {t("command.noResults")}
          </Command.Empty>

          {(Object.entries(groupedPages) as [GroupKey, typeof enrichedPages][]).map(
            ([group, items]) => (
              <Command.Group
                key={group}
                heading={t(groupLabelKey[group])}
                className="[&_[cmdk-group-heading]]:px-2 [&_[cmdk-group-heading]]:py-1.5 [&_[cmdk-group-heading]]:text-xs [&_[cmdk-group-heading]]:font-medium [&_[cmdk-group-heading]]:text-muted-foreground"
              >
                {items.map((page) => (
                  <Command.Item
                    key={page.path}
                    value={`${page.title} ${group}`}
                    onSelect={() => goTo(page.path)}
                    className="flex cursor-pointer items-center gap-2 rounded-md px-2 py-2 text-sm aria-selected:bg-accent aria-selected:text-accent-foreground"
                  >
                    <page.icon className="h-4 w-4 text-muted-foreground" />
                    {page.title}
                  </Command.Item>
                ))}
              </Command.Group>
            ),
          )}

          {checks && checks.length > 0 && (
            <Command.Group
              heading={t("command.groupChecks")}
              className="[&_[cmdk-group-heading]]:px-2 [&_[cmdk-group-heading]]:py-1.5 [&_[cmdk-group-heading]]:text-xs [&_[cmdk-group-heading]]:font-medium [&_[cmdk-group-heading]]:text-muted-foreground"
            >
              {checks.map((check) => (
                <Command.Item
                  key={check.uid}
                  value={`${check.name || ""} ${check.slug || ""}`}
                  onSelect={() =>
                    goTo(`/orgs/$org/checks/${check.uid}`)
                  }
                  className="flex cursor-pointer items-center gap-2 rounded-md px-2 py-2 text-sm aria-selected:bg-accent aria-selected:text-accent-foreground"
                >
                  <Activity className="h-4 w-4 text-muted-foreground" />
                  <span>{check.name || check.slug || check.uid}</span>
                  {check.name && check.slug && (
                    <span className="text-xs text-muted-foreground">
                      {check.slug}
                    </span>
                  )}
                </Command.Item>
              ))}
            </Command.Group>
          )}
        </Command.List>
      </div>
    </Command.Dialog>
  );
}
