import { useEffect, useState } from "react";
import { useNavigate, useParams } from "@tanstack/react-router";
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

const pages = [
  { title: "Dashboard", path: "/orgs/$org", icon: LayoutDashboard },
  { title: "Checks", path: "/orgs/$org/checks", icon: ListChecks },
  { title: "Incidents", path: "/orgs/$org/incidents", icon: AlertTriangle },
  { title: "Events", path: "/orgs/$org/events", icon: Calendar },
  { title: "Status Pages", path: "/orgs/$org/status-pages", icon: Globe },
  { title: "Profile", path: "/orgs/$org/account/profile", icon: User2, group: "Account" },
  { title: "Tokens", path: "/orgs/$org/account/tokens", icon: KeyRound, group: "Account" },
  { title: "Invitations", path: "/orgs/$org/organization/invitations", icon: Mail, group: "Organization" },
  { title: "Settings", path: "/orgs/$org/organization/settings", icon: Settings, group: "Organization" },
];

export function CommandMenu() {
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

  return (
    <Command.Dialog
      open={open}
      onOpenChange={(v) => {
        setOpen(v);
        if (!v) setSearch("");
      }}
      shouldFilter={false}
      label="Search"
      className="fixed inset-0 z-50"
    >
      {/* Overlay */}
      <div
        className="fixed inset-0 bg-black/50"
        onClick={() => setOpen(false)}
      />

      {/* Dialog content */}
      <div className="fixed left-1/2 top-[20%] z-50 w-full max-w-lg -translate-x-1/2 overflow-hidden rounded-lg border bg-popover shadow-lg">
        <Command.Input
          placeholder="Search pages and checks..."
          value={search}
          onValueChange={setSearch}
          className="w-full border-b bg-transparent px-4 py-3 text-sm outline-none placeholder:text-muted-foreground"
        />
        <Command.List className="max-h-72 overflow-y-auto p-2">
          <Command.Empty className="px-4 py-6 text-center text-sm text-muted-foreground">
            No results found.
          </Command.Empty>

          {Object.entries(
            pages
              .filter((p) =>
                !search ||
                p.title.toLowerCase().includes(search.toLowerCase())
              )
              .reduce<Record<string, typeof pages>>((groups, page) => {
                const group = (page as { group?: string }).group || "Pages";
                (groups[group] ??= []).push(page);
                return groups;
              }, {})
          ).map(([group, items]) => (
            <Command.Group
              key={group}
              heading={group}
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
          ))}

          {checks && checks.length > 0 && (
            <Command.Group
              heading="Checks"
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
