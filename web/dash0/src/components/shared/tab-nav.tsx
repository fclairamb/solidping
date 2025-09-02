import { Link, useLocation } from "@tanstack/react-router";

interface Tab {
  label: string;
  path: string;
}

export function TabNav({ tabs, org }: { tabs: Tab[]; org: string }) {
  const location = useLocation();

  return (
    <nav className="flex gap-4 border-b">
      {tabs.map((tab) => {
        const resolved = tab.path.replace("$org", org);
        const isActive = location.pathname.startsWith(resolved);
        return (
          <Link
            key={tab.label}
            to={tab.path}
            params={{ org }}
            className={`pb-2 text-sm font-medium border-b-2 -mb-px transition-colors ${
              isActive
                ? "border-primary text-primary"
                : "border-transparent text-muted-foreground hover:text-foreground"
            }`}
          >
            {tab.label}
          </Link>
        );
      })}
    </nav>
  );
}
