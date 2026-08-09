import { Link, useLocation, useNavigate, useParams } from "@tanstack/react-router";
import {
  ArrowUpRight,
  Bell,
  BellRing,
  Bug,
  LayoutDashboard,
  ListChecks,
  AlertTriangle,
  Calendar,
  CalendarClock,
  GitBranch,
  Globe,
  BadgeCheck,
  LogOut,
  Network,
  Palette,
  ChevronUp,
  User2,
  Building,
  Server,
  MessageSquare,
  Workflow,
  Wrench,
} from "lucide-react";
import {
  Sidebar,
  SidebarContent,
  SidebarFooter,
  SidebarGroup,
  SidebarGroupContent,
  SidebarHeader,
  SidebarMenu,
  SidebarMenuButton,
  SidebarMenuItem,
} from "@/components/ui/sidebar";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import { useAuth } from "@/contexts/AuthContext";
import { useVersion } from "@/api/hooks";
import { useTranslation } from "react-i18next";
import { LanguageSwitcher } from "@/components/shared/language-switcher";
import { Logo } from "@/components/ui/logo";
import { ThemeToggle } from "@/components/ui/theme-toggle";
import { LiveStatusDot } from "@/components/layout/live-status-dot";

const navItems = [
  {
    titleKey: "dashboard",
    path: "/orgs/$org" as const,
    icon: LayoutDashboard,
  },
  {
    titleKey: "checks",
    path: "/orgs/$org/checks" as const,
    icon: ListChecks,
  },
  {
    titleKey: "incidents",
    path: "/orgs/$org/incidents" as const,
    icon: AlertTriangle,
  },
  {
    titleKey: "dependencies",
    path: "/orgs/$org/dependencies" as const,
    icon: GitBranch,
  },
  {
    titleKey: "onCall",
    path: "/orgs/$org/on-call" as const,
    icon: CalendarClock,
  },
  {
    titleKey: "escalationPolicies",
    path: "/orgs/$org/escalation-policies" as const,
    icon: ArrowUpRight,
  },
  {
    titleKey: "events",
    path: "/orgs/$org/events" as const,
    icon: Calendar,
  },
  {
    titleKey: "integrations",
    path: "/orgs/$org/integrations" as const,
    icon: Bell,
  },
  {
    titleKey: "statusPages",
    path: "/orgs/$org/status-pages" as const,
    icon: Globe,
  },
  {
    titleKey: "statusUpdates",
    path: "/orgs/$org/status-updates" as const,
    icon: MessageSquare,
  },
  {
    titleKey: "maintenanceWindows",
    path: "/orgs/$org/maintenance-windows" as const,
    icon: Wrench,
  },
  {
    titleKey: "badges",
    path: "/orgs/$org/badges" as const,
    icon: BadgeCheck,
  },
  {
    titleKey: "myPages",
    path: "/orgs/$org/me/notifications" as const,
    icon: BellRing,
  },
];

const testNavItems = [
  {
    titleKey: "testTools",
    path: "/orgs/$org/test" as const,
    icon: Bug,
  },
  {
    titleKey: "designReference",
    path: "/orgs/$org/design-reference" as const,
    icon: Palette,
  },
];


export function AppSidebar() {
  const { t } = useTranslation();
  const { t: tNav } = useTranslation("nav");
  const { user, logout, organizations, switchOrg } = useAuth();
  const location = useLocation();
  const navigate = useNavigate();
  const params = useParams({ strict: false });
  const org = (params as { org?: string }).org || "test";
  const { data: versionData } = useVersion();
  const isTestMode = versionData?.runMode === "test";

  const currentOrg = organizations.find((o) => o.slug === org);
  const currentOrgName = currentOrg?.name;

  const handleLogout = async () => {
    await logout();
    navigate({ to: "/orgs/$org/login", params: { org }, search: { session_expired: false, returnTo: undefined } });
  };

  const handleSwitchOrg = async (orgSlug: string) => {
    await switchOrg(orgSlug);
    navigate({ to: "/orgs/$org", params: { org: orgSlug } });
  };


  return (
    <Sidebar data-testid="app-sidebar">
      <SidebarHeader>
        <SidebarMenu>
          <SidebarMenuItem>
            <SidebarMenuButton size="lg" asChild>
              <Link to="/orgs/$org" params={{ org }}>
                <div className="flex aspect-square size-8 items-center justify-center overflow-hidden rounded">
                  {/* The org's own logo replaces the SolidPing mark when set;
                      the product mark is the fallback (spec 2026-08-08-12). */}
                  {currentOrg?.logoUrl ? (
                    <img
                      src={currentOrg.logoUrl}
                      alt=""
                      className="h-8 w-8 object-contain"
                      data-testid="sidebar-org-logo"
                    />
                  ) : (
                    <Logo size={32} />
                  )}
                </div>
                <div className="flex flex-col gap-0.5 leading-none">
                  <span className="font-semibold">SolidPing</span>
                  <span className="text-xs text-muted-foreground">
                    {currentOrgName || org}
                  </span>
                </div>
              </Link>
            </SidebarMenuButton>
          </SidebarMenuItem>
        </SidebarMenu>
      </SidebarHeader>

      <SidebarContent>
        <SidebarGroup>
          <SidebarGroupContent>
            <SidebarMenu>
              {navItems.map((item) => {
                const itemPath = item.path.replace("$org", org);
                const title = tNav(item.titleKey);
                return (
                  <SidebarMenuItem key={item.titleKey}>
                    <SidebarMenuButton
                      asChild
                      isActive={location.pathname === itemPath}
                      tooltip={title}
                    >
                      <Link to={item.path} params={{ org }}>
                        <item.icon />
                        <span>{title}</span>
                      </Link>
                    </SidebarMenuButton>
                  </SidebarMenuItem>
                );
              })}
            </SidebarMenu>
          </SidebarGroupContent>
        </SidebarGroup>
        {user?.isAdmin && (
          <SidebarGroup>
            <SidebarGroupContent>
              <SidebarMenu>
                <SidebarMenuItem>
                  <SidebarMenuButton
                    asChild
                    isActive={location.pathname.startsWith(`/orgs/${org}/discovery`)}
                    tooltip={tNav("discovery")}
                  >
                    <Link to="/orgs/$org/discovery" params={{ org }}>
                      <Network />
                      <span>{tNav("discovery")}</span>
                    </Link>
                  </SidebarMenuButton>
                </SidebarMenuItem>
                <SidebarMenuItem>
                  <SidebarMenuButton
                    asChild
                    isActive={location.pathname.startsWith(`/orgs/${org}/jobs`)}
                    tooltip={tNav("jobs")}
                  >
                    <Link to="/orgs/$org/jobs" params={{ org }} search={{ tab: "jobs" }}>
                      <Workflow />
                      <span>{tNav("jobs")}</span>
                    </Link>
                  </SidebarMenuButton>
                </SidebarMenuItem>
              </SidebarMenu>
            </SidebarGroupContent>
          </SidebarGroup>
        )}
        {isTestMode && (
          <SidebarGroup>
            <SidebarGroupContent>
              <SidebarMenu>
                {testNavItems.map((item) => {
                  const itemPath = item.path.replace("$org", org);
                  const title = tNav(item.titleKey);
                  return (
                    <SidebarMenuItem key={item.titleKey}>
                      <SidebarMenuButton
                        asChild
                        isActive={location.pathname === itemPath}
                        tooltip={title}
                      >
                        <Link to={item.path} params={{ org }}>
                          <item.icon />
                          <span>{title}</span>
                        </Link>
                      </SidebarMenuButton>
                    </SidebarMenuItem>
                  );
                })}
              </SidebarMenu>
            </SidebarGroupContent>
          </SidebarGroup>
        )}
      </SidebarContent>

      <SidebarFooter>
        <SidebarMenu>
          <SidebarMenuItem>
            <div className="flex items-center justify-around gap-1 px-2 py-1">
              <LanguageSwitcher />
              <ThemeToggle />
              <LiveStatusDot />
            </div>
          </SidebarMenuItem>
          <SidebarMenuItem>
            <DropdownMenu>
              <DropdownMenuTrigger asChild>
                <SidebarMenuButton
                  size="lg"
                  data-testid="user-menu-button"
                  className="data-[state=open]:bg-sidebar-accent data-[state=open]:text-sidebar-accent-foreground"
                >
                  {user?.avatarUrl ? (
                    <img
                      src={user.avatarUrl}
                      alt=""
                      className="size-8 rounded-lg object-cover"
                    />
                  ) : (
                    <div className="flex aspect-square size-8 items-center justify-center rounded-lg bg-muted">
                      <User2 className="size-4" />
                    </div>
                  )}
                  <div className="grid flex-1 text-left text-sm leading-tight">
                    <span className="truncate font-semibold">
                      {user?.name || user?.email || t("user")}
                    </span>
                    <span className="truncate text-xs text-muted-foreground">
                      {user?.name ? user.email : (user?.isAdmin ? t("administrator") : t("user"))}
                    </span>
                  </div>
                  <ChevronUp className="ml-auto size-4" />
                </SidebarMenuButton>
              </DropdownMenuTrigger>
              <DropdownMenuContent
                className="w-[--radix-dropdown-menu-trigger-width] min-w-56 rounded-lg"
                side="top"
                align="end"
                sideOffset={4}
              >
                <DropdownMenuItem asChild>
                  <Link to="/orgs/$org/account" params={{ org }}>
                    <User2 className="mr-2 h-4 w-4" />
                    {t("account")}
                  </Link>
                </DropdownMenuItem>
                {user?.isAdmin && (
                  <DropdownMenuItem asChild>
                    <Link to="/orgs/$org/organization" params={{ org }}>
                      <Building className="mr-2 h-4 w-4" />
                      {t("organization")}
                    </Link>
                  </DropdownMenuItem>
                )}
                {user?.isSuperAdmin && (
                  <DropdownMenuItem asChild data-testid="server-settings-link">
                    <Link to="/orgs/$org/server" params={{ org }}>
                      <Server className="mr-2 h-4 w-4" />
                      {t("server")}
                    </Link>
                  </DropdownMenuItem>
                )}
                {organizations.length > 1 && (
                  <>
                    <DropdownMenuSeparator />
                    <DropdownMenuLabel className="text-xs text-muted-foreground">
                      {t("switchOrganization")}
                    </DropdownMenuLabel>
                    {organizations
                      .filter((o) => o.slug !== org)
                      .map((o) => (
                        <DropdownMenuItem
                          key={o.slug}
                          onClick={() => handleSwitchOrg(o.slug)}
                          data-testid={`switch-org-${o.slug}`}
                        >
                          {o.logoUrl ? (
                            <img
                              src={o.logoUrl}
                              alt=""
                              className="mr-2 h-4 w-4 object-contain"
                            />
                          ) : (
                            <Building className="mr-2 h-4 w-4" />
                          )}
                          {o.name || o.slug}
                        </DropdownMenuItem>
                      ))}
                  </>
                )}
                <DropdownMenuSeparator />
                <DropdownMenuItem onClick={handleLogout} data-testid="logout-button">
                  <LogOut className="mr-2 h-4 w-4" />
                  {t("logOut")}
                </DropdownMenuItem>
              </DropdownMenuContent>
            </DropdownMenu>
          </SidebarMenuItem>
        </SidebarMenu>
      </SidebarFooter>
    </Sidebar>
  );
}
