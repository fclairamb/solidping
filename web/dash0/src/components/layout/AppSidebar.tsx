import { Link, useLocation, useNavigate, useParams } from "@tanstack/react-router";
import {
  Activity,
  Bug,
  LayoutDashboard,
  ListChecks,
  AlertTriangle,
  Calendar,
  Globe,
  BadgeCheck,
  LogOut,
  Moon,
  Sun,
  ChevronUp,
  User2,
  Building,
  Server,
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
import { useEffect, useState } from "react";
import { useTranslation } from "react-i18next";

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
    titleKey: "events",
    path: "/orgs/$org/events" as const,
    icon: Calendar,
  },
  {
    titleKey: "statusPages",
    path: "/orgs/$org/status-pages" as const,
    icon: Globe,
  },
  {
    titleKey: "badges",
    path: "/orgs/$org/badges" as const,
    icon: BadgeCheck,
  },
];

const testNavItems = [
  {
    titleKey: "testTools",
    path: "/orgs/$org/test" as const,
    icon: Bug,
  },
];


function getInitialTheme(): "light" | "dark" {
  const stored = localStorage.getItem("theme");
  const prefersDark = window.matchMedia("(prefers-color-scheme: dark)").matches;
  return stored === "dark" || (!stored && prefersDark) ? "dark" : "light";
}

export function ThemeToggle() {
  const { t } = useTranslation();
  const [theme, setTheme] = useState<"light" | "dark">(getInitialTheme);

  useEffect(() => {
    document.documentElement.classList.toggle("dark", theme === "dark");
  }, [theme]);

  const toggleTheme = () => {
    const newTheme = theme === "light" ? "dark" : "light";
    setTheme(newTheme);
    localStorage.setItem("theme", newTheme);
  };

  return (
    <button
      data-testid="theme-toggle"
      onClick={toggleTheme}
      className="inline-flex items-center justify-center rounded-md text-sm font-medium ring-offset-background transition-colors hover:bg-accent hover:text-accent-foreground h-9 w-9"
      aria-label={theme === "light" ? t("switchToDarkMode") : t("switchToLightMode")}
    >
      {theme === "light" ? (
        <Moon className="h-4 w-4" />
      ) : (
        <Sun className="h-4 w-4" />
      )}
    </button>
  );
}

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

  const currentOrgName = organizations.find((o) => o.slug === org)?.name;

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
                <div className="flex aspect-square size-8 items-center justify-center rounded-lg bg-primary text-primary-foreground">
                  <Activity className="size-4" />
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
                          <Building className="mr-2 h-4 w-4" />
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
