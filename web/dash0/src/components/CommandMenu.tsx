import { useCallback, useEffect, useRef, useState } from "react";
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
  BadgeCheck,
  Users,
  Search,
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
  { titleKey: "badges", path: "/orgs/$org/badges", icon: BadgeCheck, group: "pages" },
  { titleKey: "profile", path: "/orgs/$org/account/profile", icon: User2, group: "account" },
  { titleKey: "tokens", path: "/orgs/$org/account/tokens", icon: KeyRound, group: "account" },
  { titleKey: "members", path: "/orgs/$org/organization/members", icon: Users, group: "organization" },
  { titleKey: "invitations", path: "/orgs/$org/organization/invitations", icon: Mail, group: "organization" },
  { titleKey: "settings", path: "/orgs/$org/organization/settings", icon: Settings, group: "organization" },
];

const groupLabelKey: Record<GroupKey, string> = {
  pages: "command.groupPages",
  account: "command.groupAccount",
  organization: "command.groupOrganization",
};

export interface CommandMenuProps {
  open?: boolean;
  onOpenChange?: (open: boolean) => void;
}

export function CommandMenu({ open: controlledOpen, onOpenChange }: CommandMenuProps = {}) {
  const { t } = useTranslation("nav");
  const [uncontrolledOpen, setUncontrolledOpen] = useState(false);
  const isControlled = controlledOpen !== undefined;
  const open = isControlled ? controlledOpen : uncontrolledOpen;
  const openRef = useRef(open);
  openRef.current = open;
  const onOpenChangeRef = useRef(onOpenChange);
  onOpenChangeRef.current = onOpenChange;
  const isControlledRef = useRef(isControlled);
  isControlledRef.current = isControlled;
  const setOpen = useCallback((v: boolean | ((prev: boolean) => boolean)) => {
    const next = typeof v === "function" ? v(openRef.current) : v;
    if (!isControlledRef.current) setUncontrolledOpen(next);
    onOpenChangeRef.current?.(next);
  }, []);
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
        e.stopPropagation();
        setOpen((prev) => !prev);
      }
    }
    // Use capture phase to win against any other listener on the same key
    // (Radix focus traps, sidebar shortcut handler, etc.) and stop propagation
    // so Cmd+K toggles in a single round-trip even if HMR/StrictMode leaks
    // a stale handler.
    document.addEventListener("keydown", onKeyDown, true);
    return () => document.removeEventListener("keydown", onKeyDown, true);
  }, [setOpen]);

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

  // Only mount the dialog when open. Keeps a single source of truth for
  // visibility and prevents stale instances from accumulating across HMR
  // reloads in dev (which manifested as needing multiple Escapes to close).
  if (!open) return null;

  return (
    <Command.Dialog
      open={open}
      onOpenChange={(v) => {
        setOpen(v);
        if (!v) setSearch("");
      }}
      shouldFilter={false}
      label={t("command.searchPlaceholder")}
      overlayClassName="fixed inset-0 z-50 bg-black/50"
      contentClassName="fixed left-1/2 top-[20%] z-50 w-full max-w-lg -translate-x-1/2 overflow-hidden rounded-lg border bg-popover shadow-lg"
      onKeyDown={(e) => {
        if (e.key === "Escape") {
          // Force-close on Escape ourselves rather than relying solely on
          // Radix's internal onEscapeKeyDown — keeps a single source of truth
          // for `open` even if a stale dialog instance is mounted.
          e.preventDefault();
          e.stopPropagation();
          setOpen(false);
          return;
        }
        if (e.key === "Enter") {
          // cmdk no-ops on Enter when no item is selected, leaving the dialog
          // open with no feedback. Close it instead.
          const hasSelection = e.currentTarget.querySelector(
            '[cmdk-item][aria-selected="true"]',
          );
          if (!hasSelection) setOpen(false);
        }
      }}
    >
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
                onSelect={() => goTo(`/orgs/$org/checks/${check.uid}`)}
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
    </Command.Dialog>
  );
}

export interface CommandMenuTriggerProps {
  onOpen: () => void;
}

export function CommandMenuTrigger({ onOpen }: CommandMenuTriggerProps) {
  const { t } = useTranslation("nav");
  return (
    <button
      type="button"
      data-testid="command-menu-trigger"
      onClick={onOpen}
      aria-label={t("command.searchPlaceholder")}
      className="inline-flex items-center gap-2 rounded-md border border-input bg-background px-2 sm:px-3 h-9 text-sm font-medium ring-offset-background transition-colors hover:bg-accent hover:text-accent-foreground"
    >
      <Search className="h-4 w-4" />
      <span className="hidden md:inline text-muted-foreground text-xs">⌘K</span>
    </button>
  );
}
