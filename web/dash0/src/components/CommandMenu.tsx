import { useCallback, useEffect, useRef, useState } from "react";
import { useNavigate, useParams } from "@tanstack/react-router";
import { useTranslation } from "react-i18next";
import { Command } from "cmdk";
import {
  LayoutDashboard,
  ListChecks,
  AlertTriangle,
  ArrowUpRight,
  Bell,
  BellRing,
  Bot,
  Calendar,
  CalendarClock,
  FileChartColumn,
  GitBranch,
  Globe,
  Activity,
  User2,
  KeyRound,
  Mail,
  MessageSquare,
  Settings,
  BadgeCheck,
  Users,
  Search,
  PlusCircle,
  Building2,
  CircleUser,
  Target,
  Wrench,
} from "lucide-react";
import { useChecks, useStatusPages, useEscalationPolicies, useSlos } from "@/api/hooks";

type GroupKey = "actions" | "pages" | "account" | "organization";

interface PageEntry {
  titleKey: string;
  descriptionKey?: string;
  path: string;
  icon: typeof LayoutDashboard;
  group: GroupKey;
  testId?: string;
}

const pages: PageEntry[] = [
  {
    titleKey: "command.newCheck",
    descriptionKey: "command.newCheckDescription",
    path: "/orgs/$org/checks/new",
    icon: PlusCircle,
    group: "actions",
    testId: "command-menu-new-check",
  },
  { titleKey: "dashboard", path: "/orgs/$org", icon: LayoutDashboard, group: "pages" },
  { titleKey: "checks", path: "/orgs/$org/checks", icon: ListChecks, group: "pages" },
  { titleKey: "incidents", path: "/orgs/$org/incidents", icon: AlertTriangle, group: "pages" },
  { titleKey: "dependencies", path: "/orgs/$org/dependencies", icon: GitBranch, group: "pages" },
  { titleKey: "onCall", path: "/orgs/$org/on-call", icon: CalendarClock, group: "pages" },
  { titleKey: "escalationPolicies", path: "/orgs/$org/escalation-policies", icon: ArrowUpRight, group: "pages" },
  { titleKey: "events", path: "/orgs/$org/events", icon: Calendar, group: "pages" },
  { titleKey: "integrations", path: "/orgs/$org/integrations", icon: Bell, group: "pages" },
  { titleKey: "statusPages", path: "/orgs/$org/status-pages", icon: Globe, group: "pages" },
  { titleKey: "statusUpdates", path: "/orgs/$org/status-updates", icon: MessageSquare, group: "pages" },
  { titleKey: "maintenanceWindows", path: "/orgs/$org/maintenance-windows", icon: Wrench, group: "pages" },
  { titleKey: "slos", path: "/orgs/$org/slos", icon: Target, group: "pages" },
  { titleKey: "badges", path: "/orgs/$org/badges", icon: BadgeCheck, group: "pages" },
  { titleKey: "myPages", path: "/orgs/$org/me/notifications", icon: BellRing, group: "pages" },
  {
    titleKey: "account",
    path: "/orgs/$org/account",
    icon: CircleUser,
    group: "account",
    testId: "command-menu-account",
  },
  { titleKey: "profile", path: "/orgs/$org/account/profile", icon: User2, group: "account" },
  { titleKey: "tokens", path: "/orgs/$org/account/tokens", icon: KeyRound, group: "account" },
  {
    titleKey: "ai",
    descriptionKey: "command.aiDescription",
    path: "/orgs/$org/account/mcp",
    icon: Bot,
    group: "account",
    testId: "command-menu-ai",
  },
  {
    titleKey: "organization",
    path: "/orgs/$org/organization",
    icon: Building2,
    group: "organization",
    testId: "command-menu-organization",
  },
  { titleKey: "members", path: "/orgs/$org/organization/members", icon: Users, group: "organization" },
  { titleKey: "invitations", path: "/orgs/$org/organization/invitations", icon: Mail, group: "organization" },
  {
    titleKey: "reportSchedules",
    path: "/orgs/$org/organization/report-schedules",
    icon: FileChartColumn,
    group: "organization",
    testId: "command-menu-report-schedules",
  },
  { titleKey: "settings", path: "/orgs/$org/organization/settings", icon: Settings, group: "organization" },
];

const groupOrder: GroupKey[] = ["actions", "pages", "account", "organization"];

const groupLabelKey: Record<GroupKey, string> = {
  actions: "command.groupActions",
  pages: "command.groupPages",
  account: "command.groupAccount",
  organization: "command.groupOrganization",
};

// Shared styling for the result groups rendered outside the static `pages`
// list (Checks, Status pages, Escalation policies, SLOs) — kept as constants
// since each entity group repeats the same group-heading and item markup.
const groupHeadingClassName =
  "[&_[cmdk-group-heading]]:px-2 [&_[cmdk-group-heading]]:py-1.5 [&_[cmdk-group-heading]]:text-xs [&_[cmdk-group-heading]]:font-medium [&_[cmdk-group-heading]]:text-muted-foreground";
const commandItemClassName =
  "flex cursor-pointer items-center gap-2 rounded-md px-2 py-2 text-sm aria-selected:bg-accent aria-selected:text-accent-foreground";

/** Case-insensitive substring match against any of the given fields. */
function matchesSearch(term: string, ...fields: (string | undefined)[]): boolean {
  const needle = term.toLowerCase();
  return fields.some((f) => !!f && f.toLowerCase().includes(needle));
}

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

  // Status pages, escalation policies and SLOs have no server-side `q`
  // support, so we fetch the (small, per-org-capped) full list and filter
  // client-side. Gate these queries on the palette being open AND the
  // search text being non-empty so opening ⌘K alone never fans out three
  // extra requests.
  const entitySearchEnabled = open && debouncedSearch.length > 0;
  const { data: statusPages } = useStatusPages(org, { enabled: entitySearchEnabled });
  const { data: escalationPolicies } = useEscalationPolicies(org, { enabled: entitySearchEnabled });
  const { data: slos } = useSlos(org, { enabled: entitySearchEnabled });

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

  const enrichedPages = pages.map((p) => ({
    ...p,
    title: t(p.titleKey),
    description: p.descriptionKey ? t(p.descriptionKey) : undefined,
  }));
  const filteredPages = enrichedPages.filter((p) => {
    if (!search) return true;
    const haystack = `${p.title} ${p.description ?? ""}`.toLowerCase();
    return haystack.includes(search.toLowerCase());
  });

  const groupedPages = filteredPages.reduce<Record<GroupKey, typeof enrichedPages>>(
    (groups, page) => {
      (groups[page.group] ??= []).push(page);
      return groups;
    },
    {} as Record<GroupKey, typeof enrichedPages>
  );

  const orderedGroupedPages = groupOrder
    .filter((g) => groupedPages[g] && groupedPages[g].length > 0)
    .map((g) => [g, groupedPages[g]] as const);

  const ENTITY_GROUP_LIMIT = 5;
  const filteredStatusPages = debouncedSearch
    ? (statusPages ?? [])
        .filter((p) => matchesSearch(debouncedSearch, p.name, p.slug))
        .slice(0, ENTITY_GROUP_LIMIT)
    : [];
  const filteredEscalationPolicies = debouncedSearch
    ? (escalationPolicies ?? [])
        .filter((p) => matchesSearch(debouncedSearch, p.name))
        .slice(0, ENTITY_GROUP_LIMIT)
    : [];
  const filteredSlos = debouncedSearch
    ? (slos ?? [])
        .filter((s) => matchesSearch(debouncedSearch, s.name, s.slug))
        .slice(0, ENTITY_GROUP_LIMIT)
    : [];

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

        {orderedGroupedPages.map(([group, items]) => (
          <Command.Group
            key={group}
            heading={t(groupLabelKey[group])}
            className={groupHeadingClassName}
          >
            {items.map((page) => (
              <Command.Item
                key={page.path}
                value={`${page.title} ${page.description ?? ""} ${group}`}
                onSelect={() => goTo(page.path)}
                data-testid={page.testId}
                className={commandItemClassName}
              >
                <page.icon className="h-4 w-4 text-muted-foreground" />
                <span>{page.title}</span>
                {page.description && (
                  <span className="text-xs text-muted-foreground">
                    {page.description}
                  </span>
                )}
              </Command.Item>
            ))}
          </Command.Group>
        ))}

        {checks && checks.length > 0 && (
          <Command.Group heading={t("command.groupChecks")} className={groupHeadingClassName}>
            {checks.map((check) => (
              <Command.Item
                key={check.uid}
                value={`${check.name || ""} ${check.slug || ""}`}
                onSelect={() => goTo(`/orgs/$org/checks/${check.uid}`)}
                className={commandItemClassName}
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

        {filteredStatusPages.length > 0 && (
          <Command.Group heading={t("command.groupStatusPages")} className={groupHeadingClassName}>
            {filteredStatusPages.map((page) => (
              <Command.Item
                key={page.uid}
                value={`${page.name} ${page.slug} statusPage`}
                onSelect={() => goTo(`/orgs/$org/status-pages/${page.uid}`)}
                className={commandItemClassName}
              >
                <Globe className="h-4 w-4 text-muted-foreground" />
                <span>{page.name || page.slug}</span>
                {page.name && page.slug && (
                  <span className="text-xs text-muted-foreground">{page.slug}</span>
                )}
              </Command.Item>
            ))}
          </Command.Group>
        )}

        {filteredEscalationPolicies.length > 0 && (
          <Command.Group
            heading={t("command.groupEscalationPolicies")}
            className={groupHeadingClassName}
          >
            {filteredEscalationPolicies.map((policy) => (
              <Command.Item
                key={policy.uid}
                value={`${policy.name} escalationPolicy`}
                onSelect={() => goTo(`/orgs/$org/escalation-policies/${policy.uid}`)}
                className={commandItemClassName}
              >
                <ArrowUpRight className="h-4 w-4 text-muted-foreground" />
                <span>{policy.name}</span>
              </Command.Item>
            ))}
          </Command.Group>
        )}

        {filteredSlos.length > 0 && (
          <Command.Group heading={t("command.groupSlos")} className={groupHeadingClassName}>
            {filteredSlos.map((slo) => (
              <Command.Item
                key={slo.uid}
                value={`${slo.name} ${slo.slug} slo`}
                onSelect={() => goTo(`/orgs/$org/slos/${slo.uid}`)}
                className={commandItemClassName}
              >
                <Target className="h-4 w-4 text-muted-foreground" />
                <span>{slo.name || slo.slug}</span>
                {slo.name && slo.slug && (
                  <span className="text-xs text-muted-foreground">{slo.slug}</span>
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
      className="inline-flex items-center gap-2 rounded-md border border-input bg-control px-2 sm:px-3 h-9 text-sm font-medium ring-offset-background transition-colors hover:bg-accent hover:text-accent-foreground"
    >
      <Search className="h-4 w-4" />
      <span className="hidden md:inline text-muted-foreground text-xs">⌘K</span>
    </button>
  );
}
