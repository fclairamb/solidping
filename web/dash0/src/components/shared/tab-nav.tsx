import { Link, useLocation } from "@tanstack/react-router";

interface Tab {
  label: string;
  path: string;
  badge?: number;
}

export function TabNav({ tabs, org }: { tabs: Tab[]; org: string }) {
  const location = useLocation();

  return (
    <nav className="flex gap-4 overflow-x-auto border-b" data-testid="tab-nav">
      {tabs.map((tab) => {
        const resolved = tab.path.replace("$org", org);
        const isActive = location.pathname.startsWith(resolved);
        return (
          <Link
            key={tab.label}
            to={tab.path}
            params={{ org }}
            className={`shrink-0 whitespace-nowrap pb-2 text-sm font-medium border-b-2 -mb-px transition-colors flex items-center gap-1.5 ${
              isActive
                ? "border-primary text-primary"
                : "border-transparent text-muted-foreground hover:text-foreground"
            }`}
          >
            <span>{tab.label}</span>
            {tab.badge !== undefined && tab.badge > 0 && (
              <span
                data-testid={`tab-badge-${tab.path.split("/").pop()}`}
                className="inline-flex items-center justify-center rounded-full bg-primary text-primary-foreground text-xs px-1.5 min-w-[1.25rem] h-5"
              >
                {tab.badge}
              </span>
            )}
          </Link>
        );
      })}
    </nav>
  );
}
