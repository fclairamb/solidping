// Dev-facing component reference. All in-page labels are intentionally
// hardcoded English — do not add i18n keys for the showcase content.
// Only the sidebar entry (nav:designReference) is translated.

import { useMemo, useState } from "react";
import { createFileRoute, Link } from "@tanstack/react-router";
import {
  AlertCircle,
  AlertTriangle,
  ArrowLeft,
  ArrowRight,
  Check,
  CheckCircle2,
  ChevronRight,
  Copy,
  Eye,
  Info,
  KeyRound,
  Moon,
  Globe,
  MoreVertical,
  Palette,
  Pencil,
  Plus,
  RefreshCw,
  RotateCw,
  Save,
  Search,
  Sun,
  Trash2,
} from "lucide-react";
import { toast } from "sonner";

import { CheckMultiPicker } from "@/components/shared/check-multi-picker";
import { MaintenanceScheduleSummary } from "@/components/shared/maintenance-schedule-summary";
import { JsonViewer } from "@/components/shared/json-viewer";
import { LabelFilter } from "@/components/shared/label-filter";
import { PageHeader } from "@/components/shared/page-header";
import { StatTile } from "@/components/shared/stat-tile";
import { StatusBadge } from "@/components/shared/status-badge";
import { StatusDot } from "@/components/shared/status-dot";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert";
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
  AlertDialogTrigger,
} from "@/components/ui/alert-dialog";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import {
  Card,
  CardContent,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import { Logo } from "@/components/ui/logo";
import { AuroraPanel } from "@/components/ui/aurora-panel";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
  DialogTrigger,
} from "@/components/ui/dialog";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover";
import {
  Tooltip,
  TooltipContent,
  TooltipProvider,
  TooltipTrigger,
} from "@/components/ui/tooltip";
import { Checkbox } from "@/components/ui/checkbox";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { PasswordInput } from "@/components/ui/password-input";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { Skeleton } from "@/components/ui/skeleton";
import { Switch } from "@/components/ui/switch";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import { Textarea } from "@/components/ui/textarea";
import { UptimeStrip } from "@/components/ui/uptime-strip";
import { useDebounce } from "@/lib/use-debounce";
import { slugify } from "@/lib/utils";

export const Route = createFileRoute("/orgs/$org/design-reference")({
  component: DesignReferencePage,
});

const SECTIONS: { id: string; label: string }[] = [
  { id: "overview", label: "Overview" },
  { id: "conventions", label: "Conventions" },
  { id: "page-header", label: "Page header" },
  { id: "breadcrumbs", label: "Breadcrumbs" },
  { id: "color-tokens", label: "Color tokens" },
  { id: "brand", label: "Brand" },
  { id: "elevation", label: "Elevation & aurora" },
  { id: "buttons-badges", label: "Buttons & badges" },
  { id: "forms", label: "Forms" },
  { id: "data-display", label: "Data display" },
  { id: "collapsible-code", label: "Collapsible code" },
  { id: "feedback", label: "Feedback" },
  { id: "label-filter", label: "Label filter" },
  { id: "check-multi-picker", label: "Check multi-picker" },
  { id: "kpi-tiles", label: "KPI tiles" },
  { id: "uptime-strip", label: "Uptime strip" },
  { id: "jobs-primitives", label: "Jobs primitives" },
  { id: "maintenance-schedule", label: "Maintenance schedule" },
];

function DesignReferencePage() {
  return (
    <div className="space-y-8 pb-24">
      <PageHeader
        icon={Palette}
        title="Design Reference"
        description="A live, scrollable index of every dash0 UI primitive and shared pattern. Copy the import line next to each example. Toggle the theme to verify dark-mode parity. Source: web/dash0/src/routes/orgs/$org/design-reference.tsx."
      />
      <SubNav />
      <OverviewSection />
      <ConventionsSection />
      <PageHeaderSection />
      <BreadcrumbsSection />
      <ColorTokensSection />
      <BrandSection />
      <ElevationSection />
      <ButtonsBadgesSection />
      <FormsSection />
      <DataDisplaySection />
      <CollapsibleCodeSection />
      <FeedbackSection />
      <LabelFilterSection />
      <CheckMultiPickerSection />
      <KpiTileSection />
      <UptimeStripSection />
      <JobsPrimitivesSection />
      <MaintenanceScheduleSection />
    </div>
  );
}

function ConventionsSection() {
  return (
    <Section
      id="conventions"
      title="Conventions"
      description="Project-wide rules that shape how routes, forms, and row actions feel across the app. Read once; reach for them whenever you scaffold something new."
    >
      <div className="space-y-4">
        <div className="rounded-md border bg-card p-4 space-y-2">
          <h3 className="text-sm font-semibold">Editing always changes the route</h3>
          <p className="text-sm text-muted-foreground">
            Editing an entity must navigate to a dedicated route, never open a
            modal dialog. Mirror the create flow:{" "}
            <code className="rounded bg-muted px-1 py-0.5 text-xs">/&lt;resource&gt;/new</code>{" "}
            for creation,{" "}
            <code className="rounded bg-muted px-1 py-0.5 text-xs">/&lt;resource&gt;/$id/edit</code>{" "}
            for editing. The edit route renders a full page and reuses the same
            form component as the create route.
          </p>
          <p className="text-sm text-muted-foreground">
            <strong>Why:</strong> routes are bookmarkable, deep-linkable,
            browser-back works as expected, and the URL is the source of truth
            for "what the user is doing." Modal edits hide state, lose on
            accidental backdrop clicks, and don't survive refreshes.
          </p>
          <p className="text-sm text-muted-foreground">
            <strong>Carve-out:</strong> trivial single-field renames may stay
            inline. Anything with a multi-field form goes through a route.
          </p>
        </div>
        <div className="rounded-md border bg-card p-4 space-y-2">
          <h3 className="text-sm font-semibold">Row actions: icons, not menus</h3>
          <p className="text-sm text-muted-foreground">
            In list/table rows, prefer two ghost icon buttons (
            <code className="rounded bg-muted px-1 py-0.5 text-xs">Pencil</code>{" "}
            for edit,{" "}
            <code className="rounded bg-muted px-1 py-0.5 text-xs">Trash2</code>{" "}
            for delete with{" "}
            <code className="rounded bg-muted px-1 py-0.5 text-xs">text-destructive</code>
            ) over a{" "}
            <code className="rounded bg-muted px-1 py-0.5 text-xs">DropdownMenu</code>{" "}
            with a{" "}
            <code className="rounded bg-muted px-1 py-0.5 text-xs">MoreVertical</code>{" "}
            trigger. The Edit icon links to the edit route; the Delete icon
            opens an{" "}
            <code className="rounded bg-muted px-1 py-0.5 text-xs">AlertDialog</code>
            . Other per-row actions live on the edit page, not in the row.
          </p>
        </div>
        <div className="rounded-md border bg-card p-4 space-y-3">
          <h3 className="text-sm font-semibold">Delete is always red, always a trash bin</h3>
          <p className="text-sm text-muted-foreground">
            Every delete (or otherwise irreversible) action is rendered in red
            and paired with the{" "}
            <code className="rounded bg-muted px-1 py-0.5 text-xs">Trash2</code>{" "}
            (trash bin) icon — no exceptions. Use a{" "}
            <code className="rounded bg-muted px-1 py-0.5 text-xs">Button variant=&quot;destructive&quot;</code>{" "}
            for standalone/prominent buttons, an icon button with{" "}
            <code className="rounded bg-muted px-1 py-0.5 text-xs">text-destructive</code>{" "}
            in row actions, and{" "}
            <code className="rounded bg-muted px-1 py-0.5 text-xs">text-destructive focus:text-destructive</code>{" "}
            on the delete item inside a{" "}
            <code className="rounded bg-muted px-1 py-0.5 text-xs">DropdownMenu</code>
            . Both colors resolve to the{" "}
            <code className="rounded bg-muted px-1 py-0.5 text-xs">--destructive</code>{" "}
            token so dark mode stays correct. The destructive red is reserved
            for destructive actions — never use it for a neutral or primary
            action, and never delete with a different icon or a muted color.
          </p>
          <div className="flex flex-wrap items-center gap-3">
            <Button variant="destructive" aria-label="Delete">
              <Trash2 />
              <span className="hidden sm:inline">Delete</span>
            </Button>
            <Button
              variant="ghost"
              size="icon"
              className="text-destructive hover:text-destructive"
              aria-label="Delete row"
            >
              <Trash2 />
            </Button>
          </div>
        </div>
      </div>
    </Section>
  );
}

function SubNav() {
  return (
    <nav
      aria-label="Design reference sections"
      className="sticky top-0 z-10 -mx-3 border-b bg-background/80 px-3 py-2 backdrop-blur sm:-mx-4 sm:px-4"
    >
      <ul className="flex flex-wrap gap-2">
        {SECTIONS.map((s) => (
          <li key={s.id}>
            <a
              href={`#${s.id}`}
              className="inline-flex items-center rounded-full border border-border bg-card px-3 py-1 text-xs font-medium text-muted-foreground transition-colors hover:bg-accent hover:text-accent-foreground"
            >
              {s.label}
            </a>
          </li>
        ))}
      </ul>
    </nav>
  );
}

function Section({
  id,
  title,
  description,
  children,
}: {
  id: string;
  title: string;
  description?: string;
  children: React.ReactNode;
}) {
  return (
    <section id={id} className="scroll-mt-20 space-y-4">
      <header>
        <h2 className="text-lg font-semibold tracking-tight">{title}</h2>
        {description ? (
          <p className="mt-1 text-sm text-muted-foreground">{description}</p>
        ) : null}
      </header>
      {children}
    </section>
  );
}

function CodeSnippet({ code }: { code: string }) {
  const [copied, setCopied] = useState(false);

  const onCopy = async () => {
    try {
      await navigator.clipboard.writeText(code);
      setCopied(true);
      window.setTimeout(() => setCopied(false), 1500);
    } catch {
      // Clipboard API requires a secure context; ignore failures silently
      // — the page still shows the code so contributors can copy by hand.
    }
  };

  return (
    <div className="relative">
      <pre className="overflow-x-auto rounded-md border bg-muted/40 px-3 py-2 pr-12 text-xs leading-relaxed">
        <code>{code}</code>
      </pre>
      <button
        type="button"
        onClick={onCopy}
        aria-label={copied ? "Copied" : "Copy to clipboard"}
        className="absolute right-1.5 top-1.5 inline-flex h-7 w-7 items-center justify-center rounded-md text-muted-foreground transition-colors hover:bg-accent hover:text-accent-foreground"
      >
        {copied ? <Check className="h-4 w-4" /> : <Copy className="h-4 w-4" />}
      </button>
    </div>
  );
}

function ExampleRow({
  preview,
  importLine,
}: {
  preview: React.ReactNode;
  importLine: string;
}) {
  return (
    <div className="grid gap-3 rounded-md border bg-card p-4 md:grid-cols-[1fr_minmax(0,1fr)] md:items-start">
      <div className="flex flex-wrap items-center gap-2">{preview}</div>
      <CodeSnippet code={importLine} />
    </div>
  );
}

function OverviewSection() {
  return (
    <Section
      id="overview"
      title="Overview"
      description="Single-page reference for every shipped UI primitive and shared pattern. Every example renders live; every import line copies clean. Toggle the theme below to verify dark-mode parity without leaving the page."
    >
      <div className="rounded-md border bg-card p-4">
        <PageThemeToggle />
        <p className="mt-3 text-sm text-muted-foreground">
          Tip: open <code className="rounded bg-muted px-1 py-0.5 text-xs">web/dash0/src/routes/orgs/$org/design-reference.tsx</code> when copying patterns — many examples here lift directly from real routes (e.g. <code className="rounded bg-muted px-1 py-0.5 text-xs">checks.index.tsx</code>) and the source file is the canonical reference.
        </p>
      </div>
    </Section>
  );
}

function PageThemeToggle() {
  const isDark = () =>
    typeof document !== "undefined" &&
    document.documentElement.classList.contains("dark");
  const [dark, setDark] = useState<boolean>(isDark);

  const toggle = () => {
    const next = !dark;
    setDark(next);
    document.documentElement.classList.toggle("dark", next);
    try {
      localStorage.setItem("theme", next ? "dark" : "light");
    } catch {
      // Storage may be unavailable (private mode); the visual toggle still works.
    }
  };

  return (
    <div className="flex items-center justify-between gap-3">
      <div>
        <p className="text-sm font-medium">Theme</p>
        <p className="text-xs text-muted-foreground">
          Toggles the same <code>html.dark</code> class the sidebar toggle uses.
        </p>
      </div>
      <Button
        type="button"
        variant="outline"
        size="sm"
        onClick={toggle}
        aria-pressed={dark}
        aria-label={dark ? "Switch to light mode" : "Switch to dark mode"}
      >
        {dark ? <Sun className="h-4 w-4" /> : <Moon className="h-4 w-4" />}
        <span>{dark ? "Light mode" : "Dark mode"}</span>
      </Button>
    </div>
  );
}

function PageHeaderSection() {
  const pageHeaderSnippet = `// Canonical page header for every list and section page.
// Boxed muted icon tile, text-2xl title, optional subtitle, right-aligned actions.
import { PageHeader } from "@/components/shared/page-header";

<PageHeader
  icon={Globe}
  title="Status pages"
  description="Optional one-line subtitle."
  actions={
    <Button asChild>
      <Link to="/orgs/$org/status-pages/new" params={{ org }}>
        <Plus className="mr-2 h-4 w-4" />
        New page
      </Link>
    </Button>
  }
  className="flex-wrap"
/>`;
  return (
    <Section
      id="page-header"
      title="Page header"
      description="Every page opens with a page header — the page title plus its right-aligned actions. 'Page title' and 'page header' are the same surface, not two primitives. List and section pages render it with the boxed PageHeader component (@/components/shared/page-header); detail and edit pages compose the same header inline so it can carry a back arrow and per-record actions. Both patterns are documented here."
    >
      <h3 className="text-sm font-medium">List &amp; section pages: the PageHeader component</h3>
      <p className="text-sm text-muted-foreground">
        Pass icon, title, an optional description, and right-aligned actions; it
        renders a rounded muted icon tile, a text-2xl font-semibold title, the
        muted subtitle below, and the actions on the right. Discovery, checks,
        incidents, status-pages, on-call, integrations, badges, me/notifications
        and the rest all ship it — use it for every new page rather than
        hand-rolling an inline header.
      </p>
      <div className="rounded-md border bg-card p-4">
        <PageHeader
          icon={Globe}
          title="Status pages"
          description="Optional one-line subtitle that explains what the page is for."
          actions={
            <Button>
              <Plus className="mr-2 h-4 w-4" />
              New page
            </Button>
          }
          className="flex-wrap"
        />
      </div>
      <CodeSnippet code={pageHeaderSnippet} />
      <p className="text-sm text-muted-foreground">
        Notes: pass the same per-page Lucide icon you would have rendered
        inline — <code className="rounded bg-muted px-1 py-0.5 text-xs">PageHeader</code>{" "}
        wraps it in the muted tile for you. Put the primary action(s) that used
        to sit in the header row (e.g.{" "}
        <code className="rounded bg-muted px-1 py-0.5 text-xs">+ New X</code>,
        export/import, a refresh button) into the{" "}
        <code className="rounded bg-muted px-1 py-0.5 text-xs">actions</code>{" "}
        prop; leave filter/search toolbars on their own row below the header.
        Add <code className="rounded bg-muted px-1 py-0.5 text-xs">className="flex-wrap"</code>{" "}
        so actions wrap instead of overflowing on mobile. The detail/edit-page
        header — back arrow inside the right-aligned action cluster — is the
        same surface for detail pages; it is documented just below.
      </p>

      <h3 className="text-sm font-medium">Detail &amp; edit pages: title block + right-aligned action cluster (back arrow first)</h3>
      <p className="text-sm text-muted-foreground">
        On detail/edit pages, compose a{" "}
        <code className="mx-1 rounded bg-muted px-1 py-0.5 text-xs">flex items-start justify-between gap-3</code>{" "}
        row. The <strong>left</strong> is the title block — the page{" "}
        <code className="mx-1 rounded bg-muted px-1 py-0.5 text-xs">h1</code>{" "}
        plus any subtitle/status — wrapped in{" "}
        <code className="mx-1 rounded bg-muted px-1 py-0.5 text-xs">min-w-0 flex-1</code>{" "}
        so it truncates instead of shoving the actions off-screen. The{" "}
        <strong>right</strong> is a{" "}
        <code className="mx-1 rounded bg-muted px-1 py-0.5 text-xs">flex gap-2 shrink-0</code>{" "}
        cluster whose <strong>first child is the icon-only ghost back button</strong>,
        followed by the page actions (View / Edit / Delete, Refresh, …). The
        back arrow is <strong>not</strong> on the far left — it leads the
        right-aligned cluster. It is{" "}
        <strong>always icon-only</strong> — never paired with a &quot;Back&quot;
        label. Use{" "}
        <code className="mx-1 rounded bg-muted px-1 py-0.5 text-xs">ArrowLeft</code>{" "}
        with{" "}
        <code className="mx-1 rounded bg-muted px-1 py-0.5 text-xs">variant=&quot;ghost&quot; size=&quot;icon&quot;</code>{" "}
        and an{" "}
        <code className="mx-1 rounded bg-muted px-1 py-0.5 text-xs">aria-label</code>.
        A trailing Refresh button labels itself on desktop and collapses to
        the icon below{" "}
        <code className="mx-1 rounded bg-muted px-1 py-0.5 text-xs">sm</code>.
      </p>
      <ExampleRow
        preview={
          <div className="flex w-full items-start justify-between gap-3">
            <div className="min-w-0 flex-1">
              <h1 className="truncate text-2xl font-bold tracking-tight sm:text-3xl">
                Page title
              </h1>
              <p className="mt-1 truncate text-muted-foreground">
                Optional subtitle / status
              </p>
            </div>
            <div className="flex shrink-0 gap-2">
              <Button variant="ghost" size="icon" aria-label="Back">
                <ArrowLeft />
              </Button>
              <Button variant="outline" size="sm" aria-label="Edit">
                <Pencil className="sm:mr-2 h-4 w-4" />
                <span className="hidden sm:inline">Edit</span>
              </Button>
              <Button variant="outline" aria-label="Refresh">
                <RotateCw className="h-4 w-4 sm:mr-2" />
                <span className="hidden sm:inline">Refresh</span>
              </Button>
              <Button variant="destructive" size="sm" aria-label="Delete">
                <Trash2 />
                <span className="hidden sm:inline">Delete</span>
              </Button>
            </div>
          </div>
        }
        importLine={`<div className="flex items-start justify-between gap-3">\n  <div className="min-w-0 flex-1">\n    <h1 className="truncate text-2xl sm:text-3xl font-bold tracking-tight">{title}</h1>\n    {subtitle && <p className="mt-1 text-muted-foreground truncate">{subtitle}</p>}\n  </div>\n  <div className="flex gap-2 shrink-0">\n    <Button asChild variant="ghost" size="icon" aria-label="Back">\n      <Link to="/orgs/$org/things" params={{ org }}>\n        <ArrowLeft className="h-4 w-4" />\n      </Link>\n    </Button>\n    <Button variant="outline" size="sm" onClick={handleEdit} aria-label="Edit">\n      <Pencil className="sm:mr-2 h-4 w-4" />\n      <span className="hidden sm:inline">Edit</span>\n    </Button>\n    <Button variant="outline" onClick={handleRefresh} aria-label="Refresh">\n      <RotateCw className="h-4 w-4 sm:mr-2" />\n      <span className="hidden sm:inline">Refresh</span>\n    </Button>\n    <Button variant="destructive" size="sm" onClick={handleDelete}>\n      <Trash2 />\n      <span className="hidden sm:inline">Delete</span>\n    </Button>\n  </div>\n</div>`}
      />

      <h3 className="text-sm font-medium">Detail &amp; edit pages: collapse the action cluster into an overflow menu on mobile</h3>
      <p className="text-sm text-muted-foreground">
        When a detail header carries more than two or three actions, the inline
        toolbar overflows on a phone. Keep only the icon-only ghost{" "}
        <strong>back button</strong> always visible; render the labeled action
        buttons in a{" "}
        <code className="mx-1 rounded bg-muted px-1 py-0.5 text-xs">hidden md:flex</code>{" "}
        cluster, and mirror every one of them as items inside a{" "}
        <code className="mx-1 rounded bg-muted px-1 py-0.5 text-xs">md:hidden</code>{" "}
        <code className="mx-1 rounded bg-muted px-1 py-0.5 text-xs">DropdownMenu</code>{" "}
        triggered by a{" "}
        <code className="mx-1 rounded bg-muted px-1 py-0.5 text-xs">MoreVertical</code>{" "}
        (⋯) button. The delete item is{" "}
        <code className="mx-1 rounded bg-muted px-1 py-0.5 text-xs">text-destructive focus:text-destructive</code>{" "}
        with a{" "}
        <code className="mx-1 rounded bg-muted px-1 py-0.5 text-xs">Trash2</code>{" "}
        icon, just like the inline destructive button. Drive any confirm
        dialog from controlled state so it opens from either surface.
      </p>
      <ExampleRow
        preview={
          <div className="flex w-full items-start justify-between gap-3">
            <div className="min-w-0 flex-1">
              <h1 className="truncate text-2xl font-bold tracking-tight sm:text-3xl">
                Page title
              </h1>
            </div>
            <div className="flex shrink-0 items-center gap-2">
              <Button variant="ghost" size="icon" aria-label="Back">
                <ArrowLeft />
              </Button>
              <div className="hidden items-center gap-2 md:flex">
                <Button variant="outline" aria-label="Edit">
                  <Pencil className="h-4 w-4 sm:mr-2" />
                  <span className="hidden sm:inline">Edit</span>
                </Button>
                <Button variant="outline" aria-label="Refresh">
                  <RotateCw className="h-4 w-4 sm:mr-2" />
                  <span className="hidden sm:inline">Refresh</span>
                </Button>
                <Button variant="destructive" aria-label="Delete">
                  <Trash2 className="h-4 w-4 sm:mr-2" />
                  <span className="hidden sm:inline">Delete</span>
                </Button>
              </div>
              <div className="md:hidden">
                <DropdownMenu>
                  <DropdownMenuTrigger asChild>
                    <Button variant="outline" size="icon" aria-label="More actions">
                      <MoreVertical />
                    </Button>
                  </DropdownMenuTrigger>
                  <DropdownMenuContent align="end">
                    <DropdownMenuItem>
                      <Pencil />
                      Edit
                    </DropdownMenuItem>
                    <DropdownMenuItem>
                      <RotateCw />
                      Refresh
                    </DropdownMenuItem>
                    <DropdownMenuSeparator />
                    <DropdownMenuItem className="text-destructive focus:text-destructive">
                      <Trash2 />
                      Delete
                    </DropdownMenuItem>
                  </DropdownMenuContent>
                </DropdownMenu>
              </div>
            </div>
          </div>
        }
        importLine={`<div className="flex shrink-0 items-center gap-2">\n  <Button variant="ghost" size="icon" aria-label="Back" onClick={goBack}>\n    <ArrowLeft className="h-4 w-4" />\n  </Button>\n  <div className="hidden items-center gap-2 md:flex">\n    {/* labeled action buttons */}\n  </div>\n  <div className="md:hidden">\n    <DropdownMenu>\n      <DropdownMenuTrigger asChild>\n        <Button variant="outline" size="icon" aria-label="More actions">\n          <MoreVertical className="h-4 w-4" />\n        </Button>\n      </DropdownMenuTrigger>\n      <DropdownMenuContent align="end">\n        {/* mirror each action; delete = text-destructive */}\n      </DropdownMenuContent>\n    </DropdownMenu>\n  </div>\n</div>`}
      />

      <h3 className="text-sm font-medium">Detail &amp; edit pages: stack the action toolbar on its own row (action-dense headers)</h3>
      <p className="text-sm text-muted-foreground">
        When a detail header carries <strong>more than ~three actions</strong>{" "}
        (e.g. the check detail page: back + Edit, Enable/Disable, Clone, Badges,
        Refresh, Delete) the labeled toolbar and a long title fight for the same
        row — even on a wide desktop. Instead of shrinking the buttons or hiding
        them behind an overflow menu, drop the toolbar onto its own row. Make the
        outer wrapper a{" "}
        <code className="mx-1 rounded bg-muted px-1 py-0.5 text-xs">flex flex-col gap-3</code>{" "}
        column: the title block (still wrapped in{" "}
        <code className="mx-1 rounded bg-muted px-1 py-0.5 text-xs">min-w-0 flex-1</code>{" "}
        so the{" "}
        <code className="mx-1 rounded bg-muted px-1 py-0.5 text-xs">h1</code>{" "}
        truncates) takes the first row, then the action cluster — back arrow
        leading, as ever — sits on a second row wrapped in{" "}
        <code className="mx-1 rounded bg-muted px-1 py-0.5 text-xs">flex flex-wrap items-center justify-end gap-2</code>.
        It is right-aligned and wraps across lines on a narrow phone rather than
        overflowing. The per-button responsive behaviour (icon-only below{" "}
        <code className="mx-1 rounded bg-muted px-1 py-0.5 text-xs">lg</code>,
        icon + label at{" "}
        <code className="mx-1 rounded bg-muted px-1 py-0.5 text-xs">lg+</code>) is
        unchanged — only the wrappers move.
      </p>
      <ExampleRow
        preview={
          <div className="flex w-full flex-col gap-3">
            <div className="min-w-0 flex-1">
              <h1 className="truncate text-2xl font-bold tracking-tight sm:text-3xl">
                A long page title that would otherwise crowd the toolbar
              </h1>
              <p className="mt-1 truncate text-muted-foreground">
                Optional subtitle / status
              </p>
            </div>
            <div className="flex flex-wrap items-center justify-end gap-2">
              <Button variant="ghost" size="icon" aria-label="Back">
                <ArrowLeft />
              </Button>
              <Button variant="outline" aria-label="Edit">
                <Pencil className="h-4 w-4 sm:mr-2" />
                <span className="hidden sm:inline">Edit</span>
              </Button>
              <Button variant="outline" aria-label="Clone">
                <Copy className="h-4 w-4 sm:mr-2" />
                <span className="hidden sm:inline">Clone</span>
              </Button>
              <Button variant="outline" aria-label="Refresh">
                <RotateCw className="h-4 w-4 sm:mr-2" />
                <span className="hidden sm:inline">Refresh</span>
              </Button>
              <Button variant="destructive" aria-label="Delete">
                <Trash2 className="h-4 w-4 sm:mr-2" />
                <span className="hidden sm:inline">Delete</span>
              </Button>
            </div>
          </div>
        }
        importLine={`<div className="flex flex-col gap-3">\n  <div className="min-w-0 flex-1">\n    <h1 className="truncate text-2xl sm:text-3xl font-bold tracking-tight">{title}</h1>\n    {subtitle && <p className="mt-1 text-muted-foreground truncate">{subtitle}</p>}\n  </div>\n  <div className="flex flex-wrap items-center justify-end gap-2">\n    <Button variant="ghost" size="icon" aria-label="Back" onClick={goBack}>\n      <ArrowLeft className="h-4 w-4" />\n    </Button>\n    {/* labeled action buttons — icon-only below lg, icon + label at lg+ */}\n  </div>\n</div>`}
      />

      <div className="rounded-md border border-dashed bg-muted/30 p-4">
        <p className="text-sm text-muted-foreground">
          <strong className="text-foreground">Legacy (retired):</strong> the
          inline{" "}
          <code className="rounded bg-muted px-1 py-0.5 text-xs">
            &lt;h1 className="text-3xl font-bold …"&gt;
          </code>{" "}
          header with an inline{" "}
          <code className="rounded bg-muted px-1 py-0.5 text-xs">h-7 w-7</code>{" "}
          icon is no longer canonical. Do not reach for it on new pages — and
          migrate any remaining inline headers to{" "}
          <code className="rounded bg-muted px-1 py-0.5 text-xs">PageHeader</code>{" "}
          so the app stays consistent.
        </p>
      </div>
    </Section>
  );
}

function BreadcrumbsSection() {
  const snippet = `// In web/dash0/src/routes/orgs/$org.tsx, add a section flag and a branch
const isFooBar = matches.some((m) => m.routeId.startsWith("/orgs/$org/foo-bar"));

if (isFooBar) {
  return (
    <span className={activeClass}>
      <FooBarIcon className={iconClass} />
      Foo Bar
    </span>
  );
}`;
  const contextualSnippet = `// Context-driven breadcrumb: reads ?from= to show the correct parent.
// Example: the notification detail route (/orgs/$org/notifications/$notificationUid)
// uses three variants depending on the ?from= search param:

// 1. No ?from= → Integrations > Notification
<Link to="/orgs/$org/integrations" ...><Bell />Integrations</Link>
<BreadcrumbSeparator />
<span className={activeClass}>Notification</span>

// 2. ?from=incident:{uid} → Incidents > {incident title} > Notification
<Link to="/orgs/$org/incidents" ...><AlertTriangle />Incidents</Link>
<BreadcrumbSeparator />
<Link to="/orgs/$org/incidents/$incidentUid" ...>{incidentLabel}</Link>
<BreadcrumbSeparator />
<span className={activeClass}>Notification</span>

// 3. ?from=integration:{uid} → Integrations > {integration name} > Notification
<Link to="/orgs/$org/integrations" ...><Bell />Integrations</Link>
<BreadcrumbSeparator />
<Link to="/orgs/$org/integrations/$integrationUid" ...>{integrationLabel}</Link>
<BreadcrumbSeparator />
<span className={activeClass}>Notification</span>

// The label falls back to uid.slice(0,8) when the page is cold (direct nav,
// no prior fetch). The cached data comes from queryClient.getQueryData(["orgNotification", ...])
// — no waterfall, no extra network request.`;
  return (
    <Section
      id="breadcrumbs"
      title="Breadcrumbs"
      description="Breadcrumbs are route-driven, not a drop-in component. They live in the Breadcrumbs() function inside web/dash0/src/routes/orgs/$org.tsx. Each section adds a flag (isChecks, isIncidents, …) and a branch. The header at the top of this very page is a live example."
    >
      <div className="rounded-md border bg-card p-4">
        <p className="text-sm text-muted-foreground">
          Look at the header bar above the sidebar trigger — the breadcrumb shows{" "}
          <code className="rounded bg-muted px-1 py-0.5 text-xs">Design Reference</code>{" "}
          with the Palette icon. That branch was added alongside the others in{" "}
          <code className="rounded bg-muted px-1 py-0.5 text-xs">$org.tsx</code>.
        </p>
      </div>
      <CodeSnippet code={snippet} />
      <div className="rounded-md border bg-card p-4 space-y-2">
        <p className="text-sm font-medium">Context-driven breadcrumbs with <code className="rounded bg-muted px-1 py-0.5 text-xs">?from=</code></p>
        <p className="text-sm text-muted-foreground">
          When a detail page can be reached from multiple parent surfaces, encode the
          navigation context in a <code className="rounded bg-muted px-1 py-0.5 text-xs">?from=type:uid</code> search
          param (e.g. <code className="rounded bg-muted px-1 py-0.5 text-xs">?from=incident:abc123</code> or{" "}
          <code className="rounded bg-muted px-1 py-0.5 text-xs">?from=integration:xyz</code>).
          The breadcrumb reads the param and renders the matching parent chain. Label resolution
          uses the query cache — no extra fetch. The notification detail route
          (<code className="rounded bg-muted px-1 py-0.5 text-xs">/orgs/$org/notifications/$notificationUid</code>) is
          the canonical example of this pattern.
        </p>
      </div>
      <CodeSnippet code={contextualSnippet} />
    </Section>
  );
}

const COLOR_TOKENS: { name: string; varName: string; description?: string }[] = [
  { name: "primary", varName: "--primary", description: "Action color (buttons, links, focus rings)" },
  { name: "brand", varName: "--brand", description: "Logo/marketing chrome — never an interactive affordance" },
  { name: "brand-muted", varName: "--brand-muted", description: "Soft brand wash for headers / hero strips" },
  { name: "destructive", varName: "--destructive", description: "Delete / irreversible action confirms" },
  { name: "accent", varName: "--accent", description: "Hover/highlight surface" },
  { name: "muted-foreground", varName: "--muted-foreground", description: "Secondary text" },
  { name: "status-ok", varName: "--status-ok", description: "Healthy / passing" },
  { name: "status-warning", varName: "--status-warning", description: "Degraded" },
  { name: "status-error", varName: "--status-error", description: "Failing" },
];

const CHART_TOKENS = ["--chart-1", "--chart-2", "--chart-3", "--chart-4", "--chart-5"];

function Swatch({ varName, label, description }: { varName: string; label: string; description?: string }) {
  return (
    <div className="flex items-center gap-3 rounded-md border bg-card p-3">
      <div
        className="h-10 w-10 shrink-0 rounded-md border"
        style={{ backgroundColor: `var(${varName})` }}
      />
      <div className="min-w-0 flex-1">
        <p className="text-sm font-medium leading-tight">{label}</p>
        <p className="font-mono text-xs text-muted-foreground">{varName}</p>
        {description ? (
          <p className="mt-0.5 text-xs text-muted-foreground">{description}</p>
        ) : null}
      </div>
    </div>
  );
}

function ButtonsBadgesSection() {
  return (
    <Section
      id="buttons-badges"
      title="Buttons & badges"
      description="All variants and sizes shipped today. Pick the one with the least visual weight that still does the job. Action buttons should pair an icon with a short verb and collapse to icon-only on mobile. The detail-page header that composes these into a back button + action cluster lives in the Page header section."
    >
      <div className="space-y-4">
        <h3 className="text-sm font-medium">Button variants</h3>
        <ExampleRow
          preview={
            <>
              <Button>Default</Button>
              <Button variant="destructive">Destructive</Button>
              <Button variant="outline">Outline</Button>
              <Button variant="secondary">Secondary</Button>
              <Button variant="ghost">Ghost</Button>
              <Button variant="link">Link</Button>
            </>
          }
          importLine={`import { Button } from "@/components/ui/button";`}
        />

        <h3 className="text-sm font-medium">Button sizes</h3>
        <ExampleRow
          preview={
            <>
              <Button size="sm">Small</Button>
              <Button size="default">Default</Button>
              <Button size="lg">Large</Button>
              <Button size="icon" aria-label="Icon button">
                <ArrowRight />
              </Button>
            </>
          }
          importLine={`import { Button } from "@/components/ui/button";`}
        />

        <h3 className="text-sm font-medium">Action buttons (icon + label, mobile collapses to icon)</h3>
        <p className="text-sm text-muted-foreground">
          Pair every action with a recognisable icon and a one-word verb. Use{" "}
          <code className="mx-1 rounded bg-muted px-1 py-0.5 text-xs">Save</code> (floppy disk) for
          save, <code className="mx-1 rounded bg-muted px-1 py-0.5 text-xs">Trash2</code> for
          delete, <code className="mx-1 rounded bg-muted px-1 py-0.5 text-xs">Pencil</code> for
          edit, and <code className="mx-1 rounded bg-muted px-1 py-0.5 text-xs">RotateCw</code>{" "}
          for reload. Below the{" "}
          <code className="mx-1 rounded bg-muted px-1 py-0.5 text-xs">sm</code> breakpoint, the
          label collapses and only the icon remains: wrap the label in{" "}
          <code className="mx-1 rounded bg-muted px-1 py-0.5 text-xs">
            &lt;span className=&quot;hidden sm:inline&quot;&gt;
          </code>{" "}
          and pair every button with an{" "}
          <code className="mx-1 rounded bg-muted px-1 py-0.5 text-xs">aria-label</code> so screen
          readers still announce the action when the text is gone. Resize your viewport to verify.
        </p>
        <ExampleRow
          preview={
            <>
              <Button aria-label="Save">
                <Save />
                <span className="hidden sm:inline">Save</span>
              </Button>
              <Button variant="destructive" aria-label="Delete">
                <Trash2 />
                <span className="hidden sm:inline">Delete</span>
              </Button>
              <Button variant="outline" aria-label="Edit">
                <Pencil />
                <span className="hidden sm:inline">Edit</span>
              </Button>
              <Button variant="outline" aria-label="Reload">
                <RotateCw />
                <span className="hidden sm:inline">Reload</span>
              </Button>
            </>
          }
          importLine={`<Button aria-label="Save">\n  <Save />\n  <span className="hidden sm:inline">Save</span>\n</Button>`}
        />

        <h3 className="text-sm font-medium">Header refresh button (icon-only on mobile)</h3>
        <p className="text-sm text-muted-foreground">
          The canonical list/detail header refresh control. An{" "}
          <code className="mx-1 rounded bg-muted px-1 py-0.5 text-xs">outline</code> button
          wrapping{" "}
          <code className="mx-1 rounded bg-muted px-1 py-0.5 text-xs">RefreshCw</code> that shows
          the word <strong>Refresh</strong> from the{" "}
          <code className="mx-1 rounded bg-muted px-1 py-0.5 text-xs">sm</code> breakpoint up and
          collapses to icon-only below it. Drop{" "}
          <code className="mx-1 rounded bg-muted px-1 py-0.5 text-xs">size=&quot;icon&quot;</code>{" "}
          so the button sizes to its content, put{" "}
          <code className="mx-1 rounded bg-muted px-1 py-0.5 text-xs">sm:mr-2</code> on the icon so
          the gap only appears with the label, and keep an{" "}
          <code className="mx-1 rounded bg-muted px-1 py-0.5 text-xs">aria-label</code> for the
          icon-only state. Add{" "}
          <code className="mx-1 rounded bg-muted px-1 py-0.5 text-xs">animate-spin</code> while the
          query is refetching. (Use the localized{" "}
          <code className="mx-1 rounded bg-muted px-1 py-0.5 text-xs">common:refresh</code> string
          on real pages.)
        </p>
        <ExampleRow
          preview={
            <Button variant="outline" aria-label="Refresh">
              <RefreshCw className="h-4 w-4 sm:mr-2" />
              <span className="hidden sm:inline">Refresh</span>
            </Button>
          }
          importLine={`<Button variant="outline" onClick={() => void refetch()} disabled={isRefetching} aria-label={t("common:refresh")}>\n  <RefreshCw className={\`h-4 w-4 sm:mr-2 \${isRefetching ? "animate-spin" : ""}\`} />\n  <span className="hidden sm:inline">{t("common:refresh")}</span>\n</Button>`}
        />

        <h3 className="text-sm font-medium">Badge variants</h3>
        <ExampleRow
          preview={
            <>
              <Badge>Default</Badge>
              <Badge variant="secondary">Secondary</Badge>
              <Badge variant="success">Success</Badge>
              <Badge variant="warning">Warning</Badge>
              <Badge variant="destructive">Destructive</Badge>
              <Badge variant="outline">Outline</Badge>
            </>
          }
          importLine={`import { Badge } from "@/components/ui/badge";`}
        />

        <h3 className="text-sm font-medium">Button with &quot;Last used&quot; badge</h3>
        <p className="text-sm text-muted-foreground">
          The promoted-slot pattern used on the login page: a full-width action
          button carries an inline{" "}
          <code className="mx-1 rounded bg-muted px-1 py-0.5 text-xs">secondary</code>{" "}
          Badge marking the option a returning user picked last. Keep the badge
          last in the button and spaced with{" "}
          <code className="mx-1 rounded bg-muted px-1 py-0.5 text-xs">ml-2</code>.
        </p>
        <ExampleRow
          preview={
            <Button variant="outline" className="w-full">
              <KeyRound className="mr-2 h-4 w-4" />
              Sign in with passkey
              <Badge variant="secondary" className="ml-2">
                Last used
              </Badge>
            </Button>
          }
          importLine={`import { Button } from "@/components/ui/button";\nimport { Badge } from "@/components/ui/badge";\n\n<Button variant="outline" className="w-full">\n  <KeyRound className="mr-2 h-4 w-4" />\n  Sign in with passkey\n  <Badge variant="secondary" className="ml-2">Last used</Badge>\n</Button>`}
        />

        <h3 className="text-sm font-medium">Status dot</h3>
        <p className="text-sm text-muted-foreground">
          The small dot rendered beside a check name (listing) and the detail
          header. Colours come from the single source of truth{" "}
          <code className="mx-1 rounded bg-muted px-1 py-0.5 text-xs">statusStyle()</code>{" "}
          so the dot always matches the{" "}
          <code className="mx-1 rounded bg-muted px-1 py-0.5 text-xs">StatusBadge</code>{" "}
          beside it. A <strong>disabled</strong> check (
          <code className="mx-1 rounded bg-muted px-1 py-0.5 text-xs">enabled === false</code>)
          renders a neutral grey dot that overrides the last/live status colour, so a
          paused check no longer reads as "healthy & live". Pass a localized{" "}
          <code className="mx-1 rounded bg-muted px-1 py-0.5 text-xs">title</code> (the
          translated "Disabled") for the tooltip and accessible label.
        </p>
        <ExampleRow
          preview={
            <>
              <span className="inline-flex items-center gap-1.5 text-sm">
                <StatusDot status="up" /> Up
              </span>
              <span className="inline-flex items-center gap-1.5 text-sm">
                <StatusDot status="warning" /> Warning
              </span>
              <span className="inline-flex items-center gap-1.5 text-sm">
                <StatusDot status="down" /> Down
              </span>
              <span className="inline-flex items-center gap-1.5 text-sm">
                <StatusDot status="unknown" /> Unknown
              </span>
              <span className="inline-flex items-center gap-1.5 text-sm">
                <StatusDot status="up" enabled={false} title="Disabled" /> Disabled
              </span>
            </>
          }
          importLine={`import { StatusDot } from "@/components/shared/status-dot";\n\n<StatusDot\n  status={check.status ?? check.lastResult?.status}\n  enabled={check.enabled}\n  title={check.enabled === false ? t("checks:detail.disabled") : undefined}\n/>`}
        />
      </div>
    </Section>
  );
}

function FormsSection() {
  return (
    <Section
      id="forms"
      title="Forms"
      description="Inputs, controls, and the canonical assembled form. Each field group uses space-y-2 internally; spacing between fields is space-y-4."
    >
      <div className="space-y-4">
        <ExampleRow
          preview={
            <div className="w-full max-w-sm space-y-2">
              <Label htmlFor="dr-input">Input</Label>
              <Input id="dr-input" placeholder="A standard text input" />
            </div>
          }
          importLine={`import { Input } from "@/components/ui/input";\nimport { Label } from "@/components/ui/label";`}
        />

        <ExampleRow
          preview={
            <div className="w-full max-w-sm space-y-2">
              <Label htmlFor="dr-textarea">Textarea</Label>
              <Textarea id="dr-textarea" placeholder="Multi-line input" rows={3} />
            </div>
          }
          importLine={`import { Textarea } from "@/components/ui/textarea";`}
        />

        <ExampleRow
          preview={
            <div className="w-full max-w-sm space-y-2">
              <Label htmlFor="dr-password">Password input</Label>
              <PasswordInput id="dr-password" placeholder="••••••••" />
            </div>
          }
          importLine={`import { PasswordInput } from "@/components/ui/password-input";`}
        />

        <ExampleRow
          preview={
            <div className="w-full max-w-sm space-y-2">
              <Label htmlFor="dr-select">Select</Label>
              <Select>
                <SelectTrigger id="dr-select">
                  <SelectValue placeholder="Pick one" />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="alpha">Alpha</SelectItem>
                  <SelectItem value="beta">Beta</SelectItem>
                  <SelectItem value="gamma">Gamma</SelectItem>
                </SelectContent>
              </Select>
            </div>
          }
          importLine={`import {\n  Select,\n  SelectContent,\n  SelectItem,\n  SelectTrigger,\n  SelectValue,\n} from "@/components/ui/select";`}
        />

        <ExampleRow
          preview={
            <div className="flex items-center gap-2">
              <Checkbox id="dr-checkbox" />
              <Label htmlFor="dr-checkbox">Checkbox label</Label>
            </div>
          }
          importLine={`import { Checkbox } from "@/components/ui/checkbox";`}
        />

        <ExampleRow
          preview={
            <label className="flex items-center gap-2">
              <Checkbox id="dr-checkbox-hint" />
              <span className="text-sm">Use implicit TLS</span>
            </label>
          }
          importLine={`<label className="flex items-center gap-2">\n  <Checkbox checked={tls} onCheckedChange={(v) => setTls(v === true)} />\n  <span className="text-sm">Use implicit TLS</span>\n</label>\n<p className="text-xs text-muted-foreground">Port 993 uses implicit TLS.</p>`}
        />
        <p className="text-sm text-muted-foreground">
          A hint line under a checkbox is a plain{" "}
          <code className="rounded bg-muted px-1 py-0.5 text-xs">{'<p className="text-xs text-muted-foreground">'}</code>{" "}
          immediately below the label row — no dedicated hint/description
          component exists. Used e.g. by the IMAP/POP3 check form's
          port&harr;TLS auto-toggle affordance to explain why the toggle just
          flipped.
        </p>

        <ExampleRow
          preview={
            <div className="flex items-center gap-2">
              <Switch id="dr-switch" />
              <Label htmlFor="dr-switch">Switch label</Label>
            </div>
          }
          importLine={`import { Switch } from "@/components/ui/switch";`}
        />

        <h3 className="text-sm font-medium">Name + slug pair</h3>
        <p className="text-sm text-muted-foreground">
          When a resource has both a human-readable name and a URL slug, render{" "}
          <strong>Name first</strong> and auto-fill the slug from it via{" "}
          <code className="rounded bg-muted px-1 py-0.5 text-xs">slugify()</code>{" "}
          from <code className="rounded bg-muted px-1 py-0.5 text-xs">@/lib/utils</code>.
          Stop auto-filling once the user has manually edited the slug, so their
          override is never clobbered. Don't surface a separate "edit slug"
          toggle — letting them type into the slug field is enough.
        </p>
        <NameSlugExample />

        <h3 className="text-sm font-medium">Assembled form</h3>
        <div className="rounded-md border bg-card p-4">
          <form className="max-w-md space-y-4" onSubmit={(e) => e.preventDefault()}>
            <div className="space-y-2">
              <Label htmlFor="dr-name">Name</Label>
              <Input id="dr-name" placeholder="Acme Inc." />
              <p className="text-xs text-muted-foreground">
                Used as the display name across the dashboard.
              </p>
            </div>
            <div className="space-y-2">
              <Label htmlFor="dr-email">Email</Label>
              <Input
                id="dr-email"
                type="email"
                placeholder="contact@acme.test"
                aria-invalid="true"
                aria-describedby="dr-email-error"
              />
              <p id="dr-email-error" className="text-xs text-destructive">
                This is what an inline error message looks like.
              </p>
            </div>
            <div className="space-y-2">
              <Label htmlFor="dr-notes">Notes</Label>
              <Textarea id="dr-notes" rows={3} />
            </div>
            <div className="flex justify-end gap-2">
              <Button type="button" variant="outline">
                Cancel
              </Button>
              <Button type="submit">Save</Button>
            </div>
          </form>
        </div>
      </div>
    </Section>
  );
}

function NameSlugExample() {
  const [name, setName] = useState("");
  const [slug, setSlug] = useState("");
  const [slugManuallyEdited, setSlugManuallyEdited] = useState(false);
  return (
    <ExampleRow
      preview={
        <div className="grid w-full max-w-lg grid-cols-2 gap-4">
          <div className="space-y-2">
            <Label htmlFor="dr-name-slug-name">Name</Label>
            <Input
              id="dr-name-slug-name"
              value={name}
              onChange={(e) => {
                setName(e.target.value);
                if (!slugManuallyEdited) setSlug(slugify(e.target.value));
              }}
              placeholder="Production paging"
            />
          </div>
          <div className="space-y-2">
            <Label htmlFor="dr-name-slug-slug">Slug</Label>
            <Input
              id="dr-name-slug-slug"
              value={slug}
              onChange={(e) => {
                setSlug(e.target.value);
                setSlugManuallyEdited(true);
              }}
              placeholder="prod-paging"
            />
          </div>
        </div>
      }
      importLine={`import { slugify } from "@/lib/utils";\n\nconst [name, setName] = useState("");\nconst [slug, setSlug] = useState("");\nconst [slugManuallyEdited, setSlugManuallyEdited] = useState(false);\n\n<Input\n  value={name}\n  onChange={(e) => {\n    setName(e.target.value);\n    if (!slugManuallyEdited) setSlug(slugify(e.target.value));\n  }}\n/>\n<Input\n  value={slug}\n  onChange={(e) => {\n    setSlug(e.target.value);\n    setSlugManuallyEdited(true);\n  }}\n/>`}
    />
  );
}

type MockRow = {
  id: string;
  name: string;
  status: "up" | "down" | "warning" | "degraded";
  latency: string;
};

const MOCK_ROWS: MockRow[] = [
  { id: "1", name: "api.example.com", status: "up", latency: "120 ms" },
  { id: "2", name: "checkout-prod", status: "up", latency: "85 ms" },
  // "warning" is the live amber status ("up, but something to report");
  // "degraded" is its aggregated rollup counterpart — both render amber and
  // route through the shared statusStyle() util.
  { id: "3", name: "billing-staging", status: "warning", latency: "950 ms" },
  { id: "6", name: "cdn-edge (24h)", status: "degraded", latency: "210 ms" },
  { id: "4", name: "auth.example.com", status: "down", latency: "—" },
  { id: "5", name: "static.example.com", status: "up", latency: "42 ms" },
];

function MockTableHeader() {
  return (
    <TableHeader>
      <TableRow>
        <TableHead>Name</TableHead>
        <TableHead>Status</TableHead>
        <TableHead>Latency</TableHead>
      </TableRow>
    </TableHeader>
  );
}

function HappyTable() {
  const [search, setSearch] = useState("");
  const debouncedSearch = useDebounce(search, 300);
  const rows = useMemo(() => {
    const q = debouncedSearch.trim().toLowerCase();
    if (!q) return MOCK_ROWS;
    return MOCK_ROWS.filter((r) => r.name.toLowerCase().includes(q));
  }, [debouncedSearch]);

  return (
    <div className="space-y-3">
      <div className="relative max-w-xs">
        <Search className="absolute left-3 top-1/2 -translate-y-1/2 h-4 w-4 text-muted-foreground" />
        <Input
          placeholder="Search…"
          value={search}
          onChange={(e) => setSearch(e.target.value)}
          className="pl-9"
        />
      </div>
      <div className="rounded-md border">
        <Table>
          <MockTableHeader />
          <TableBody>
            {rows.length === 0 ? (
              <TableRow>
                <TableCell colSpan={3} className="py-8 text-center text-sm text-muted-foreground">
                  No matches.
                </TableCell>
              </TableRow>
            ) : (
              rows.map((row) => (
                <TableRow key={row.id}>
                  <TableCell className="font-medium">{row.name}</TableCell>
                  <TableCell>
                    <StatusBadge status={row.status} />
                  </TableCell>
                  <TableCell className="font-mono text-xs">{row.latency}</TableCell>
                </TableRow>
              ))
            )}
          </TableBody>
        </Table>
      </div>
    </div>
  );
}

function LoadingTable() {
  return (
    <div className="space-y-3">
      <Skeleton className="h-9 max-w-xs" />
      <div className="rounded-md border">
        <Table>
          <MockTableHeader />
          <TableBody>
            {Array.from({ length: 5 }).map((_, i) => (
              <TableRow key={i}>
                <TableCell colSpan={3}>
                  <Skeleton className="h-5 w-full" />
                </TableCell>
              </TableRow>
            ))}
          </TableBody>
        </Table>
      </div>
    </div>
  );
}

function EmptyTable() {
  return (
    <div className="space-y-3">
      <div className="relative max-w-xs">
        <Search className="absolute left-3 top-1/2 -translate-y-1/2 h-4 w-4 text-muted-foreground" />
        <Input placeholder="Search…" disabled className="pl-9" />
      </div>
      <div className="rounded-md border">
        <Table>
          <MockTableHeader />
          <TableBody>
            <TableRow>
              <TableCell colSpan={3} className="py-12 text-center text-sm text-muted-foreground">
                No items yet. Create the first one to get started.
              </TableCell>
            </TableRow>
          </TableBody>
        </Table>
      </div>
    </div>
  );
}

/** ClickableTable demonstrates the whole-row navigation pattern: the entire
 * <TableRow> is the click/keyboard target (no nested link or button), so the
 * row reads as a single `role="link"` to assistive tech. Used by list pages
 * whose rows open a detail route (e.g. discovery scans). In real code `onClick`
 * calls `navigate(...)`; here it's inert because the reference page has no live
 * detail target. */
function ClickableTable() {
  return (
    <div className="rounded-md border">
      <Table>
        <MockTableHeader />
        <TableBody>
          {MOCK_ROWS.slice(0, 3).map((row) => (
            <TableRow
              key={row.id}
              className="cursor-pointer hover:bg-muted/50"
              role="link"
              tabIndex={0}
              onClick={() => {
                /* navigate({ to: "/…/$uid", params: { uid: row.id } }) */
              }}
              onKeyDown={(e) => {
                if (e.key === "Enter" || e.key === " ") {
                  e.preventDefault();
                  /* navigate(...) */
                }
              }}
            >
              <TableCell className="font-medium">{row.name}</TableCell>
              <TableCell>
                <StatusBadge status={row.status} />
              </TableCell>
              <TableCell className="font-mono text-xs">{row.latency}</TableCell>
            </TableRow>
          ))}
        </TableBody>
      </Table>
    </div>
  );
}

function DataDisplaySection() {
  return (
    <Section
      id="data-display"
      title="Data display"
      description="The canonical list pattern: rounded-md border around the table, debounced search input, status badge per row. Three side-by-side variants — happy, loading, empty — because new features cut corners on these states."
    >
      <div className="grid gap-4 lg:grid-cols-3">
        <div className="space-y-2">
          <h3 className="text-sm font-medium">Happy path</h3>
          <HappyTable />
        </div>
        <div className="space-y-2">
          <h3 className="text-sm font-medium">Loading</h3>
          <LoadingTable />
        </div>
        <div className="space-y-2">
          <h3 className="text-sm font-medium">Empty</h3>
          <EmptyTable />
        </div>
      </div>
      <CodeSnippet
        code={`import {\n  Table,\n  TableBody,\n  TableCell,\n  TableHead,\n  TableHeader,\n  TableRow,\n} from "@/components/ui/table";\nimport { Skeleton } from "@/components/ui/skeleton";\nimport { useDebounce } from "@/lib/use-debounce";`}
      />

      <div className="space-y-2 pt-2">
        <h3 className="text-sm font-medium">Clickable rows</h3>
        <p className="text-sm text-muted-foreground">
          When a row opens a detail route, make the whole <code>TableRow</code>{" "}
          the target — not a trailing "View" link. Add{" "}
          <code>cursor-pointer hover:bg-muted/50</code> for affordance, and keep
          it keyboard-accessible with <code>role="link"</code>,{" "}
          <code>tabIndex=&#123;0&#125;</code>, and an Enter/Space{" "}
          <code>onKeyDown</code>. The row must contain no nested links or buttons
          so the click target is unambiguous.
        </p>
        <ClickableTable />
      </div>
      <CodeSnippet
        code={`import { useNavigate } from "@tanstack/react-router";\n\nconst navigate = useNavigate();\nconst open = () =>\n  void navigate({ to: "/orgs/$org/widgets/$uid", params: { org, uid: row.uid } });\n\n<TableRow\n  className="cursor-pointer hover:bg-muted/50"\n  role="link"\n  tabIndex={0}\n  onClick={open}\n  onKeyDown={(e) => {\n    if (e.key === "Enter" || e.key === " ") {\n      e.preventDefault();\n      open();\n    }\n  }}\n>\n  {/* cells — no nested <Link>/<Button> */}\n</TableRow>`}
      />
    </Section>
  );
}

/** ReferenceCollapsibleCode mirrors the CollapsibleCode used on the notification
 * delivery detail page: a native <details>/<summary> disclosure wrapping a
 * monospace block with a copy affordance. Keyboard-accessible and mobile-safe
 * with no JS-driven layout. Reuse this pattern for long, optional payloads
 * (request/response bodies, raw JSON) that should default to collapsed. */
function ReferenceCollapsibleCode({
  label,
  value,
  defaultOpen = false,
}: {
  label: string;
  value: string;
  defaultOpen?: boolean;
}) {
  const [copied, setCopied] = useState(false);

  const onCopy = async () => {
    try {
      await navigator.clipboard.writeText(value);
      setCopied(true);
      window.setTimeout(() => setCopied(false), 1500);
    } catch {
      // Clipboard requires a secure context; the value stays visible to copy by hand.
    }
  };

  return (
    <details className="group rounded-md border" open={defaultOpen}>
      <summary className="flex cursor-pointer list-none items-center justify-between gap-2 rounded-md px-3 py-2 text-sm hover:bg-accent">
        <span className="flex min-w-0 items-center gap-2">
          <ChevronRight className="h-4 w-4 shrink-0 transition-transform group-open:rotate-90" />
          <span className="truncate font-medium">{label}</span>
        </span>
        <button
          type="button"
          onClick={(e) => {
            e.preventDefault();
            void onCopy();
          }}
          aria-label={copied ? "Copied" : `Copy ${label}`}
          className="inline-flex h-7 w-7 shrink-0 items-center justify-center rounded-md text-muted-foreground transition-colors hover:bg-background hover:text-foreground"
        >
          {copied ? <Check className="h-4 w-4" /> : <Copy className="h-4 w-4" />}
        </button>
      </summary>
      <pre className="max-h-80 overflow-auto whitespace-pre-wrap break-words border-t bg-muted p-3 font-mono text-xs">
        {value}
      </pre>
    </details>
  );
}

function CollapsibleCodeSection() {
  return (
    <Section
      id="collapsible-code"
      title="Collapsible code"
      description="A native <details> disclosure wrapping a copyable monospace block. Use for long, optional payloads (webhook request/response bodies, raw JSON) that should default collapsed but stay one click from view. Keyboard- and touch-friendly; no extra dependency. Lives inline on the notification delivery detail page."
    >
      <div className="grid gap-3 rounded-md border bg-card p-4 md:grid-cols-[minmax(0,1fr)_minmax(0,1fr)] md:items-start">
        <div className="space-y-2">
          <ReferenceCollapsibleCode
            label="Request payload"
            value={`{\n  "type": "incident.created",\n  "data": { "incident": { "uid": "018e…" } }\n}`}
          />
          <ReferenceCollapsibleCode
            label="Response body"
            value={`{ "error": "service unavailable" }`}
            defaultOpen
          />
        </div>
        <CodeSnippet
          code={`// Native disclosure + copy. Default collapsed; pass defaultOpen for failures.\n<details className="group rounded-md border">\n  <summary>…<ChevronRight className="group-open:rotate-90" />…</summary>\n  <pre className="font-mono text-xs">{value}</pre>\n</details>`}
        />
      </div>
    </Section>
  );
}

function FeedbackSection() {
  return (
    <Section
      id="feedback"
      title="Feedback"
      description="Inline alerts, dialogs, tooltips, popovers, and toasts. Each is rendered live — open every one to verify behavior in both themes."
    >
      <div className="space-y-4">
        <h3 className="text-sm font-medium">Alert</h3>
        <ExampleRow
          preview={
            <div className="flex w-full max-w-md flex-col gap-2">
              <Alert>
                <Info />
                <AlertTitle>Default</AlertTitle>
                <AlertDescription>Neutral, informational message.</AlertDescription>
              </Alert>
              <Alert variant="success">
                <CheckCircle2 />
                <AlertTitle>Success</AlertTitle>
                <AlertDescription>The action completed successfully.</AlertDescription>
              </Alert>
              <Alert variant="warning">
                <AlertTriangle />
                <AlertTitle>Warning</AlertTitle>
                <AlertDescription>Something is degraded but still functional.</AlertDescription>
              </Alert>
              <Alert variant="destructive">
                <AlertCircle />
                <AlertTitle>Destructive</AlertTitle>
                <AlertDescription>An error occurred. Action failed.</AlertDescription>
              </Alert>
            </div>
          }
          importLine={`import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert";`}
        />

        <h3 className="text-sm font-medium">Dialog</h3>
        <ExampleRow
          preview={
            <Dialog>
              <DialogTrigger asChild>
                <Button variant="outline">Open dialog</Button>
              </DialogTrigger>
              <DialogContent>
                <DialogHeader>
                  <DialogTitle>Dialog title</DialogTitle>
                  <DialogDescription>Modal content for non-destructive flows.</DialogDescription>
                </DialogHeader>
                <p className="text-sm">
                  Use Dialog for inline forms, multi-step pickers, or any cancelable flow that
                  doesn&apos;t carry irreversible consequences.
                </p>
                <DialogFooter>
                  <Button variant="outline">Cancel</Button>
                  <Button>Confirm</Button>
                </DialogFooter>
              </DialogContent>
            </Dialog>
          }
          importLine={`import {\n  Dialog,\n  DialogContent,\n  DialogDescription,\n  DialogFooter,\n  DialogHeader,\n  DialogTitle,\n  DialogTrigger,\n} from "@/components/ui/dialog";`}
        />

        <h3 className="text-sm font-medium">Alert dialog</h3>
        <ExampleRow
          preview={
            <AlertDialog>
              <AlertDialogTrigger asChild>
                <Button variant="destructive">
                  <Trash2 />
                  Delete
                </Button>
              </AlertDialogTrigger>
              <AlertDialogContent>
                <AlertDialogHeader>
                  <AlertDialogTitle>Are you sure?</AlertDialogTitle>
                  <AlertDialogDescription>
                    This action is permanent. Use AlertDialog (not Dialog) for destructive
                    confirmations.
                  </AlertDialogDescription>
                </AlertDialogHeader>
                <AlertDialogFooter>
                  <AlertDialogCancel>Cancel</AlertDialogCancel>
                  <AlertDialogAction>Delete</AlertDialogAction>
                </AlertDialogFooter>
              </AlertDialogContent>
            </AlertDialog>
          }
          importLine={`import {\n  AlertDialog,\n  AlertDialogAction,\n  AlertDialogCancel,\n  AlertDialogContent,\n  AlertDialogDescription,\n  AlertDialogFooter,\n  AlertDialogHeader,\n  AlertDialogTitle,\n  AlertDialogTrigger,\n} from "@/components/ui/alert-dialog";`}
        />

        <h3 className="text-sm font-medium">Toast (sonner)</h3>
        <ExampleRow
          preview={
            <div className="flex flex-wrap gap-2">
              <Button variant="outline" onClick={() => toast("Hello from sonner")}>
                Default toast
              </Button>
              <Button
                variant="outline"
                onClick={() => toast.success("Saved successfully")}
              >
                Success toast
              </Button>
              <Button
                variant="outline"
                onClick={() => toast.error("Something went wrong")}
              >
                Error toast
              </Button>
            </div>
          }
          importLine={`import { toast } from "sonner";`}
        />

        <h3 className="text-sm font-medium">Tooltip</h3>
        <ExampleRow
          preview={
            <TooltipProvider>
              <Tooltip>
                <TooltipTrigger asChild>
                  <Button variant="outline">Hover me</Button>
                </TooltipTrigger>
                <TooltipContent>This is a tooltip.</TooltipContent>
              </Tooltip>
            </TooltipProvider>
          }
          importLine={`import {\n  Tooltip,\n  TooltipContent,\n  TooltipProvider,\n  TooltipTrigger,\n} from "@/components/ui/tooltip";`}
        />

        <h3 className="text-sm font-medium">Popover</h3>
        <ExampleRow
          preview={
            <Popover>
              <PopoverTrigger asChild>
                <Button variant="outline">Open popover</Button>
              </PopoverTrigger>
              <PopoverContent className="p-3">
                <p className="text-sm">
                  Popover content — useful for inline pickers and contextual hints that need
                  more space than a tooltip.
                </p>
              </PopoverContent>
            </Popover>
          }
          importLine={`import {\n  Popover,\n  PopoverContent,\n  PopoverTrigger,\n} from "@/components/ui/popover";`}
        />

        <h3 className="text-sm font-medium">Dropdown menu</h3>
        <ExampleRow
          preview={
            <DropdownMenu>
              <DropdownMenuTrigger asChild>
                <Button variant="outline" size="icon" aria-label="Row actions">
                  <MoreVertical />
                </Button>
              </DropdownMenuTrigger>
              <DropdownMenuContent align="end">
                <DropdownMenuLabel>Actions</DropdownMenuLabel>
                <DropdownMenuSeparator />
                <DropdownMenuItem>
                  <Eye />
                  View
                </DropdownMenuItem>
                <DropdownMenuItem>
                  <Pencil />
                  Edit
                </DropdownMenuItem>
                <DropdownMenuSeparator />
                <DropdownMenuItem className="text-destructive focus:text-destructive">
                  <Trash2 />
                  Delete
                </DropdownMenuItem>
              </DropdownMenuContent>
            </DropdownMenu>
          }
          importLine={`import {\n  DropdownMenu,\n  DropdownMenuContent,\n  DropdownMenuItem,\n  DropdownMenuLabel,\n  DropdownMenuSeparator,\n  DropdownMenuTrigger,\n} from "@/components/ui/dropdown-menu";`}
        />
      </div>
    </Section>
  );
}

function BrandSection() {
  return (
    <Section
      id="brand"
      title="Brand"
      description="The SolidPing mark, plus the brand-color tokens reserved for chrome (logo tile, header strips, marketing accents). Brand color is never used as an interactive affordance in the operator UI — the rule that keeps brand-pink from competing with destructive / status-error reds."
    >
      <div className="space-y-2">
        <h3 className="text-sm font-medium">Logo sizes</h3>
        <div className="flex flex-wrap items-end gap-6">
          {[16, 24, 32, 48, 64, 96].map((size) => (
            <div key={size} className="flex flex-col items-center gap-2">
              <Logo size={size} />
              <span className="text-xs text-muted-foreground">{size}px</span>
            </div>
          ))}
        </div>
      </div>
      <div className="space-y-2">
        <h3 className="text-sm font-medium">Wordmark variant</h3>
        <Logo size={32} variant="wordmark" />
      </div>
      <div className="space-y-2">
        <h3 className="text-sm font-medium">
          Brand swatches (kept distinct from primary / destructive / status-error)
        </h3>
        <div className="grid gap-3 sm:grid-cols-3">
          <Swatch
            varName="--brand"
            label="brand"
            description="Logo tile, header strips, marketing CTAs"
          />
          <Swatch
            varName="--brand-foreground"
            label="brand-foreground"
            description="Text on brand-colored chrome"
          />
          <Swatch
            varName="--brand-muted"
            label="brand-muted"
            description="Soft brand wash for hero strips"
          />
        </div>
      </div>
    </Section>
  );
}

function ElevationSection() {
  return (
    <Section
      id="elevation"
      title="Elevation, aurora & glass"
      description="Depth tokens that add polish without adding a new color. Tinted shadows carry the primary hue and are already baked into Button's default variant and Card — reach for the utilities only when styling a bespoke surface. The aurora panel + glass utility are for marketing surfaces ONLY (login split-screen, hero strips, empty-state splashes) — never operator data views."
    >
      <div className="space-y-2">
        <h3 className="text-sm font-medium">
          Tinted elevation (shadows carry the primary hue, auto-adapt to dark)
        </h3>
        <ExampleRow
          preview={
            <>
              <div className="rounded-xl border bg-card px-4 py-3 text-sm shadow-card">
                shadow-card
              </div>
              <div className="rounded-lg bg-primary px-4 py-3 text-sm text-primary-foreground shadow-primary">
                shadow-primary
              </div>
            </>
          }
          importLine={`// Defined in index.css @theme; color-mix keeps them tracking --primary.\n<div className="shadow-card" />      {/* cards, KPI tiles — soft ambient lift */}\n<Button className="shadow-primary" /> {/* primary CTA glow (default variant has it) */}\n<Card className="hover:shadow-card-hover transition" /> {/* lift on hover */}`}
        />
      </div>

      <div className="space-y-2">
        <h3 className="text-sm font-medium">Aurora panel + glass card</h3>
        <ExampleRow
          preview={
            <AuroraPanel className="h-52 w-full rounded-xl">
              <div className="flex h-full items-center justify-center p-6">
                <div className="glass space-y-1 rounded-2xl p-5">
                  <p className="text-sm font-semibold text-white">
                    Glass on aurora
                  </p>
                  <p className="text-xs text-white/70">
                    white/8 fill · blur 12 · white/18 border
                  </p>
                </div>
              </div>
            </AuroraPanel>
          }
          importLine={`import { AuroraPanel } from "@/components/ui/aurora-panel";\n\n// Marketing surfaces only. Always dark; renders white text. The \`glass\`\n// utility (index.css) is the frosted card that sits on top.\n<AuroraPanel className="min-h-screen p-12">\n  <div className="glass rounded-3xl p-8">…</div>\n</AuroraPanel>\n\n// Auth pages: don't hand-roll the panel — wrap your card in AuthSplitLayout,\n// which renders this aurora + the marketing copy + the theme toggle.\nimport { AuthSplitLayout } from "@/components/layout/auth-split-layout";`}
        />
      </div>
    </Section>
  );
}

function LabelFilterSection() {
  const { org } = Route.useParams();
  const [labels, setLabels] = useState<Record<string, string>>({
    env: "prod",
  });
  const snippet = `import { LabelFilter } from "@/components/shared/label-filter";

// Faceted label picker for list toolbars. Applied filters render as removable
// chips; the compact "+ Label" trigger opens one popover with a guided two-step
// cmdk list (pick a key, then pick/type a value). Selecting a value applies
// immediately — no Add button. The caller owns URL serialization via onChange.
<LabelFilter
  org={org}
  value={labelFilters}
  onChange={(next) => {
    const serialized = serializeLabelsParam(next);
    void navigate({
      search: (prev) => ({ ...prev, labels: serialized || undefined }),
      replace: true,
    });
  }}
/>`;
  return (
    <Section
      id="label-filter"
      title="Label filter"
      description="Faceted key:value filter used in the checks-list toolbar. Reuse this instead of LabelInput when filtering a list (LabelInput stays for authoring labels in a form). Applied filters are removable chips; the compact + Label trigger opens a single popover with a two-step key→value cmdk picker that applies on select. Try it below."
    >
      <ExampleRow
        preview={
          <LabelFilter org={org} value={labels} onChange={setLabels} />
        }
        importLine={snippet}
      />
    </Section>
  );
}

function KpiTileSection() {
  const { org } = Route.useParams();
  const snippet = `import { Link } from "@tanstack/react-router";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";

// Wrap in <Link> for clickable tiles; omit the wrapper for static metrics.
<Link to="/orgs/$org/checks" params={{ org }} className="block">
  <Card className="transition hover:-translate-y-0.5 hover:bg-accent/40 hover:shadow-card-hover">
    <CardHeader className="flex flex-row items-center justify-between space-y-0 pb-2">
      <CardTitle className="text-sm font-medium text-muted-foreground">Monitored</CardTitle>
      <Icon className="h-4 w-4 text-muted-foreground" />
    </CardHeader>
    <CardContent>
      <div className="text-3xl font-bold">42</div>
      <p className="text-xs text-muted-foreground mt-1">2 disabled</p>
    </CardContent>
  </Card>
</Link>`;
  return (
    <Section
      id="kpi-tiles"
      title="KPI tiles"
      description="Large-number summary cards used on the org dashboard. Link tiles 1–3 to drill-down list pages; leave purely metric tiles (e.g. % availability) static. Clickable tiles lift on hover (hover:-translate-y-0.5 hover:bg-accent/40 hover:shadow-card-hover) — no nested interactive elements inside."
    >
      <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-3">
        <Link
          to="/orgs/$org/checks"
          params={{ org }}
          className="block"
        >
          <Card className="transition hover:-translate-y-0.5 hover:bg-accent/40 hover:shadow-card-hover">
            <CardHeader className="flex flex-row items-center justify-between space-y-0 pb-2">
              <CardTitle className="text-sm font-medium text-muted-foreground">
                Monitored
              </CardTitle>
              <AlertTriangle className="h-4 w-4 text-muted-foreground" />
            </CardHeader>
            <CardContent>
              <div className="text-3xl font-bold">42</div>
              <p className="text-xs text-muted-foreground mt-1">2 disabled</p>
            </CardContent>
          </Card>
        </Link>
        <Card>
          <CardHeader className="flex flex-row items-center justify-between space-y-0 pb-2">
            <CardTitle className="text-sm font-medium text-muted-foreground">
              Availability (static)
            </CardTitle>
            <AlertTriangle className="h-4 w-4 text-muted-foreground" />
          </CardHeader>
          <CardContent>
            <div className="text-3xl font-bold">99.98%</div>
            <p className="text-xs text-muted-foreground mt-1">24h window</p>
          </CardContent>
        </Card>
      </div>
      <CodeSnippet code={snippet} />
    </Section>
  );
}

function UptimeStripSection() {
  // 24 hourly buckets, oldest → newest. A mix of full uptime, a partial hour,
  // a down hour, and a couple of no-data hours to exercise every cell color.
  const buckets = Array.from({ length: 24 }, (_, i) => {
    const periodStart = new Date(
      Date.now() - (23 - i) * 60 * 60 * 1000,
    ).toISOString();
    let availabilityPct: number | undefined = 100;
    if (i === 22 || i === 23) availabilityPct = undefined; // most recent hours: no data yet
    else if (i === 10) availabilityPct = 0; // a full outage hour
    else if (i === 11) availabilityPct = 66.7; // a partially-degraded hour
    return {
      periodStart,
      availabilityPct,
      durationMs: availabilityPct === undefined ? undefined : 120 + i,
    };
  });
  const snippet = `import { UptimeStrip } from "@/components/ui/uptime-strip";

// Pure presentational: pass hourly buckets (oldest → newest), no fetching.
// Cell color: green at 100%, yellow in between, red at 0%, gray for no data.
<UptimeStrip
  buckets={[
    { periodStart, availabilityPct, durationMs },
    // ...one per hour
  ]}
/>`;
  return (
    <Section
      id="uptime-strip"
      title="Uptime strip"
      description="A compact 24-hour availability sparkline used in the dashboard's Checks-at-a-glance rows. One cell per hour, oldest → newest; color comes from each bucket's availabilityPct. Hover a cell for the hour, availability %, and average latency. Presentational only — group results by check and pass buckets down."
    >
      <div className="rounded-md border bg-card p-4 space-y-4">
        <UptimeStrip buckets={buckets} />
        <p className="text-xs text-muted-foreground">
          Mostly green with one outage hour (red), one degraded hour (yellow),
          and the two most recent hours awaiting data (gray).
        </p>
      </div>
      <CodeSnippet code={snippet} />
    </Section>
  );
}

function ColorTokensSection() {
  return (
    <Section
      id="color-tokens"
      title="Color tokens"
      description="Use the CSS variable token, not a hex. Tailwind classes like bg-primary, text-destructive, and bg-muted resolve to these tokens — they swap correctly in dark mode."
    >
      <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-3">
        {COLOR_TOKENS.map((t) => (
          <Swatch key={t.varName} varName={t.varName} label={t.name} description={t.description} />
        ))}
      </div>
      <div className="space-y-2">
        <h3 className="text-sm font-medium">Chart palette</h3>
        <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-5">
          {CHART_TOKENS.map((v, i) => (
            <Swatch key={v} varName={v} label={`chart-${i + 1}`} />
          ))}
        </div>
      </div>
    </Section>
  );
}

function CheckMultiPickerSection() {
  const { org } = Route.useParams();
  const [checkUids, setCheckUids] = useState<string[]>([]);
  const [groupUids, setGroupUids] = useState<string[]>([]);

  return (
    <Section
      id="check-multi-picker"
      title="Check multi-picker"
      description="Multi-select for checks or check groups, parameterized by kind. Selected items render as removable Badge chips. Used by the maintenance-window form (which needs multiple checks and multiple groups). Mirrors the single-value CheckPicker."
    >
      <p className="text-xs text-muted-foreground">
        import {"{ CheckMultiPicker }"} from "@/components/shared/check-multi-picker"
      </p>
      <div className="grid gap-4 sm:grid-cols-2 max-w-2xl">
        <div className="space-y-2">
          <Label>Checks</Label>
          <CheckMultiPicker org={org} kind="checks" value={checkUids} onChange={setCheckUids} />
        </div>
        <div className="space-y-2">
          <Label>Check groups</Label>
          <CheckMultiPicker org={org} kind="groups" value={groupUids} onChange={setGroupUids} />
        </div>
      </div>
    </Section>
  );
}

function JobsPrimitivesSection() {
  const [tab, setTab] = useState("first");

  return (
    <Section
      id="jobs-primitives"
      title="Jobs primitives"
      description="Building blocks for the admin Jobs observability page: compact stat tiles for the activity overview, in-page Tabs (dependency-free), and a read-only JSON viewer for config/output blocks."
    >
      <div className="space-y-2">
        <h3 className="text-sm font-medium">Stat tile</h3>
        <p className="text-xs text-muted-foreground">
          import {"{ StatTile }"} from "@/components/shared/stat-tile"
        </p>
        <div className="grid grid-cols-2 gap-2 sm:grid-cols-4">
          <StatTile label="Pending" value={3} tone="info" />
          <StatTile label="Running" value={1} tone="info" />
          <StatTile label="Failed (24h)" value={2} tone="destructive" />
          <StatTile label="In-flight" value={5} tone="success" />
        </div>
      </div>

      <div className="space-y-2">
        <h3 className="text-sm font-medium">Tabs</h3>
        <p className="text-xs text-muted-foreground">
          import {"{ Tabs, TabsList, TabsTrigger, TabsContent }"} from "@/components/ui/tabs"
        </p>
        <Tabs value={tab} onValueChange={setTab}>
          <TabsList>
            <TabsTrigger value="first">First</TabsTrigger>
            <TabsTrigger value="second">Second</TabsTrigger>
          </TabsList>
          <TabsContent value="first">
            <div className="rounded-md border bg-card p-4 text-sm">First panel</div>
          </TabsContent>
          <TabsContent value="second">
            <div className="rounded-md border bg-card p-4 text-sm">Second panel</div>
          </TabsContent>
        </Tabs>
      </div>

      <div className="space-y-2">
        <h3 className="text-sm font-medium">JSON viewer</h3>
        <p className="text-xs text-muted-foreground">
          import {"{ JsonViewer }"} from "@/components/shared/json-viewer"
        </p>
        <JsonViewer value={{ url: "https://example.com", timeout: 5000 }} />
      </div>
    </Section>
  );
}

function MaintenanceScheduleSection() {
  const [weekday, setWeekday] = useState(3); // Wed
  const [durationValue, setDurationValue] = useState("1");
  const [durationUnit, setDurationUnit] = useState("hours");

  // Mon..Sun, labelled via Intl from a reference week (2024-01-01 is a Monday).
  const weekdayOrder = [1, 2, 3, 4, 5, 6, 0];
  const weekdayLabel = (dow: number) =>
    new Date(2024, 0, 1 + ((dow - 1 + 7) % 7)).toLocaleDateString(undefined, {
      weekday: "short",
    });

  // A sample weekly window for the summary panel preview. Computed once (the
  // current date is read in an effect-free memo to avoid impure render calls).
  const sampleWindow = useMemo(() => {
    const now = new Date();
    const sampleStart = new Date(
      Date.UTC(now.getUTCFullYear(), now.getUTCMonth(), now.getUTCDate(), 22, 0),
    );
    return {
      uid: "dr-sample",
      title: "Weekly DB backup",
      startAt: sampleStart.toISOString(),
      endAt: new Date(sampleStart.getTime() + 3600000).toISOString(),
      recurrence: "weekly" as const,
      createdAt: now.toISOString(),
      updatedAt: now.toISOString(),
    };
  }, []);

  return (
    <Section
      id="maintenance-schedule"
      title="Maintenance schedule"
      description="Recurrence-aware controls used by the maintenance-window form: a single-select weekday chip row, a number+unit duration input, and the plain-language schedule summary panel (which also previews the next occurrences)."
    >
      <div className="space-y-2">
        <h3 className="text-sm font-medium">Weekday chips (single-select)</h3>
        <p className="text-xs text-muted-foreground">
          Built from {"<Button>"} — variant "default" when selected, "outline"
          otherwise, with aria-pressed.
        </p>
        <div className="flex flex-wrap gap-2" role="group">
          {weekdayOrder.map((dow) => (
            <Button
              key={dow}
              type="button"
              size="sm"
              variant={weekday === dow ? "default" : "outline"}
              aria-pressed={weekday === dow}
              onClick={() => setWeekday(dow)}
            >
              {weekdayLabel(dow)}
            </Button>
          ))}
        </div>
      </div>

      <div className="space-y-2">
        <h3 className="text-sm font-medium">Duration input (number + unit)</h3>
        <p className="text-xs text-muted-foreground">
          {"<Input type=\"number\">"} paired with a unit {"<Select>"}.
        </p>
        <div className="flex gap-2 max-w-xs">
          <Input
            type="number"
            min={1}
            step={1}
            value={durationValue}
            onChange={(e) => setDurationValue(e.target.value)}
            className="max-w-[7rem]"
          />
          <Select value={durationUnit} onValueChange={setDurationUnit}>
            <SelectTrigger className="flex-1">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="minutes">minutes</SelectItem>
              <SelectItem value="hours">hours</SelectItem>
            </SelectContent>
          </Select>
        </div>
      </div>

      <div className="space-y-2">
        <h3 className="text-sm font-medium">Schedule summary panel</h3>
        <p className="text-xs text-muted-foreground">
          import{" "}
          {"{ MaintenanceScheduleSummary }"} from
          "@/components/shared/maintenance-schedule-summary"
        </p>
        <MaintenanceScheduleSummary window={sampleWindow} />
      </div>
    </Section>
  );
}
