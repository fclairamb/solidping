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
  ArrowUpRight,
  Bot,
  Building,
  Check,
  CheckCircle2,
  ChevronDown,
  ChevronRight,
  Copy,
  Eye,
  Info,
  Building2,
  KeyRound,
  Layers,
  Loader2,
  LogOut,
  Moon,
  Monitor,
  Globe,
  Inbox,
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
  Upload,
  Wand2,
} from "lucide-react";
import { toast } from "sonner";

import {
  EventTypeBadge,
  getEventTone,
} from "@/components/dashboard/event-display";
import {
  CheckTypeBadge,
  CheckTypeIcon,
} from "@/components/shared/check-type-identity";
import { CheckMultiPicker } from "@/components/shared/check-multi-picker";
import { CheckGroupPicker } from "@/components/shared/check-group-picker";
import { RecipientsInput } from "@/components/shared/recipients-input";
import { TokenChipsInput } from "@/components/shared/token-chips-input";
import {
  isValidStatusPattern,
  normalizeStatusPattern,
} from "@/lib/http-status";
import {
  JsonAssertionEditor,
  type AssertionNode,
} from "@/components/checks/json-assertion-editor";
import {
  CollapsibleCode,
  CopyableCode,
  CopyableInline,
} from "@/components/shared/copyable-code";
import { DnsRecordRow } from "@/components/shared/dns-record-row";
import { DocsLink } from "@/components/shared/docs-link";
import { LiveDurationAgo } from "@/components/shared/relative-time";
import { TimeAgo } from "@/components/ui/time-ago";
import { ErrorFallbackCard } from "@/components/shared/error-boundary";
import { MaintenanceScheduleSummary } from "@/components/shared/maintenance-schedule-summary";
import { JsonViewer } from "@/components/shared/json-viewer";
import { LabelFilter } from "@/components/shared/label-filter";
import { FacetedFilter } from "@/components/shared/faceted-filter";
import { OnboardingChecklistCard } from "@/components/dashboard/onboarding-checklist";
import { PageHeader } from "@/components/shared/page-header";
import { CheckRateLimitBanner } from "@/components/shared/check-rate-limit-banner";
import { CheckRateMeter } from "@/components/shared/check-rate-meter";
import { StatTile } from "@/components/shared/stat-tile";
import { StatusBadge } from "@/components/shared/status-badge";
import { StatusDot } from "@/components/shared/status-dot";
import { SupportMessageBubble } from "@/components/support/message-bubble";
import { Ipv6CapabilityBadge } from "@/components/shared/ipv6-capability";
import { BrowserCapabilityIcon } from "@/components/shared/browser-capability";
import { FlappingBadge } from "@/components/shared/flapping-badge";
import {
  formatBudgetSeconds,
  sloBudgetBarClass,
  sloStateBadgeClass,
} from "@/lib/slo-format";
import { BudgetBurndownChart } from "@/components/slos/budget-burndown-chart";
import type { SloBurndown } from "@/api/hooks";
import { AgentVersionCell } from "@/components/shared/agent-version";
import { LiveStatusDot } from "@/components/layout/live-status-dot";
import { ServerVersionIndicator } from "@/components/layout/server-version-indicator";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert";
import {
  ConfirmByTypingButton,
  DangerZone,
} from "@/components/shared/danger-zone";
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
import { SegmentedControl } from "@/components/ui/segmented-control";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
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
import {
  Popover,
  PopoverContent,
  PopoverTrigger,
} from "@/components/ui/popover";
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
import { Stepper } from "@/components/ui/stepper";
import {
  PagingCoverageCell,
  EmailOnlyBadge,
} from "@/components/notifications/member-coverage";
import { Switch } from "@/components/ui/switch";
import { CollapsibleSection } from "@/components/ui/collapsible-section";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import { Textarea } from "@/components/ui/textarea";
import { CodeTextarea } from "@/components/ui/code-textarea";
import { UptimeStrip } from "@/components/ui/uptime-strip";
import { AvailabilityStrip } from "@/components/ui/availability-strip";
import { useIsDarkTheme } from "@/hooks/use-is-dark-theme";
import { useDebounce } from "@/lib/use-debounce";
import { facetedFilterTriggerLabel } from "@/lib/faceted-filter";
import { cn, slugify } from "@/lib/utils";

export const Route = createFileRoute("/orgs/$org/design-reference")({
  component: DesignReferencePage,
});

// Frozen at module load: calling Date.now() inside the render would be an
// impure read, and these are decorative sample timestamps either way.
const SUPPORT_BUBBLE_INBOUND_AT = new Date(Date.now() - 300_000).toISOString();
const SUPPORT_BUBBLE_OUTBOUND_AT = new Date(Date.now() - 120_000).toISOString();

// Computed once at module scope (not inside a component render) so the
// "N ago" showcase below stays a pure render — see the LiveDurationAgo
// example.
const RELATIVE_TIME_DEMO_SINCE = new Date(
  Date.now() - 5 * 60_000,
).toISOString();
const TIME_AGO_DEMO_DATE = new Date(Date.now() - 46 * 60_000).toISOString();

const SECTIONS: { id: string; label: string }[] = [
  { id: "overview", label: "Overview" },
  { id: "conventions", label: "Conventions" },
  { id: "page-header", label: "Page header" },
  { id: "button-placement", label: "Button placement" },
  { id: "docs-link", label: "Docs link" },
  { id: "breadcrumbs", label: "Breadcrumbs" },
  { id: "color-tokens", label: "Color tokens" },
  { id: "brand", label: "Brand" },
  { id: "elevation", label: "Elevation & aurora" },
  { id: "buttons-badges", label: "Buttons & badges" },
  { id: "check-type-badge", label: "Check type identity" },
  { id: "event-tone", label: "Event tone badge" },
  { id: "live-dot", label: "Live & pulse dots" },
  { id: "forms", label: "Forms" },
  { id: "data-display", label: "Data display" },
  { id: "responsive-table", label: "Responsive table" },
  { id: "list-surface", label: "List surface" },
  { id: "copyable-code", label: "Copyable code" },
  { id: "copyable-inline", label: "Copyable inline" },
  { id: "dns-record-row", label: "DNS record row" },
  { id: "collapsible-code", label: "Collapsible code" },
  { id: "collapsible-section", label: "Collapsible section" },
  { id: "sandboxed-preview", label: "Sandboxed preview (iframe)" },
  { id: "stepper", label: "Stepper" },
  { id: "feedback", label: "Feedback" },
  { id: "label-filter", label: "Label filter" },
  { id: "faceted-filter", label: "Faceted filter" },
  { id: "check-multi-picker", label: "Check multi-picker" },
  { id: "check-group-picker", label: "Check group picker" },
  { id: "token-chips-input", label: "Token chips input" },
  { id: "kpi-tiles", label: "KPI tiles" },
  { id: "clickable-status-banner", label: "Clickable status banner" },
  { id: "uptime-strip", label: "Uptime strip" },
  { id: "availability-strip", label: "Availability strip" },
  { id: "jobs-primitives", label: "Jobs primitives" },
  { id: "maintenance-schedule", label: "Maintenance schedule" },
  { id: "stats-strip", label: "Stats strip" },
  { id: "swatch-legend-chips", label: "Swatch-legend chips" },
  { id: "paging-coverage", label: "Paging coverage" },
  { id: "onboarding-checklist", label: "Onboarding checklist" },
  { id: "magic-wand", label: "Magic wand" },
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
      <ButtonPlacementSection />
      <DocsLinkSection />
      <BreadcrumbsSection />
      <ColorTokensSection />
      <BrandSection />
      <ElevationSection />
      <ButtonsBadgesSection />
      <CheckTypeIdentitySection />
      <EventToneSection />
      <LiveDotSection />
      <FormsSection />
      <DataDisplaySection />
      <ResponsiveTableSection />
      <ListSurfaceSection />
      <CopyableCodeSection />
      <CopyableInlineSection />
      <DnsRecordRowSection />
      <CollapsibleCodeSection />
      <EvidenceImageSection />
      <CollapsibleSectionSection />
      <SandboxedPreviewSection />
      <StepperSection />
      <FeedbackSection />
      <LabelFilterSection />
      <FacetedFilterSection />
      <CheckMultiPickerSection />
      <CheckGroupPickerSection />
      <TokenChipsInputSection />
      <JsonAssertionEditorSection />
      <KpiTileSection />
      <ClickableStatusBannerSection />
      <UptimeStripSection />
      <AvailabilityStripSection />
      <JobsPrimitivesSection />
      <MaintenanceScheduleSection />
      <StatsStripSection />
      <SwatchLegendChipsSection />
      <PagingCoverageSection />
      <OnboardingChecklistSection />
      <MagicWandSection />
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
          <h3 className="text-sm font-semibold">
            Editing always changes the route
          </h3>
          <p className="text-sm text-muted-foreground">
            Editing an entity must navigate to a dedicated route, never open a
            modal dialog. Mirror the create flow:{" "}
            <code className="rounded bg-muted px-1 py-0.5 text-xs">
              /&lt;resource&gt;/new
            </code>{" "}
            for creation,{" "}
            <code className="rounded bg-muted px-1 py-0.5 text-xs">
              /&lt;resource&gt;/$id/edit
            </code>{" "}
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
          <h3 className="text-sm font-semibold">
            Row actions: icons, not menus
          </h3>
          <p className="text-sm text-muted-foreground">
            In list/table rows, prefer two ghost icon buttons (
            <code className="rounded bg-muted px-1 py-0.5 text-xs">Pencil</code>{" "}
            for edit,{" "}
            <code className="rounded bg-muted px-1 py-0.5 text-xs">Trash2</code>{" "}
            for delete with{" "}
            <code className="rounded bg-muted px-1 py-0.5 text-xs">
              text-destructive
            </code>
            ) over a{" "}
            <code className="rounded bg-muted px-1 py-0.5 text-xs">
              DropdownMenu
            </code>{" "}
            with a{" "}
            <code className="rounded bg-muted px-1 py-0.5 text-xs">
              MoreVertical
            </code>{" "}
            trigger. The Edit icon links to the edit route; the Delete icon
            opens an{" "}
            <code className="rounded bg-muted px-1 py-0.5 text-xs">
              AlertDialog
            </code>
            . Other per-row actions live on the edit page, not in the row.
          </p>
        </div>
        <div className="rounded-md border bg-card p-4 space-y-3">
          <h3 className="text-sm font-semibold">
            Delete is always red, always a trash bin
          </h3>
          <p className="text-sm text-muted-foreground">
            Every delete (or otherwise irreversible) action is rendered in red
            and paired with the{" "}
            <code className="rounded bg-muted px-1 py-0.5 text-xs">Trash2</code>{" "}
            (trash bin) icon — no exceptions. Use a{" "}
            <code className="rounded bg-muted px-1 py-0.5 text-xs">
              Button variant=&quot;destructive&quot;
            </code>{" "}
            for standalone/prominent buttons, an icon button with{" "}
            <code className="rounded bg-muted px-1 py-0.5 text-xs">
              text-destructive
            </code>{" "}
            in row actions, and{" "}
            <code className="rounded bg-muted px-1 py-0.5 text-xs">
              text-destructive focus:text-destructive
            </code>{" "}
            on the delete item inside a{" "}
            <code className="rounded bg-muted px-1 py-0.5 text-xs">
              DropdownMenu
            </code>
            . Both colors resolve to the{" "}
            <code className="rounded bg-muted px-1 py-0.5 text-xs">
              --destructive
            </code>{" "}
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

// Static burn-down fixture for the design reference. Fixed timestamps (not
// Date.now()) so the rendered example is identical on every visit.
const designReferenceBurndown: SloBurndown = {
  window: {
    start: "2026-08-01T00:00:00Z",
    end: "2026-09-01T00:00:00Z",
    label: "2026-08",
  },
  targetPct: 99.9,
  budgetTotalSeconds: 2678,
  data: [0, 1, 2, 3, 4, 5, 6].map((day) => ({
    at: new Date(Date.UTC(2026, 7, day + 1)).toISOString(),
    budgetRemainingSeconds: 2678 - day * 380,
    idealRemainingSeconds: Math.round(2678 * (1 - (day + 1) / 31)),
    attainmentPct: 99.94 - day * 0.01,
    hasData: true,
  })),
};

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
  // Both tracks are minmax(0,…) and both children carry min-w-0: a grid item
  // defaults to min-width:auto, so without this a preview whose intrinsic
  // min-content is wider than the column (e.g. a max-w-md card at 375px)
  // widens the track and overflows the page instead of wrapping.
  return (
    <div className="grid gap-3 rounded-md border bg-card p-4 md:grid-cols-[minmax(0,1fr)_minmax(0,1fr)] md:items-start">
      <div className="flex min-w-0 flex-wrap items-center gap-2">{preview}</div>
      <div className="min-w-0">
        <CodeSnippet code={importLine} />
      </div>
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
          Tip: open{" "}
          <code className="rounded bg-muted px-1 py-0.5 text-xs">
            web/dash0/src/routes/orgs/$org/design-reference.tsx
          </code>{" "}
          when copying patterns — many examples here lift directly from real
          routes (e.g.{" "}
          <code className="rounded bg-muted px-1 py-0.5 text-xs">
            checks.index.tsx
          </code>
          ) and the source file is the canonical reference.
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
  docsHref="/docs/features/status-pages"
  className="flex-wrap"
/>`;
  return (
    <Section
      id="page-header"
      title="Page header"
      description="Every page opens with a page header — the page title plus its right-aligned actions. 'Page title' and 'page header' are the same surface, not two primitives. List and section pages render it with the boxed PageHeader component (@/components/shared/page-header); detail and edit pages compose the same header inline so it can carry a back arrow and per-record actions. Both patterns are documented here."
    >
      <h3 className="text-sm font-medium">
        List &amp; section pages: the PageHeader component
      </h3>
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
          docsHref="/docs/features/status-pages"
          className="flex-wrap"
        />
      </div>
      <CodeSnippet code={pageHeaderSnippet} />
      <p className="text-sm text-muted-foreground">
        Notes: pass the same per-page Lucide icon you would have rendered inline
        —{" "}
        <code className="rounded bg-muted px-1 py-0.5 text-xs">PageHeader</code>{" "}
        wraps it in the muted tile for you. Put the primary action(s) that used
        to sit in the header row (e.g.{" "}
        <code className="rounded bg-muted px-1 py-0.5 text-xs">+ New X</code>,
        export/import, a refresh button) into the{" "}
        <code className="rounded bg-muted px-1 py-0.5 text-xs">actions</code>{" "}
        prop; leave filter/search toolbars on their own row below the header.
        Add{" "}
        <code className="rounded bg-muted px-1 py-0.5 text-xs">
          className="flex-wrap"
        </code>{" "}
        so actions wrap instead of overflowing on mobile. Pass{" "}
        <code className="rounded bg-muted px-1 py-0.5 text-xs">docsHref</code>{" "}
        when a genuinely relevant docs page exists — it renders a small{" "}
        <code className="rounded bg-muted px-1 py-0.5 text-xs">DocsLink</code>{" "}
        next to the actions; omit it rather than pointing at an unrelated page.
        See the{" "}
        <a href="#docs-link" className="text-primary hover:underline">
          Docs link
        </a>{" "}
        section below for the standalone primitive. The detail/edit-page header
        — back arrow inside the right-aligned action cluster — is the same
        surface for detail pages; it is documented just below.
      </p>

      <h3 className="text-sm font-medium">
        Detail &amp; edit pages: title block + right-aligned action cluster
        (back arrow first)
      </h3>
      <p className="text-sm text-muted-foreground">
        On detail/edit pages, compose a{" "}
        <code className="mx-1 rounded bg-muted px-1 py-0.5 text-xs">
          flex items-start justify-between gap-3
        </code>{" "}
        row. The <strong>left</strong> is the title block — the page{" "}
        <code className="mx-1 rounded bg-muted px-1 py-0.5 text-xs">h1</code>{" "}
        plus any subtitle/status — wrapped in{" "}
        <code className="mx-1 rounded bg-muted px-1 py-0.5 text-xs">
          min-w-0 flex-1
        </code>{" "}
        so it truncates instead of shoving the actions off-screen. The{" "}
        <strong>right</strong> is a{" "}
        <code className="mx-1 rounded bg-muted px-1 py-0.5 text-xs">
          flex gap-2 shrink-0
        </code>{" "}
        cluster whose{" "}
        <strong>first child is the icon-only ghost back button</strong>,
        followed by the page actions (View / Edit / Delete, Refresh, …). The
        back arrow is <strong>not</strong> on the far left — it leads the
        right-aligned cluster. It is <strong>always icon-only</strong> — never
        paired with a &quot;Back&quot; label. Use{" "}
        <code className="mx-1 rounded bg-muted px-1 py-0.5 text-xs">
          ArrowLeft
        </code>{" "}
        with{" "}
        <code className="mx-1 rounded bg-muted px-1 py-0.5 text-xs">
          variant=&quot;ghost&quot; size=&quot;icon&quot;
        </code>{" "}
        and an{" "}
        <code className="mx-1 rounded bg-muted px-1 py-0.5 text-xs">
          aria-label
        </code>
        . A trailing Refresh button labels itself on desktop and collapses to
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
          </div>
        }
        importLine={`<div className="flex items-start justify-between gap-3">\n  <div className="min-w-0 flex-1">\n    <h1 className="truncate text-2xl sm:text-3xl font-bold tracking-tight">{title}</h1>\n    {subtitle && <p className="mt-1 text-muted-foreground truncate">{subtitle}</p>}\n  </div>\n  <div className="flex gap-2 shrink-0">\n    <Button asChild variant="ghost" size="icon" aria-label="Back">\n      <Link to="/orgs/$org/things" params={{ org }}>\n        <ArrowLeft className="h-4 w-4" />\n      </Link>\n    </Button>\n    {/* One cluster = one button height. Don't mix size="sm" with the default. */}\n    <Button variant="outline" onClick={handleEdit} aria-label="Edit">\n      <Pencil className="h-4 w-4 sm:mr-2" />\n      <span className="hidden sm:inline">Edit</span>\n    </Button>\n    <Button variant="outline" onClick={handleRefresh} aria-label="Refresh">\n      <RotateCw className="h-4 w-4 sm:mr-2" />\n      <span className="hidden sm:inline">Refresh</span>\n    </Button>\n    <Button variant="destructive" onClick={handleDelete} aria-label="Delete">\n      <Trash2 className="h-4 w-4 sm:mr-2" />\n      <span className="hidden sm:inline">Delete</span>\n    </Button>\n  </div>\n</div>`}
      />

      <h3 className="text-sm font-medium">
        Detail &amp; edit pages: collapse the action cluster into an overflow
        menu on mobile
      </h3>
      <p className="text-sm text-muted-foreground">
        When a detail header carries more than two or three actions, the inline
        toolbar overflows on a phone. Keep only the icon-only ghost{" "}
        <strong>back button</strong> always visible; render the labeled action
        buttons in a{" "}
        <code className="mx-1 rounded bg-muted px-1 py-0.5 text-xs">
          hidden md:flex
        </code>{" "}
        cluster, and mirror every one of them as items inside a{" "}
        <code className="mx-1 rounded bg-muted px-1 py-0.5 text-xs">
          md:hidden
        </code>{" "}
        <code className="mx-1 rounded bg-muted px-1 py-0.5 text-xs">
          DropdownMenu
        </code>{" "}
        triggered by a{" "}
        <code className="mx-1 rounded bg-muted px-1 py-0.5 text-xs">
          MoreVertical
        </code>{" "}
        (⋯) button. The delete item is{" "}
        <code className="mx-1 rounded bg-muted px-1 py-0.5 text-xs">
          text-destructive focus:text-destructive
        </code>{" "}
        with a{" "}
        <code className="mx-1 rounded bg-muted px-1 py-0.5 text-xs">
          Trash2
        </code>{" "}
        icon, just like the inline destructive button. Drive any confirm dialog
        from controlled state so it opens from either surface.
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
                    <Button
                      variant="outline"
                      size="icon"
                      aria-label="More actions"
                    >
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

      <h3 className="text-sm font-medium">
        Detail &amp; edit pages: stack the action toolbar on its own row
        (action-dense headers)
      </h3>
      <p className="text-sm text-muted-foreground">
        When a detail header carries <strong>more than ~three actions</strong>{" "}
        (e.g. the check detail page: back + Edit, Enable/Disable, Clone, Badges,
        Refresh, Delete) the labeled toolbar and a long title fight for the same
        row — even on a wide desktop. Instead of shrinking the buttons or hiding
        them behind an overflow menu, drop the toolbar onto its own row. Make
        the outer wrapper a{" "}
        <code className="mx-1 rounded bg-muted px-1 py-0.5 text-xs">
          flex flex-col gap-3
        </code>{" "}
        column: the title block (still wrapped in{" "}
        <code className="mx-1 rounded bg-muted px-1 py-0.5 text-xs">
          min-w-0 flex-1
        </code>{" "}
        so the{" "}
        <code className="mx-1 rounded bg-muted px-1 py-0.5 text-xs">h1</code>{" "}
        truncates) takes the first row, then the action cluster — back arrow
        leading, as ever — sits on a second row wrapped in{" "}
        <code className="mx-1 rounded bg-muted px-1 py-0.5 text-xs">
          flex flex-wrap items-center justify-end gap-2
        </code>
        . It is right-aligned and wraps across lines on a narrow phone rather
        than overflowing. The per-button responsive behaviour (icon-only below{" "}
        <code className="mx-1 rounded bg-muted px-1 py-0.5 text-xs">lg</code>,
        icon + label at{" "}
        <code className="mx-1 rounded bg-muted px-1 py-0.5 text-xs">lg+</code>)
        is unchanged — only the wrappers move.
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
          <code className="rounded bg-muted px-1 py-0.5 text-xs">
            PageHeader
          </code>{" "}
          so the app stays consistent.
        </p>
      </div>
    </Section>
  );
}

function ButtonPlacementSection() {
  const buttonPlacementSnippet = `// A page WITH a search/filter toolbar: PageHeader actions carries only the
// primary "New X" action. Refresh moves into the toolbar row, right of search.
<PageHeader
  icon={Globe}
  title="Status pages"
  actions={
    <Button asChild>
      <Link to="/orgs/$org/status-pages/new" params={{ org }}>
        <Plus className="mr-2 h-4 w-4" />
        New page
      </Link>
    </Button>
  }
/>
<div className="flex flex-wrap items-center gap-4">
  <div className="relative flex-1 min-w-[200px] max-w-sm">
    <Search className="absolute left-3 top-1/2 -translate-y-1/2 h-4 w-4 text-muted-foreground" />
    <Input placeholder="Search…" className="pl-9" />
  </div>
  {/* Any filter selects go here, between search and Refresh. */}
  <Button variant="outline" onClick={() => refetch()} disabled={isRefetching} aria-label={t("common:refresh")}>
    <RefreshCw className={\`h-4 w-4 sm:mr-2 \${isRefetching ? "animate-spin" : ""}\`} />
    <span className="hidden sm:inline">{t("common:refresh")}</span>
  </Button>
</div>

// A page with NO search/filter toolbar (e.g. on-call) keeps Refresh in the
// header, to the left of the primary action:
<PageHeader
  actions={
    <>
      <Button variant="outline" onClick={() => refetch()} aria-label={t("common:refresh")}>
        <RefreshCw className="h-4 w-4 sm:mr-2" />
        <span className="hidden sm:inline">{t("common:refresh")}</span>
      </Button>
      <Button asChild>
        <Link to="/orgs/$org/on-call/new" params={{ org }}>Create schedule</Link>
      </Button>
    </>
  }
/>`;

  return (
    <Section
      id="button-placement"
      title="Button placement"
      description="Where a button lives depends on what it does: PageHeader actions change what exists on the page (create, export); the toolbar row below the header changes what you're currently looking at (search, filter, refresh). Row-level Pencil/Trash2 icon buttons are a third, separate surface — documented in Conventions, cross-referenced below."
    >
      <h3 className="text-sm font-medium">
        PageHeader actions: primary, page-level actions only
      </h3>
      <p className="text-sm text-muted-foreground">
        The{" "}
        <code className="rounded bg-muted px-1 py-0.5 text-xs">actions</code>{" "}
        slot on{" "}
        <code className="rounded bg-muted px-1 py-0.5 text-xs">PageHeader</code>{" "}
        is reserved for the page's primary action — typically a single &quot;New
        &lt;resource&gt;&quot; create button, plus at most one secondary
        page-level action (export/import, a scope toggle). It is{" "}
        <strong>not</strong> a catch-all toolbar: a page that has a
        search/filter toolbar row below the header does not put Refresh in the
        header — Refresh moves into that row (see below). The one exception is a
        page with <strong>no search toolbar at all</strong> — on-call has no
        search field, so it keeps Refresh in the header, to the left of the
        primary button.
      </p>
      <p className="text-sm text-muted-foreground">
        A third case reads like a toolbar but isn't one:{" "}
        <strong>filters scoped to a single table or card</strong>, rendered
        inside that card's own header next to its title (e.g. a source-type
        select next to a &quot;Scans&quot; card title, or a per-tab status
        filter above one tab's table). That's card-level chrome, not a
        page-level toolbar row — the row this section means sits directly under{" "}
        <code className="rounded bg-muted px-1 py-0.5 text-xs">PageHeader</code>
        , outside and above any card. A page whose only filtering controls are
        card-nested like this has, from the page's point of view, no toolbar at
        all — Refresh stays in the header, per the exception above.
      </p>

      <h3 className="text-sm font-medium">
        Toolbar row: search, filters, then Refresh
      </h3>
      <p className="text-sm text-muted-foreground">
        Data/view controls — the search input, any filter selects, and the
        Refresh button — live in their own{" "}
        <code className="rounded bg-muted px-1 py-0.5 text-xs">
          flex flex-wrap items-center gap-4
        </code>{" "}
        row below the header. Refresh sits to the{" "}
        <strong>right of the search input</strong> (after any filter selects, if
        the row has them) — mirror{" "}
        <code className="rounded bg-muted px-1 py-0.5 text-xs">
          integrations.index.tsx
        </code>{" "}
        (search-only toolbar) or{" "}
        <code className="rounded bg-muted px-1 py-0.5 text-xs">
          checks.index.tsx
        </code>{" "}
        (search + several filters, Refresh trailing).
      </p>

      <div className="space-y-3 rounded-md border bg-card p-4">
        <PageHeader
          icon={Globe}
          title="Status pages"
          actions={
            <Button size="sm">
              <Plus className="mr-2 h-4 w-4" />
              New page
            </Button>
          }
          className="flex-wrap"
        />
        <div className="flex flex-wrap items-center gap-4">
          <div className="relative flex-1 min-w-[200px] max-w-sm">
            <Search className="absolute left-3 top-1/2 -translate-y-1/2 h-4 w-4 text-muted-foreground" />
            <Input placeholder="Search…" className="pl-9" />
          </div>
          <Button variant="outline" size="sm" aria-label="Refresh">
            <RefreshCw className="h-4 w-4 sm:mr-2" />
            <span className="hidden sm:inline">Refresh</span>
          </Button>
        </div>
      </div>
      <CodeSnippet code={buttonPlacementSnippet} />

      <p className="text-sm text-muted-foreground">
        Row-level actions — the ghost{" "}
        <code className="rounded bg-muted px-1 py-0.5 text-xs">Pencil</code>/
        <code className="rounded bg-muted px-1 py-0.5 text-xs">Trash2</code>{" "}
        icon-button pair on each table row — are documented separately, see{" "}
        <a href="#conventions" className="text-primary hover:underline">
          Conventions → Row actions
        </a>
        . And the empty state that replaces the table when it has nothing to
        show is documented in{" "}
        <a href="#list-surface" className="text-primary hover:underline">
          List surface → Empty state
        </a>
        .
      </p>
    </Section>
  );
}

function DocsLinkSection() {
  const importLine = `import { DocsLink } from "@/components/shared/docs-link";

<DocsLink href="/docs/features/check-types" />`;
  return (
    <Section
      id="docs-link"
      title="Docs link"
      description="A small, discreet ghost icon button that opens the embedded docs site (served same-origin at /docs on every host) in a new tab. Intended for header-level placement via PageHeader's docsHref prop — see the Page header section above — but usable standalone anywhere a contextual doc link is warranted. Only wire it when a genuinely relevant docs page exists; never point it at a generic landing page like /docs/intro."
    >
      <ExampleRow
        preview={<DocsLink href="/docs/features/check-types" />}
        importLine={importLine}
      />
      <p className="text-sm text-muted-foreground">
        Renders a{" "}
        <code className="rounded bg-muted px-1 py-0.5 text-xs">BookOpen</code>{" "}
        icon in a ~h-8 w-8 ghost button, with a "Documentation" tooltip and{" "}
        <code className="rounded bg-muted px-1 py-0.5 text-xs">aria-label</code>
        . The <code className="rounded bg-muted px-1 py-0.5 text-xs">href</code>{" "}
        is a same-origin relative path (e.g.{" "}
        <code className="rounded bg-muted px-1 py-0.5 text-xs">
          /docs/features/...
        </code>
        ), opened with{" "}
        <code className="rounded bg-muted px-1 py-0.5 text-xs">
          target=&quot;_blank&quot; rel=&quot;noopener&quot;
        </code>
        . Header-level links only for now — don't sprinkle field-level docs
        links inside forms in this pass.
      </p>
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
          Look at the header bar above the sidebar trigger — the breadcrumb
          shows{" "}
          <code className="rounded bg-muted px-1 py-0.5 text-xs">
            Design Reference
          </code>{" "}
          with the Palette icon. That branch was added alongside the others in{" "}
          <code className="rounded bg-muted px-1 py-0.5 text-xs">$org.tsx</code>
          .
        </p>
      </div>
      <CodeSnippet code={snippet} />
      <div className="rounded-md border bg-card p-4 space-y-2">
        <p className="text-sm font-medium">
          Context-driven breadcrumbs with{" "}
          <code className="rounded bg-muted px-1 py-0.5 text-xs">?from=</code>
        </p>
        <p className="text-sm text-muted-foreground">
          When a detail page can be reached from multiple parent surfaces,
          encode the navigation context in a{" "}
          <code className="rounded bg-muted px-1 py-0.5 text-xs">
            ?from=type:uid
          </code>{" "}
          search param (e.g.{" "}
          <code className="rounded bg-muted px-1 py-0.5 text-xs">
            ?from=incident:abc123
          </code>{" "}
          or{" "}
          <code className="rounded bg-muted px-1 py-0.5 text-xs">
            ?from=integration:xyz
          </code>
          ). The breadcrumb reads the param and renders the matching parent
          chain. Label resolution uses the query cache — no extra fetch. The
          notification detail route (
          <code className="rounded bg-muted px-1 py-0.5 text-xs">
            /orgs/$org/notifications/$notificationUid
          </code>
          ) is the canonical example of this pattern.
        </p>
      </div>
      <CodeSnippet code={contextualSnippet} />
    </Section>
  );
}

const COLOR_TOKENS: { name: string; varName: string; description?: string }[] =
  [
    {
      name: "primary",
      varName: "--primary",
      description: "Action color (buttons, links, focus rings)",
    },
    {
      name: "brand",
      varName: "--brand",
      description: "Logo/marketing chrome — never an interactive affordance",
    },
    {
      name: "brand-muted",
      varName: "--brand-muted",
      description: "Soft brand wash for headers / hero strips",
    },
    {
      name: "destructive",
      varName: "--destructive",
      description: "Delete / irreversible action confirms",
    },
    {
      name: "accent",
      varName: "--accent",
      description: "Hover/highlight surface",
    },
    {
      name: "control",
      varName: "--control",
      description:
        "Fill of interactive form surfaces (input, textarea, select trigger, outline button). Light raises it to white; dark recesses it below --card. Use bg-control — bg-background is reserved for the page shell, the sidebar rail and the switch thumb.",
    },
    {
      name: "input",
      varName: "--input",
      description:
        "BORDER color of form controls, consumed as border-input — not a fill. The fill is --control; there is deliberately no --input-background.",
    },
    {
      name: "muted-foreground",
      varName: "--muted-foreground",
      description: "Secondary text",
    },
    {
      name: "status-ok",
      varName: "--status-ok",
      description: "Healthy / passing — swatch color (dots, bars, soft tints)",
    },
    {
      name: "status-ok-foreground",
      varName: "--status-ok-foreground",
      description: "Text on a soft status-ok tint (badges, alerts)",
    },
    {
      name: "status-warning",
      varName: "--status-warning",
      description: "Degraded — swatch color",
    },
    {
      name: "status-warning-foreground",
      varName: "--status-warning-foreground",
      description: "Text on a soft status-warning tint",
    },
    {
      name: "status-error",
      varName: "--status-error",
      description: "Failing — swatch color",
    },
    {
      name: "status-error-foreground",
      varName: "--status-error-foreground",
      description: "Text on a soft status-error tint",
    },
  ];

const CHART_TOKENS = [
  "--chart-1",
  "--chart-2",
  "--chart-3",
  "--chart-4",
  "--chart-5",
];

function Swatch({
  varName,
  label,
  description,
}: {
  varName: string;
  label: string;
  description?: string;
}) {
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

        <h3 className="text-sm font-medium">
          Action buttons (icon + label, mobile collapses to icon)
        </h3>
        <p className="text-sm text-muted-foreground">
          Pair every action with a recognisable icon and a one-word verb. Use{" "}
          <code className="mx-1 rounded bg-muted px-1 py-0.5 text-xs">
            Save
          </code>{" "}
          (floppy disk) for save,{" "}
          <code className="mx-1 rounded bg-muted px-1 py-0.5 text-xs">
            Trash2
          </code>{" "}
          for delete,{" "}
          <code className="mx-1 rounded bg-muted px-1 py-0.5 text-xs">
            Pencil
          </code>{" "}
          for edit, and{" "}
          <code className="mx-1 rounded bg-muted px-1 py-0.5 text-xs">
            RotateCw
          </code>{" "}
          for reload. Below the{" "}
          <code className="mx-1 rounded bg-muted px-1 py-0.5 text-xs">sm</code>{" "}
          breakpoint, the label collapses and only the icon remains: wrap the
          label in{" "}
          <code className="mx-1 rounded bg-muted px-1 py-0.5 text-xs">
            &lt;span className=&quot;hidden sm:inline&quot;&gt;
          </code>{" "}
          and pair every button with an{" "}
          <code className="mx-1 rounded bg-muted px-1 py-0.5 text-xs">
            aria-label
          </code>{" "}
          so screen readers still announce the action when the text is gone.
          Resize your viewport to verify.
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

        <h3 className="text-sm font-medium">
          Header refresh button (icon-only on mobile)
        </h3>
        <p className="text-sm text-muted-foreground">
          The canonical list/detail header refresh control. An{" "}
          <code className="mx-1 rounded bg-muted px-1 py-0.5 text-xs">
            outline
          </code>{" "}
          button wrapping{" "}
          <code className="mx-1 rounded bg-muted px-1 py-0.5 text-xs">
            RefreshCw
          </code>{" "}
          that shows the word <strong>Refresh</strong> from the{" "}
          <code className="mx-1 rounded bg-muted px-1 py-0.5 text-xs">sm</code>{" "}
          breakpoint up and collapses to icon-only below it. Drop{" "}
          <code className="mx-1 rounded bg-muted px-1 py-0.5 text-xs">
            size=&quot;icon&quot;
          </code>{" "}
          so the button sizes to its content, put{" "}
          <code className="mx-1 rounded bg-muted px-1 py-0.5 text-xs">
            sm:mr-2
          </code>{" "}
          on the icon so the gap only appears with the label, and keep an{" "}
          <code className="mx-1 rounded bg-muted px-1 py-0.5 text-xs">
            aria-label
          </code>{" "}
          for the icon-only state. Add{" "}
          <code className="mx-1 rounded bg-muted px-1 py-0.5 text-xs">
            animate-spin
          </code>{" "}
          while the query is refetching. (Use the localized{" "}
          <code className="mx-1 rounded bg-muted px-1 py-0.5 text-xs">
            common:refresh
          </code>{" "}
          string on real pages.)
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

        <h3 className="text-sm font-medium">
          Button with &quot;Last used&quot; badge
        </h3>
        <p className="text-sm text-muted-foreground">
          The promoted-slot pattern used on the login page: a full-width action
          button carries an inline{" "}
          <code className="mx-1 rounded bg-muted px-1 py-0.5 text-xs">
            secondary
          </code>{" "}
          Badge marking the option a returning user picked last. Keep the badge
          last in the button and spaced with{" "}
          <code className="mx-1 rounded bg-muted px-1 py-0.5 text-xs">
            ml-2
          </code>
          .
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

        <h3 className="text-sm font-medium">Support message bubble</h3>
        <p className="text-sm text-muted-foreground">
          One message in a support-inbox thread. Inbound sits left in{" "}
          <code className="mx-1 rounded bg-muted px-1 py-0.5 text-xs">
            bg-muted
          </code>
          , our own replies sit right in{" "}
          <code className="mx-1 rounded bg-muted px-1 py-0.5 text-xs">
            bg-primary
          </code>
          , so the thread reads like the WhatsApp/Telegram/SMS conversation the
          person is actually sitting in. The body is rendered as{" "}
          <strong>text, never as markup</strong> — these bodies arrive from
          publicly reachable phone numbers and are attacker-influenced by
          definition. A body stored over the cap shows the truncation note; a
          reply whose provider send failed shows the delivery warning, because a
          failed reply that left no trace is how an operator answers the same
          person twice.
        </p>
        <ExampleRow
          preview={
            <div className="w-full max-w-md space-y-2">
              <SupportMessageBubble
                message={{
                  uid: "m1",
                  threadUid: "t1",
                  channel: "whatsapp",
                  direction: "inbound",
                  body: "is the api down for you too?",
                  rawType: "text",
                  createdAt: SUPPORT_BUBBLE_INBOUND_AT,
                }}
              />
              <SupportMessageBubble
                message={{
                  uid: "m2",
                  threadUid: "t1",
                  channel: "whatsapp",
                  direction: "outbound",
                  body: "looking into it now",
                  rawType: "text",
                  delivery: { status: "failed" },
                  createdAt: SUPPORT_BUBBLE_OUTBOUND_AT,
                }}
              />
            </div>
          }
          importLine={`import { SupportMessageBubble } from "@/components/support/message-bubble";\n\n<SupportMessageBubble message={message} />`}
        />

        <h3 className="text-sm font-medium">Status dot</h3>
        <p className="text-sm text-muted-foreground">
          The small dot rendered beside a check name (listing) and the detail
          header. Colours come from the single source of truth{" "}
          <code className="mx-1 rounded bg-muted px-1 py-0.5 text-xs">
            statusStyle()
          </code>{" "}
          so the dot always matches the{" "}
          <code className="mx-1 rounded bg-muted px-1 py-0.5 text-xs">
            StatusBadge
          </code>{" "}
          beside it. A <strong>disabled</strong> check (
          <code className="mx-1 rounded bg-muted px-1 py-0.5 text-xs">
            enabled === false
          </code>
          ) renders a neutral grey dot that overrides the last/live status
          colour, so a paused check no longer reads as "healthy & live". Pass a
          localized{" "}
          <code className="mx-1 rounded bg-muted px-1 py-0.5 text-xs">
            title
          </code>{" "}
          (the translated "Disabled") for the tooltip and accessible label.
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
                <StatusDot status="up" enabled={false} title="Disabled" />{" "}
                Disabled
              </span>
            </>
          }
          importLine={`import { StatusDot } from "@/components/shared/status-dot";\n\n<StatusDot\n  status={check.status ?? check.lastResult?.status}\n  enabled={check.enabled}\n  title={check.enabled === false ? t("checks:detail.disabled") : undefined}\n/>`}
        />

        <h3 className="text-sm font-medium">IPv6 capability badge</h3>
        <p className="text-sm text-muted-foreground">
          What a region's <strong>live</strong> workers report about their IPv6
          egress (spec 2026-08-15-11). Three states, three renderings —{" "}
          <code className="mx-1 rounded bg-muted px-1 py-0.5 text-xs">
            unknown
          </code>{" "}
          means "not reported yet" (no live worker, or an older agent) and must{" "}
          <strong>never</strong> be rendered as{" "}
          <code className="mx-1 rounded bg-muted px-1 py-0.5 text-xs">no</code>.
          The value is a hint only: it never hides, disables or filters a
          region, because the run-time egress probe is the authority. Use{" "}
          <code className="mx-1 rounded bg-muted px-1 py-0.5 text-xs">
            hideUnknown
          </code>{" "}
          on dense inline surfaces (the region picker) — "no" always renders, so
          a missing badge can never be misread as a negative.
        </p>
        <ExampleRow
          preview={
            <>
              <Ipv6CapabilityBadge capability="yes" />
              <Ipv6CapabilityBadge capability="no" />
              <Ipv6CapabilityBadge capability="unknown" />
            </>
          }
          importLine={`import {\n  Ipv6CapabilityBadge,\n  ipv6Capability,\n} from "@/components/shared/ipv6-capability";\n\n<Ipv6CapabilityBadge\n  capability={ipv6Capability(region.capabilities)}\n  hideUnknown={!pinnedIpv6}\n/>`}
        />

        <h3 className="text-sm font-medium">Browser capability icon</h3>
        <p className="text-sm text-muted-foreground">
          What a region's <strong>live</strong> workers report about having a
          headless browser available (spec 2026-08-26-01). Same three-state
          contract as the IPv6 badge above —{" "}
          <code className="mx-1 rounded bg-muted px-1 py-0.5 text-xs">
            unknown
          </code>{" "}
          means "not reported yet" and must <strong>never</strong> be rendered
          as{" "}
          <code className="mx-1 rounded bg-muted px-1 py-0.5 text-xs">no</code>
          . Rendered as a single icon with no label text — a second text badge
          next to IPv6 would crowd the region picker — so the state lives in
          the icon's color and the tooltip. The value is a hint only: it never
          hides, disables or filters a region. Use{" "}
          <code className="mx-1 rounded bg-muted px-1 py-0.5 text-xs">
            hideUnknown
          </code>{" "}
          on dense inline surfaces (the region picker) — "no" always renders,
          so a missing icon can never be misread as a negative.
        </p>
        <ExampleRow
          preview={
            <>
              <BrowserCapabilityIcon capability="yes" />
              <BrowserCapabilityIcon capability="no" />
              <BrowserCapabilityIcon capability="unknown" />
            </>
          }
          importLine={`import {\n  BrowserCapabilityIcon,\n  browserCapability,\n} from "@/components/shared/browser-capability";\n\n<BrowserCapabilityIcon\n  capability={browserCapability(region.capabilities)}\n  hideUnknown={type !== "browser"}\n/>`}
        />

        <h3 className="text-sm font-medium">Flapping badge</h3>
        <p className="text-sm text-muted-foreground">
          "Flapping ×N" for an incident that opened or reopened at an escalated
          adaptive-recovery flap level (spec 2026-08-24-05,
          <code className="mx-1 rounded bg-muted px-1 py-0.5 text-xs">
            incident.flapLevel
          </code>
          ). Used identically on the incidents list and the incident detail page
          — one component so the amber tone (
          <code className="mx-1 rounded bg-muted px-1 py-0.5 text-xs">
            flappingBadgeClass
          </code>
          ) can never drift between the two. The component does not self-hide —
          callers guard on{" "}
          <code className="mx-1 rounded bg-muted px-1 py-0.5 text-xs">
            flapLevel &gt; 0
          </code>
          .
        </p>
        <ExampleRow
          preview={
            <div className="flex flex-wrap items-center gap-2">
              <FlappingBadge flapLevel={1} t={designReferenceFlappingT} />
              <FlappingBadge flapLevel={3} t={designReferenceFlappingT} />
            </div>
          }
          importLine={`import { FlappingBadge } from "@/components/shared/flapping-badge";\n\n{(incident.flapLevel ?? 0) > 0 && (\n  <FlappingBadge flapLevel={incident.flapLevel} t={t} />\n)}`}
        />

        <h3 className="text-sm font-medium">
          SLO state chip &amp; error-budget meter
        </h3>
        <p className="text-sm text-muted-foreground">
          The four objective states (spec 2026-08-20-01) and the
          remaining-budget meter that accompanies them.{" "}
          <code className="mx-1 rounded bg-muted px-1 py-0.5 text-xs">
            unknown
          </code>{" "}
          is the no-data state and is deliberately{" "}
          <strong>neutral grey, never green</strong>: an objective over a window
          with no probes has null attainment, and rendering that as a healthy
          100% would turn "we were not watching" into "everything was fine".
          Attainment itself follows the same rule — render{" "}
          <code className="mx-1 rounded bg-muted px-1 py-0.5 text-xs">
            null
          </code>{" "}
          as a dash via{" "}
          <code className="mx-1 rounded bg-muted px-1 py-0.5 text-xs">
            formatAttainment
          </code>
          . The meter is clamped to [0, 1] by{" "}
          <code className="mx-1 rounded bg-muted px-1 py-0.5 text-xs">
            budgetRemainingFraction
          </code>
          , while the label keeps the sign so an overspent budget reads as{" "}
          <code className="mx-1 rounded bg-muted px-1 py-0.5 text-xs">
            -12m 30s
          </code>
          .
        </p>
        <ExampleRow
          preview={
            <div className="w-full space-y-3">
              <div className="flex flex-wrap items-center gap-2">
                <Badge className={sloStateBadgeClass("healthy")}>Healthy</Badge>
                <Badge className={sloStateBadgeClass("at_risk")}>At risk</Badge>
                <Badge className={sloStateBadgeClass("breached")}>
                  Breached
                </Badge>
                <Badge className={sloStateBadgeClass("unknown")}>No data</Badge>
              </div>
              <div className="max-w-xs space-y-1">
                <div className="h-2.5 w-full overflow-hidden rounded-full bg-muted">
                  <div
                    className={`h-full rounded-full ${sloBudgetBarClass("at_risk")}`}
                    style={{ width: "38%" }}
                  />
                </div>
                <p className="text-xs text-muted-foreground">
                  {formatBudgetSeconds(990)} of {formatBudgetSeconds(2592)}{" "}
                  remaining
                </p>
              </div>
            </div>
          }
          importLine={`import {\n  budgetRemainingFraction,\n  formatAttainment,\n  formatBudgetSeconds,\n  sloBudgetBarClass,\n  sloStateBadgeClass,\n} from "@/lib/slo-format";\n\n<Badge className={sloStateBadgeClass(row.state)}>\n  {t(\`state.\${row.state}\`)}\n</Badge>`}
        />

        <h3 className="text-sm font-medium">Error-budget burn-down chart</h3>
        <p className="text-sm text-muted-foreground">
          The objective detail page's burn-down (spec 2026-08-20-01): remaining
          budget over the current window against the straight "ideal" line that
          spends it exactly. Two conventions are load-bearing. The actual series
          is <strong>never clamped at zero</strong> — an overspent budget dips
          below the dashed destructive reference line, and flattening it there
          would hide the magnitude of a breach. And every DATA dot renders a{" "}
          <code className="mx-1 rounded bg-muted px-1 py-0.5 text-xs">
            &lt;title&gt;
          </code>
          , which recharts' hover activeDot does not, so an E2E can count{" "}
          <code className="mx-1 rounded bg-muted px-1 py-0.5 text-xs">
            circle:has(title)
          </code>{" "}
          deterministically.
        </p>
        <ExampleRow
          preview={
            <div className="w-full max-w-lg">
              <BudgetBurndownChart burndown={designReferenceBurndown} />
            </div>
          }
          importLine={`import { BudgetBurndownChart } from "@/components/slos/budget-burndown-chart";\n\n<BudgetBurndownChart\n  burndown={burndown}\n  isLoading={burndownLoading}\n/>`}
        />

        <h3 className="text-sm font-medium">Agent version cell</h3>
        <p className="text-sm text-muted-foreground">
          Compares an agent's self-reported build version against this server's
          own (spec 2026-08-19-07). Unlike the IPv6 badge above, this is not a
          stored three-state value — it is a comparison computed at read time
          from two inputs, each already two-state: the agent's version (
          <code className="mx-1 rounded bg-muted px-1 py-0.5 text-xs">
            null
          </code>{" "}
          = never reported) and the server's own (from{" "}
          <code className="mx-1 rounded bg-muted px-1 py-0.5 text-xs">
            useVersion()
          </code>
          ). Matching and unknown render as plain text; only a genuine mismatch
          gets the amber "Drifted" badge —{" "}
          <code className="mx-1 rounded bg-muted px-1 py-0.5 text-xs">
            null
          </code>{" "}
          must <strong>never</strong> render as drifted, or an agent that simply
          predates this feature would look broken.
        </p>
        <ExampleRow
          preview={
            <div className="flex flex-col items-start gap-2">
              <AgentVersionCell agentVersion="0.17.0" serverVersion="0.17.0" />
              <AgentVersionCell agentVersion="0.16.2" serverVersion="0.17.0" />
              <AgentVersionCell agentVersion={null} serverVersion="0.17.0" />
            </div>
          }
          importLine={`import { AgentVersionCell } from "@/components/shared/agent-version";\n\n<AgentVersionCell\n  agentVersion={agent.version}\n  serverVersion={versionData?.version}\n/>`}
        />

        <h3 className="text-sm font-medium">Check group status header</h3>
        <p className="text-sm text-muted-foreground">
          The checks index (
          <code className="mx-1 rounded bg-muted px-1 py-0.5 text-xs">
            checks.index.tsx
          </code>
          ) buckets checks by{" "}
          <code className="mx-1 rounded bg-muted px-1 py-0.5 text-xs">
            checkGroupUid
          </code>{" "}
          into collapsible sections. The header always reuses the same{" "}
          <code className="mx-1 rounded bg-muted px-1 py-0.5 text-xs">
            StatusBadge
          </code>{" "}
          as check rows — no new colors — next to a compact member summary
          derived from the group's{" "}
          <code className="mx-1 rounded bg-muted px-1 py-0.5 text-xs">
            memberStatusCounts
          </code>
          : the "N/N up" form when every counted member is up (the
          collapse-eligible case), otherwise severity-ordered parts like "1 down
          · 3 up". A group defaults to collapsed only when its rollup status is{" "}
          <code className="mx-1 rounded bg-muted px-1 py-0.5 text-xs">up</code>;
          any manual toggle overrides the default and persists per org in
          localStorage.
        </p>
        <ExampleRow
          preview={
            <>
              <div className="flex items-center gap-2 rounded-md border px-3 py-2 text-sm">
                <ChevronRight className="h-4 w-4 text-muted-foreground" />
                <span className="font-semibold">prod-eu-west</span>
                <StatusBadge status="up" />
                <span className="text-xs text-muted-foreground">4/4 up</span>
                <Badge variant="secondary" className="text-xs">
                  4
                </Badge>
              </div>
              <div className="flex items-center gap-2 rounded-md border px-3 py-2 text-sm">
                <ChevronDown className="h-4 w-4 text-muted-foreground" />
                <span className="font-semibold">prod-us-east</span>
                <StatusBadge status="degraded" />
                <span className="text-xs text-muted-foreground">
                  1 down · 3 up
                </span>
                <Badge variant="secondary" className="text-xs">
                  4
                </Badge>
              </div>
            </>
          }
          importLine={`import { StatusBadge } from "@/components/shared/status-badge";\n\n<StatusBadge status={group.status} />\n<span className="text-xs text-muted-foreground">{formatMemberSummary(group.memberStatusCounts, t)}</span>`}
        />

        <h3 className="text-sm font-medium">Incident group header</h3>
        <p className="text-sm text-muted-foreground">
          Incidents are per-check (spec 2026-08-24-14): six members of a group
          going down produce six incidents, which is what makes each one
          independently pageable and lets any of them act as a dependency-rollup
          parent. The consolidated &quot;N/M down&quot; view is rebuilt at READ
          time — the incidents list (
          <code className="mx-1 rounded bg-muted px-1 py-0.5 text-xs">
            incidents.index.tsx
          </code>
          ) and the dashboard&apos;s Active-incidents card group what they hold
          with{" "}
          <code className="mx-1 rounded bg-muted px-1 py-0.5 text-xs">
            groupIncidentsByCheckGroup
          </code>{" "}
          and render this header above the member rows.
        </p>
        <p className="text-sm text-muted-foreground">
          Deliberately NOT collapsible, unlike the checks-index group header
          above: there is no per-group open/closed state worth persisting across
          filters, sorts and pagination. The count is what is LOADED, so the
          denominator is dropped rather than overstated when a group&apos;s
          members straddle a page boundary — never assert a fleet-wide total the
          client cannot see.
        </p>
        <ExampleRow
          preview={
            <div className="w-full overflow-hidden rounded-md border">
              <div className="flex items-center gap-2 bg-muted/40 px-3 py-2 text-sm">
                <Layers className="h-4 w-4 shrink-0 text-muted-foreground" />
                <span className="font-semibold">RabbitMQ — 2/6 down</span>
              </div>
              <div className="border-t px-3 py-2 text-sm text-muted-foreground">
                rabbitmq-prod
              </div>
              <div className="border-t px-3 py-2 text-sm text-muted-foreground">
                rabbitmq-nonprod
              </div>
            </div>
          }
          importLine={`import { groupIncidentsByCheckGroup, groupHeaderCounts } from "@/lib/incident-grouping";\n\nconst rows = groupIncidentsByCheckGroup(incidents, checks, checkGroups);`}
        />

        <h3 className="text-sm font-medium">Live connection status dot</h3>
        <p className="text-sm text-muted-foreground">
          Passive indicator for the org live-updates WebSocket, mounted in the
          sidebar footer utility row alongside{" "}
          <code className="mx-1 rounded bg-muted px-1 py-0.5 text-xs">
            LanguageSwitcher
          </code>{" "}
          and{" "}
          <code className="mx-1 rounded bg-muted px-1 py-0.5 text-xs">
            ThemeToggle
          </code>
          . Reads{" "}
          <code className="mx-1 rounded bg-muted px-1 py-0.5 text-xs">
            useLiveConnectionStatus()
          </code>{" "}
          from{" "}
          <code className="mx-1 rounded bg-muted px-1 py-0.5 text-xs">
            LiveEventsContext
          </code>{" "}
          — green while streaming, red while dropped/retrying, grey while
          connecting or when realtime is disabled/forbidden server-side (never
          red for a by-design non-live state). Purely informational: no click
          action, no popover. The dot below reflects this page's actual live
          connection.
        </p>
        <ExampleRow
          preview={
            <span className="inline-flex items-center gap-1.5 text-sm">
              <LiveStatusDot /> Hover for the current state
            </span>
          }
          importLine={`import { LiveStatusDot } from "@/components/layout/live-status-dot";\n\n<LiveStatusDot />`}
        />

        <h3 className="text-sm font-medium">Server version indicator</h3>
        <p className="text-sm text-muted-foreground">
          Also mounted in the sidebar footer utility row (spec
          2026-08-28-01). Discreet in the common case: just the current
          server version, small and muted. The loaded version is captured
          once, from the first{" "}
          <code className="mx-1 rounded bg-muted px-1 py-0.5 text-xs">
            /api/mgmt/version
          </code>{" "}
          response after boot, and never overwritten —{" "}
          <code className="mx-1 rounded bg-muted px-1 py-0.5 text-xs">
            useVersion()
          </code>{" "}
          polls the live server every 15 minutes and on window
          focus/visibilitychange. Since dash0 is embedded in the Go binary,
          a poll returning a different version than the loaded baseline
          means the server was redeployed after this page loaded — the row
          then adds the loaded version and a red reload icon (
          <code className="mx-1 rounded bg-muted px-1 py-0.5 text-xs">
            location.reload()
          </code>
          ). The live instance below reflects this tab's actual state, so
          it normally shows the muted form — the icon only appears after a
          real redeploy.
        </p>
        <ExampleRow
          preview={<ServerVersionIndicator />}
          importLine={`import { ServerVersionIndicator } from "@/components/layout/server-version-indicator";\n\n<ServerVersionIndicator />`}
        />

        <h3 className="text-sm font-medium">Relative time (live)</h3>
        <p className="text-sm text-muted-foreground">
          Live-ticking "N ago" text for a timestamp — used by the check summary
          cards' "last checked" line and the private locations agents table's
          "Last seen" column. Ticks on its own 1s interval (cleared on unmount)
          via{" "}
          <code className="mx-1 rounded bg-muted px-1 py-0.5 text-xs">
            LiveDurationAgo
          </code>
          , formats with the shared{" "}
          <code className="mx-1 rounded bg-muted px-1 py-0.5 text-xs">
            formatDuration()
          </code>{" "}
          (caps at{" "}
          <code className="mx-1 rounded bg-muted px-1 py-0.5 text-xs">
            Xd Yh Zm
          </code>
          ), and wraps the result in the translated{" "}
          <code className="mx-1 rounded bg-muted px-1 py-0.5 text-xs">
            checks:detail.summary.ago
          </code>{" "}
          template so FR/DE/ES render correctly instead of a hard-coded "ago"
          suffix. Pair it with a{" "}
          <code className="mx-1 rounded bg-muted px-1 py-0.5 text-xs">
            title
          </code>{" "}
          carrying the full local timestamp so the exact moment stays reachable
          on hover, and keep a separate "never" fallback for a null/undefined
          timestamp — the component itself has no empty state.
        </p>
        <ExampleRow
          preview={
            <span
              className="text-sm text-muted-foreground"
              title={new Date(RELATIVE_TIME_DEMO_SINCE).toLocaleString()}
            >
              <LiveDurationAgo since={RELATIVE_TIME_DEMO_SINCE} />
            </span>
          }
          importLine={`import { LiveDurationAgo } from "@/components/shared/relative-time";\n\n{agent.lastSeenAt ? (\n  <span title={new Date(agent.lastSeenAt).toLocaleString()}>\n    <LiveDurationAgo since={agent.lastSeenAt} />\n  </span>\n) : (\n  t("privateLocations.agents.never", "never")\n)}`}
        />

        <h3 className="text-sm font-medium">
          TimeAgo (hover/tap + click-to-copy)
        </h3>
        <p className="text-sm text-muted-foreground">
          The incidents list, incident detail (timeline, comments, header) and
          jobs pages' timestamp. Unlike{" "}
          <code className="mx-1 rounded bg-muted px-1 py-0.5 text-xs">
            LiveDurationAgo
          </code>{" "}
          above, the absolute time isn't just a passive{" "}
          <code className="mx-1 rounded bg-muted px-1 py-0.5 text-xs">
            title
          </code>{" "}
          — hovering (or tapping, on touch) opens a{" "}
          <code className="mx-1 rounded bg-muted px-1 py-0.5 text-xs">
            Tooltip
          </code>{" "}
          with both local and UTC time, and clicking copies the UTC time as
          ISO&nbsp;8601 (
          <code className="mx-1 rounded bg-muted px-1 py-0.5 text-xs">
            2026-08-14T09:31:07Z
          </code>
          ) to the clipboard — the format that pastes cleanly into a log query.
          The{" "}
          <code className="mx-1 rounded bg-muted px-1 py-0.5 text-xs">
            inline
          </code>{" "}
          variant's always-visible clock is the browser's LOCAL time, suffixed
          with a short zone marker (
          <code className="mx-1 rounded bg-muted px-1 py-0.5 text-xs">
            11:31:07 CEST
          </code>
          , falling back to a UTC-offset label like{" "}
          <code className="mx-1 rounded bg-muted px-1 py-0.5 text-xs">
            UTC+2
          </code>{" "}
          when the runtime can't name the zone) — an on-call operator comparing
          an incident against their own wall clock shouldn't have to do offset
          arithmetic in their head. UTC stays one hover away, in the same
          Tooltip. All instances share a single 30s re-render timer (not one{" "}
          <code className="mx-1 rounded bg-muted px-1 py-0.5 text-xs">
            setInterval
          </code>{" "}
          per row) so long-lived tabs don't drift.
        </p>
        <ExampleRow
          preview={
            <span className="text-sm">
              <code className="mx-1 rounded bg-muted px-1 py-0.5 text-xs">
                tooltip
              </code>{" "}
              (default, dense lists):{" "}
              <TimeAgo
                date={TIME_AGO_DEMO_DATE}
                data-testid="design-ref-time-ago-tooltip"
              />
            </span>
          }
          importLine={`import { TimeAgo } from "@/components/ui/time-ago";\n\n// Dense lists (incidents index, jobs): compact relative text, absolute\n// time behind hover/tap.\n{incident.startedAt ? <TimeAgo date={incident.startedAt} /> : "-"}`}
        />
        <ExampleRow
          preview={
            <span className="text-sm">
              <code className="mx-1 rounded bg-muted px-1 py-0.5 text-xs">
                inline
              </code>{" "}
              (incident detail — several timestamps compared at once):{" "}
              <TimeAgo
                date={TIME_AGO_DEMO_DATE}
                variant="inline"
                data-testid="design-ref-time-ago-inline"
              />
            </span>
          }
          importLine={`import { TimeAgo } from "@/components/ui/time-ago";\n\n// Incident detail (timeline, comments, header): absolute LOCAL time shown\n// inline instead of hidden behind hover (UTC is one hover away).\n<TimeAgo date={u.publishedAt} variant="inline" />`}
        />

        <h3 className="text-sm font-medium">Session card</h3>
        <p className="text-sm text-muted-foreground">
          The account Sessions page (
          <code className="mx-1 rounded bg-muted px-1 py-0.5 text-xs">
            /orgs/$org/account/sessions
          </code>
          ) lists login/refresh-token sessions distinctly from API tokens. Each
          row: a device icon from{" "}
          <code className="mx-1 rounded bg-muted px-1 py-0.5 text-xs">
            parseUserAgent().device
          </code>{" "}
          (
          <code className="mx-1 rounded bg-muted px-1 py-0.5 text-xs">
            Smartphone
          </code>{" "}
          /{" "}
          <code className="mx-1 rounded bg-muted px-1 py-0.5 text-xs">
            Tablet
          </code>{" "}
          /{" "}
          <code className="mx-1 rounded bg-muted px-1 py-0.5 text-xs">
            Monitor
          </code>
          ), a{" "}
          <code className="mx-1 rounded bg-muted px-1 py-0.5 text-xs">
            border-primary
          </code>{" "}
          accent + &quot;Current session&quot; badge on the caller&apos;s own
          row, a login-method badge, the raw user agent as muted mono text, and
          a destructive ghost icon revoke button. Static mock below — the real
          page is data-driven via{" "}
          <code className="mx-1 rounded bg-muted px-1 py-0.5 text-xs">
            useSessions(org)
          </code>
          .
        </p>
        <ExampleRow
          preview={
            <div className="w-full space-y-2">
              <Card className="border-primary">
                <CardContent className="flex items-start justify-between gap-3 p-4">
                  <div className="flex gap-3">
                    <Monitor className="mt-0.5 h-5 w-5 shrink-0 text-muted-foreground" />
                    <div className="space-y-1">
                      <div className="flex flex-wrap items-center gap-2">
                        <span className="font-medium">Chrome 128 on macOS</span>
                        <Badge className="border-primary">
                          Current session
                        </Badge>
                        <Badge variant="secondary">password</Badge>
                      </div>
                      <div className="text-xs text-muted-foreground font-mono">
                        Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7)...
                      </div>
                      <div className="text-xs text-muted-foreground">
                        Connected 3d ago · Last active just now · IP 203.0.113.5
                      </div>
                    </div>
                  </div>
                  <Button
                    variant="ghost"
                    size="icon"
                    className="h-8 w-8 shrink-0 text-destructive hover:text-destructive"
                    aria-label="Revoke"
                  >
                    <Trash2 className="h-4 w-4" />
                  </Button>
                </CardContent>
              </Card>
              <div className="flex justify-end">
                <Button variant="destructive" size="sm">
                  <LogOut className="mr-2 h-4 w-4" />
                  Sign out other sessions
                </Button>
              </div>
            </div>
          }
          importLine={`import { Card, CardContent } from "@/components/ui/card";\nimport { Badge } from "@/components/ui/badge";\nimport { parseUserAgent } from "@/lib/user-agent";\n\n<Card className={session.isCurrent ? "border-primary" : undefined}>\n  <CardContent className="flex items-start justify-between gap-3 p-4">\n    {/* device icon + browser/OS + badges + revoke button */}\n  </CardContent>\n</Card>`}
        />

        <h3 className="text-sm font-medium">Organization row</h3>
        <p className="text-sm text-muted-foreground">
          The account Organizations page (
          <code className="mx-1 rounded bg-muted px-1 py-0.5 text-xs">
            /orgs/$org/account/organizations
          </code>
          ) reuses the same current-marker card as the session list above: a{" "}
          <code className="mx-1 rounded bg-muted px-1 py-0.5 text-xs">
            border-primary
          </code>{" "}
          accent + &quot;Current&quot; badge on the active org&apos;s row. The
          logo box falls back to the{" "}
          <code className="mx-1 rounded bg-muted px-1 py-0.5 text-xs">
            Building
          </code>{" "}
          icon exactly like the sidebar org switcher, and every other row gets
          an outline Switch button instead of a revoke action.
        </p>
        <ExampleRow
          preview={
            <div className="w-full space-y-2">
              <Card className="border-primary">
                <CardContent className="flex flex-col gap-3 p-4 sm:flex-row sm:items-center sm:justify-between">
                  <div className="flex min-w-0 items-center gap-3">
                    <div className="flex h-9 w-9 shrink-0 items-center justify-center overflow-hidden rounded bg-muted">
                      <Building className="h-5 w-5 text-muted-foreground" />
                    </div>
                    <div className="min-w-0">
                      <div className="flex flex-wrap items-center gap-2">
                        <span className="truncate font-medium">Acme Corp</span>
                        <Badge className="border-primary">Current</Badge>
                      </div>
                      <div className="text-xs text-muted-foreground">
                        acme · Role: owner
                      </div>
                    </div>
                  </div>
                </CardContent>
              </Card>
              <Card>
                <CardContent className="flex flex-col gap-3 p-4 sm:flex-row sm:items-center sm:justify-between">
                  <div className="flex min-w-0 items-center gap-3">
                    <div className="flex h-9 w-9 shrink-0 items-center justify-center overflow-hidden rounded bg-muted">
                      <Building className="h-5 w-5 text-muted-foreground" />
                    </div>
                    <div className="min-w-0">
                      <span className="truncate font-medium">Other Org</span>
                      <div className="text-xs text-muted-foreground">
                        other-org · Role: admin
                      </div>
                    </div>
                  </div>
                  <Button variant="outline" size="sm" className="shrink-0">
                    Switch
                  </Button>
                </CardContent>
              </Card>
            </div>
          }
          importLine={`import { Card, CardContent } from "@/components/ui/card";\nimport { Badge } from "@/components/ui/badge";\nimport { Building } from "lucide-react";\n\n<Card className={isCurrent ? "border-primary" : undefined}>\n  <CardContent className="flex flex-col gap-3 p-4 sm:flex-row sm:items-center sm:justify-between">\n    {/* logo/Building fallback + name + slug + role, "Current" badge or a Switch button */}\n  </CardContent>\n</Card>`}
        />

        <h3 className="text-sm font-medium">
          Secondary-path divider + sub-card
        </h3>
        <p className="text-sm text-muted-foreground">
          When a page has one primary action and a clearly subordinate
          alternative path (e.g. the empty-state onboarding hero&apos;s
          &quot;let AI set everything up&quot; MCP link under the quick-create
          form), separate them with a hairline &quot;or&quot; divider followed
          by a bordered sub-card: icon + title/description on the left, an
          outline{" "}
          <code className="mx-1 rounded bg-muted px-1 py-0.5 text-xs">
            Button asChild
          </code>{" "}
          CTA on the right. Stacks vertically below{" "}
          <code className="mx-1 rounded bg-muted px-1 py-0.5 text-xs">sm</code>.
          The divider is decorative — mark it{" "}
          <code className="mx-1 rounded bg-muted px-1 py-0.5 text-xs">
            aria-hidden
          </code>
          .
        </p>
        <ExampleRow
          preview={
            <div className="w-full max-w-md space-y-4">
              <div className="flex items-center gap-3" aria-hidden="true">
                <div className="h-px flex-1 bg-border" />
                <span className="text-xs uppercase text-muted-foreground">
                  or
                </span>
                <div className="h-px flex-1 bg-border" />
              </div>
              <div className="flex flex-col gap-3 rounded-md border bg-card p-4 text-left sm:flex-row sm:items-center">
                <div className="flex flex-1 items-start gap-3">
                  <Bot className="mt-0.5 h-5 w-5 shrink-0 text-primary" />
                  <div>
                    <p className="text-sm font-medium">
                      Let AI set everything up
                    </p>
                    <p className="mt-0.5 text-xs text-muted-foreground">
                      Connect an AI assistant and ask it to do the work for you.
                    </p>
                  </div>
                </div>
                <Button variant="outline" size="sm" className="shrink-0">
                  Set up MCP
                  <ArrowRight className="ml-1 h-4 w-4" />
                </Button>
              </div>
            </div>
          }
          importLine={`import { Button } from "@/components/ui/button";\nimport { Link } from "@tanstack/react-router";\n\n<div className="flex items-center gap-3" aria-hidden="true">\n  <div className="h-px flex-1 bg-border" />\n  <span className="text-xs uppercase text-muted-foreground">{t("welcome.or")}</span>\n  <div className="h-px flex-1 bg-border" />\n</div>\n<div className="flex flex-col gap-3 rounded-md border bg-card p-4 text-left sm:flex-row sm:items-center">\n  {/* icon + title/description */}\n  <Button asChild variant="outline" size="sm" className="shrink-0">\n    <Link to="/orgs/$org/account/mcp" params={{ org }}>…</Link>\n  </Button>\n</div>`}
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
              <Textarea
                id="dr-textarea"
                placeholder="Multi-line input"
                rows={3}
              />
            </div>
          }
          importLine={`import { Textarea } from "@/components/ui/textarea";`}
        />

        <ExampleRow
          preview={
            <div className="w-full max-w-sm space-y-2">
              <Label htmlFor="dr-code-textarea">Code textarea</Label>
              <CodeTextarea
                id="dr-code-textarea"
                rows={4}
                placeholder={":root {\n  --brand: #ff5500;\n}"}
              />
              <p className="text-xs text-muted-foreground">
                Monospace, with spell-check / autocorrect / autocapitalize off.
                Use for CSS, JSON and other code input.
              </p>
            </div>
          }
          importLine={`import { CodeTextarea } from "@/components/ui/code-textarea";`}
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
          <code className="rounded bg-muted px-1 py-0.5 text-xs">
            {'<p className="text-xs text-muted-foreground">'}
          </code>{" "}
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
          <code className="rounded bg-muted px-1 py-0.5 text-xs">
            slugify()
          </code>{" "}
          from{" "}
          <code className="rounded bg-muted px-1 py-0.5 text-xs">
            @/lib/utils
          </code>
          . Stop auto-filling once the user has manually edited the slug, so
          their override is never clobbered. Don't surface a separate "edit
          slug" toggle — letting them type into the slug field is enough.
        </p>
        <NameSlugExample />

        <h3 className="text-sm font-medium">Image / logo field</h3>
        <p className="text-sm text-muted-foreground">
          A field that accepts <em>either</em> a pasted URL or an uploaded file
          renders as one row: a fixed-size preview tile with a{" "}
          <code className="rounded bg-muted px-1 py-0.5 text-xs">lucide</code>{" "}
          placeholder icon, then <em>one</em> of two mutually-exclusive
          affordances for the source, then an outline{" "}
          <strong>Upload</strong>/<strong>Replace</strong> button driving a
          hidden{" "}
          <code className="rounded bg-muted px-1 py-0.5 text-xs">
            &lt;input type="file"&gt;
          </code>
          , and — only when something is set — a destructive{" "}
          <code className="rounded bg-muted px-1 py-0.5 text-xs">Trash2</code>{" "}
          icon button to clear it. The row wraps on narrow screens; the input
          takes the remaining width with{" "}
          <code className="rounded bg-muted px-1 py-0.5 text-xs">
            min-w-0 flex-1
          </code>{" "}
          so it never overflows.
        </p>
        <p className="text-sm text-muted-foreground">
          <strong>Never feed a stored value into the URL input sight-unseen.</strong>{" "}
          If the current value can also come from an upload (a relative,
          server-generated path rather than a URL the user typed), track the
          source in state instead of overloading one{" "}
          <code className="rounded bg-muted px-1 py-0.5 text-xs">
            type="url"
          </code>{" "}
          field: an uploaded value renders as a{" "}
          <code className="rounded bg-muted px-1 py-0.5 text-xs">
            Badge variant="secondary"
          </code>{" "}
          ("Uploaded file") plus a text{" "}
          <code className="rounded bg-muted px-1 py-0.5 text-xs">
            variant="link"
          </code>{" "}
          button ("Use an external URL instead") that swaps in the input; a
          typed or empty URL renders the input itself. A relative path landing
          in a{" "}
          <code className="rounded bg-muted px-1 py-0.5 text-xs">
            type="url"
          </code>{" "}
          input fails native constraint validation and silently blocks the
          whole form's submit — this pattern exists to rule that out
          structurally. Last-action-wins: a successful upload always switches
          back to the badge view. Shipped in{" "}
          <code className="rounded bg-muted px-1 py-0.5 text-xs">
            components/shared/org-profile-card.tsx
          </code>
          .
        </p>
        <ImageFieldExample />

        <h3 className="text-sm font-medium">Assembled form</h3>
        <div className="rounded-md border bg-card p-4">
          <form
            className="max-w-md space-y-4"
            onSubmit={(e) => e.preventDefault()}
          >
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

function ImageFieldExample() {
  const [url, setUrl] = useState("");
  // Mirrors org-profile-card.tsx: an uploaded value never lands in the
  // type="url" input — it renders as a badge + "use a URL instead" toggle.
  const [uploaded, setUploaded] = useState(false);
  const previewUrl = uploaded ? "/pub/assets/example-uid" : url;

  return (
    <ExampleRow
      preview={
        <div className="flex w-full flex-wrap items-center gap-3">
          <div className="flex h-14 w-14 shrink-0 items-center justify-center overflow-hidden rounded-md border bg-muted">
            {previewUrl ? (
              <div className="flex h-full w-full items-center justify-center bg-primary/10 text-[10px] font-medium text-primary">
                IMG
              </div>
            ) : (
              <Building2 className="h-6 w-6 text-muted-foreground" />
            )}
          </div>
          {uploaded ? (
            <div className="flex min-w-0 flex-1 flex-wrap items-center gap-x-3 gap-y-1">
              <Badge variant="secondary">Uploaded file</Badge>
              <Button
                type="button"
                variant="link"
                className="h-auto p-0 text-xs"
                onClick={() => setUploaded(false)}
              >
                Use an external URL instead
              </Button>
            </div>
          ) : (
            <Input
              type="url"
              placeholder="https://example.com/logo.png"
              value={url}
              onChange={(e) => setUrl(e.target.value)}
              className="min-w-0 flex-1"
            />
          )}
          <Button type="button" variant="outline" onClick={() => setUploaded(true)}>
            <Upload className="mr-2 h-4 w-4" />
            {previewUrl ? "Replace" : "Upload"}
          </Button>
          {previewUrl && (
            <Button
              type="button"
              variant="ghost"
              size="icon"
              className="text-destructive"
              aria-label="Remove image"
              onClick={() => {
                setUrl("");
                setUploaded(false);
              }}
            >
              <Trash2 className="h-4 w-4" />
            </Button>
          )}
        </div>
      }
      importLine={`const fileInput = useRef<HTMLInputElement>(null);\n// isUploadedLogoPath(value) === !value.startsWith("http") && value !== ""\n\n<div className="flex h-14 w-14 shrink-0 items-center justify-center overflow-hidden rounded-md border bg-muted">\n  {currentUrl ? <img src={currentUrl} alt="" className="h-full w-full object-contain" /> : <Building2 className="h-6 w-6 text-muted-foreground" />}\n</div>\n{showUrlField ? (\n  <Input type="url" value={urlDraft} onChange={...} className="min-w-0 flex-1" />\n) : (\n  <div className="flex min-w-0 flex-1 flex-wrap items-center gap-x-3 gap-y-1">\n    <Badge variant="secondary">Uploaded file</Badge>\n    <Button type="button" variant="link" className="h-auto p-0 text-xs" onClick={() => setShowUrlField(true)}>Use an external URL instead</Button>\n  </div>\n)}\n<input ref={fileInput} type="file" accept="image/png,image/jpeg,image/webp,image/gif,image/svg+xml" className="hidden" onChange={...} />\n<Button type="button" variant="outline" onClick={() => fileInput.current?.click()}>\n  <Upload className="mr-2 h-4 w-4" />\n  {currentUrl ? "Replace" : "Upload"}\n</Button>\n<Button type="button" variant="ghost" size="icon" className="text-destructive" aria-label="Remove image">\n  <Trash2 className="h-4 w-4" />\n</Button>`}
    />
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
                <TableCell
                  colSpan={3}
                  className="py-8 text-center text-sm text-muted-foreground"
                >
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
                  <TableCell className="font-mono text-xs">
                    {row.latency}
                  </TableCell>
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
              <TableCell
                colSpan={3}
                className="py-12 text-center text-sm text-muted-foreground"
              >
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

/** Narrow rows where one cell holds an unpredictably long, unbreakable value
 * (a URL, a name with no natural wrap point). `max-w-0 w-full truncate` on the
 * <TableCell> — not just the text inside it — is what makes an auto-layout
 * <table> respect the column's fair share instead of growing to fit content;
 * pair it with `title` (or the Tooltip primitive above) so the full value is
 * still one hover/focus away. Columns that must never wrap (a badge, an
 * icon-link) get `whitespace-nowrap` so the flexible column absorbs the
 * leftover width — that's also the mechanism the blast-radius table on the
 * incident detail page uses to survive a 375px viewport. */
function TruncatedCellTable() {
  const rows = [
    {
      id: "1",
      name: "api.acme-staging.io document-storage version (http)",
      status: "up" as const,
    },
    {
      id: "2",
      name: "84698cfb-5b01-4fab-b898-13beda200722",
      status: "down" as const,
    },
  ];

  return (
    <div className="max-w-xs rounded-md border">
      <Table>
        <TableHeader>
          <TableRow>
            <TableHead>Check</TableHead>
            <TableHead className="whitespace-nowrap">State</TableHead>
            <TableHead className="whitespace-nowrap px-2" />
          </TableRow>
        </TableHeader>
        <TableBody>
          {rows.map((row) => (
            <TableRow key={row.id}>
              <TableCell className="max-w-0">
                <a
                  href="#"
                  title={row.name}
                  className="block truncate text-primary hover:underline"
                >
                  {row.name}
                </a>
              </TableCell>
              <TableCell className="whitespace-nowrap">
                <StatusBadge status={row.status} />
              </TableCell>
              <TableCell className="whitespace-nowrap px-2 text-right">
                <a
                  href="#"
                  aria-label="Open check"
                  className="inline-flex text-muted-foreground hover:text-foreground"
                >
                  <ArrowUpRight className="h-3.5 w-3.5" />
                </a>
              </TableCell>
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
          <code>onKeyDown</code>. The row must contain no nested links or
          buttons so the click target is unambiguous.
        </p>
        <ClickableTable />
      </div>
      <CodeSnippet
        code={`import { useNavigate } from "@tanstack/react-router";\n\nconst navigate = useNavigate();\nconst open = () =>\n  void navigate({ to: "/orgs/$org/widgets/$uid", params: { org, uid: row.uid } });\n\n<TableRow\n  className="cursor-pointer hover:bg-muted/50"\n  role="link"\n  tabIndex={0}\n  onClick={open}\n  onKeyDown={(e) => {\n    if (e.key === "Enter" || e.key === " ") {\n      e.preventDefault();\n      open();\n    }\n  }}\n>\n  {/* cells — no nested <Link>/<Button> */}\n</TableRow>`}
      />

      <div className="space-y-2 pt-2">
        <h3 className="text-sm font-medium">Truncated cell</h3>
        <p className="text-sm text-muted-foreground">
          When a column's value has no natural break point (a UUID, a long URL),
          let it truncate instead of wrapping to several lines or forcing the
          table wider than its container. Give the <code>TableCell</code> itself{" "}
          <code>max-w-0</code> — not just the text node inside it — so the
          browser's table layout algorithm shrinks that column to its fair share
          instead of growing to fit the content; other columns that must stay
          one line (a badge, an icon-link) get <code>whitespace-nowrap</code> so
          the flexible column absorbs whatever width is left. Pair the truncated
          element with a <code>title</code> attribute (or Tooltip, above) so the
          full value is still reachable on hover/focus. Two link targets in one
          row (here: the name → detail page, the trailing icon → a related page)
          stay distinguishable by weight — underlined text vs. a muted icon —
          rather than by color alone.
        </p>
        <TruncatedCellTable />
      </div>
      <CodeSnippet
        code={`<TableHead>Check</TableHead>\n<TableHead className="whitespace-nowrap">State</TableHead>\n<TableHead className="whitespace-nowrap px-2" />\n\n<TableCell className="max-w-0">\n  <Link to="..." title={name} className="block truncate text-primary hover:underline">\n    {name}\n  </Link>\n</TableCell>\n<TableCell className="whitespace-nowrap">\n  <Badge>{state}</Badge>\n</TableCell>\n<TableCell className="whitespace-nowrap px-2 text-right">\n  <Link to="..." aria-label="Open check" className="inline-flex text-muted-foreground hover:text-foreground">\n    <ArrowUpRight className="h-3.5 w-3.5" />\n  </Link>\n</TableCell>`}
      />
    </Section>
  );
}

/* Two example tables sharing the same tier logic as the checks and incidents
 * list pages (spec 2026-08-28-08): identity + the one "live signal" column +
 * row actions stay visible at every width; the widest or most redundant
 * columns fold away first via `hidden <bp>:table-cell`. Shrink the browser
 * window (or the design-reference iframe) to see Type/Target/Status drop off
 * below `sm`/`md`. */
function ResponsiveDemoTable() {
  const rows: {
    id: string;
    name: string;
    type: string;
    target: string;
    status: "up" | "warning";
    response: string;
  }[] = [
    {
      id: "1",
      name: "api.acme.com health",
      type: "HTTP",
      target: "https://api.acme.com/health",
      status: "up",
      response: "42ms",
    },
    {
      id: "2",
      name: "checkout-prod-gateway-eu",
      type: "TCP",
      target: "checkout.acme.com:443",
      status: "warning",
      response: "812ms",
    },
  ];

  return (
    <div className="rounded-md border">
      <Table>
        <TableHeader>
          <TableRow>
            <TableHead>Name</TableHead>
            <TableHead className="hidden sm:table-cell">Type</TableHead>
            <TableHead className="hidden md:table-cell">Target</TableHead>
            <TableHead className="hidden md:table-cell">Status</TableHead>
            <TableHead>Response</TableHead>
          </TableRow>
        </TableHeader>
        <TableBody>
          {rows.map((row) => (
            <TableRow key={row.id}>
              <TableCell className="max-w-0">
                <div className="flex min-w-0 items-center gap-2">
                  <StatusDot status={row.status} />
                  <span className="min-w-0 truncate">{row.name}</span>
                </div>
              </TableCell>
              <TableCell className="hidden sm:table-cell">
                {row.type}
              </TableCell>
              <TableCell className="hidden max-w-[220px] truncate font-mono text-xs text-muted-foreground md:table-cell">
                {row.target}
              </TableCell>
              <TableCell className="hidden md:table-cell">
                <StatusBadge status={row.status} />
              </TableCell>
              <TableCell className="font-mono text-xs">
                {row.response}
              </TableCell>
            </TableRow>
          ))}
        </TableBody>
      </Table>
    </div>
  );
}

function ResponsiveTableSection() {
  return (
    <Section
      id="responsive-table"
      title="Responsive table"
      description="Hide secondary columns below a breakpoint instead of letting a wide table fall back to horizontal scroll. Used by the checks and incidents list pages (spec 2026-08-28-08); the canonical pattern for any list page with more columns than fit a phone."
    >
      <ResponsiveDemoTable />
      <CodeSnippet
        code={`<TableHead>Name</TableHead>\n<TableHead className="hidden sm:table-cell">Type</TableHead>\n<TableHead className="hidden md:table-cell">Target</TableHead>\n\n<TableCell className="max-w-0">\n  <div className="flex min-w-0 items-center gap-2">\n    <StatusDot status={row.status} />\n    {/* min-w-0 on the span too — a flex item's default min-width:auto\n        stops "truncate" from ever actually shrinking it below its\n        content size. */}\n    <span className="min-w-0 truncate">{row.name}</span>\n  </div>\n</TableCell>\n<TableCell className="hidden sm:table-cell">{row.type}</TableCell>\n<TableCell className="hidden md:table-cell">{row.target}</TableCell>`}
      />
      <ul className="list-disc space-y-1.5 pl-5 text-sm text-muted-foreground">
        <li>
          <strong className="text-foreground">Always the head/cell pair.</strong>{" "}
          Apply the exact same <code>hidden &lt;bp&gt;:table-cell</code> class
          to the <code>TableHead</code> AND every matching{" "}
          <code>TableCell</code> in that column. A mismatched pair doesn't
          error — it silently shifts every following column under the wrong
          header, and only at the breakpoint where it applies, so it's easy
          to ship and hard to notice in review.
        </li>
        <li>
          <strong className="text-foreground">Tier by importance, not by width.</strong>{" "}
          Identity (name/title, ideally with its status dot folded in), the
          one live/primary signal, and row actions stay visible at every
          width. The widest or most redundant columns — a status badge next
          to a dot that already conveys it, a "check" column that duplicates
          the row's own title — fold away first, at{" "}
          <code>sm</code> then <code>md</code>.
        </li>
        <li>
          <strong className="text-foreground">
            Hiding columns alone doesn't stop overflow.
          </strong>{" "}
          A remaining cell whose content can't shrink — a long check name, an
          incident title, a UUID — still forces the page wider even after
          every secondary column is gone. Give that <code>TableCell</code>{" "}
          <code>max-w-0</code> and its content <code>truncate</code> (add an
          explicit <code>max-w-[…]</code> when the cell holds more than one
          flex child, as in the incidents title cell's dot + badges + title).
          When the truncated element is itself a flex item (inside a{" "}
          <code>flex</code> row, not just a plain block), it also needs its
          own <code>min-w-0</code> — a flex item's default{" "}
          <code>min-width: auto</code> stops <code>truncate</code> from ever
          shrinking it below its content size, so the class is present but
          silently does nothing. A badge cluster in the same cell needs{" "}
          <code>flex-wrap</code> too, or a badge-heavy row overflows
          regardless of column hiding. Verify
          against <code>document.documentElement.scrollWidth {"<="} clientWidth</code>{" "}
          at 375px — the real invariant, not just "the columns look hidden."
        </li>
      </ul>
    </Section>
  );
}

function CopyableCodeSection() {
  return (
    <Section
      id="copyable-code"
      title="Copyable code"
      description="A short, always-visible monospace value (URL, one-liner command) with an inline copy-to-clipboard button. Use for values that should never require an extra click to reveal. For long or optional payloads, use Collapsible code below instead. Lives on the AI assistants (MCP) page."
    >
      <div className="grid gap-3 rounded-md border bg-card p-4 md:grid-cols-[minmax(0,1fr)_minmax(0,1fr)] md:items-start">
        <div className="space-y-2">
          <CopyableCode code="https://monitoring.example.com/api/v1/mcp" />
          <CopyableCode code="claude mcp add --transport http solidping <url>" />
        </div>
        <CodeSnippet
          code={`import { CopyableCode } from "@/components/shared/copyable-code";\n\n<CopyableCode code="https://monitoring.example.com/api/v1/mcp" />`}
        />
      </div>
    </Section>
  );
}

function CopyableInlineSection() {
  return (
    <Section
      id="copyable-inline"
      title="Copyable inline"
      description="A copy-to-clipboard icon button for a value shown elsewhere on the page — a form-field-style ID/URL row (inline, the default) or a bare button next to a caller-rendered value block, e.g. a <pre> error box (inline={false}). For a boxed, always-visible code block use Copyable code above; for a collapsible block use Collapsible code below. Lives on the notification delivery detail page."
    >
      <div className="grid gap-3 rounded-md border bg-card p-4 md:grid-cols-[minmax(0,1fr)_minmax(0,1fr)] md:items-start">
        <div className="space-y-3">
          <div className="space-y-1">
            <div className="text-muted-foreground text-xs">Request URL</div>
            <CopyableInline
              value="https://hooks.example.com/incoming/018e"
              label="request URL"
            />
          </div>
          <div className="flex items-start gap-2">
            <pre className="min-w-0 flex-1 overflow-x-auto whitespace-pre-wrap break-words rounded-md bg-muted p-3 font-mono text-xs text-destructive">
              connect ECONNREFUSED
            </pre>
            <CopyableInline
              value="connect ECONNREFUSED"
              label="error"
              inline={false}
              size="md"
            />
          </div>
        </div>
        <CodeSnippet
          code={`import { CopyableInline } from "@/components/shared/copyable-code";\n\n// Form-field-style row (inline, default)\n<CopyableInline value={url} label="request URL" />\n\n// Bare button next to a caller-rendered value block\n<CopyableInline value={error} label="error" inline={false} size="md" />`}
        />
      </div>
    </Section>
  );
}

function DnsRecordRowSection() {
  return (
    <Section
      id="dns-record-row"
      title="DNS record row"
      description="A single DNS record the user must create — a labeled Type / Name / Value grid with copy-to-clipboard buttons on the copyable values. Used by the status-page custom-domain section, which since v0.8.0 asks for exactly one record: the routing CNAME (shared mode below, or the per-page token target in token mode)."
    >
      <div className="grid gap-3 rounded-md border bg-card p-4 md:grid-cols-[minmax(0,1fr)_minmax(0,1fr)] md:items-start">
        <div className="space-y-2">
          <DnsRecordRow
            record={{
              type: "CNAME",
              name: "status.acme.com",
              value: "cname.solidping.io",
            }}
          />
          <DnsRecordRow
            record={{
              type: "CNAME",
              name: "status.acme.com",
              value: "spq7f3k2m6x4t7b.cname.solidping.io",
            }}
          />
        </div>
        <CodeSnippet
          code={`import { DnsRecordRow } from "@/components/shared/dns-record-row";\n\n<DnsRecordRow record={{ type: "CNAME", name: "status.acme.com", value: "cname.solidping.io" }} />`}
        />
      </div>
    </Section>
  );
}

function CollapsibleCodeSection() {
  return (
    <Section
      id="collapsible-code"
      title="Collapsible code"
      description="A native <details> disclosure wrapping a copyable monospace block. Use for long, optional payloads (webhook request/response bodies, raw JSON) that should default collapsed but stay one click from view. Keyboard- and touch-friendly; no extra dependency. Shared by the notification delivery detail page and the AI assistants page."
    >
      <div className="grid gap-3 rounded-md border bg-card p-4 md:grid-cols-[minmax(0,1fr)_minmax(0,1fr)] md:items-start">
        <div className="space-y-2">
          <CollapsibleCode
            label="Request payload"
            value={`{\n  "type": "incident.created",\n  "data": { "incident": { "uid": "018e…" } }\n}`}
          />
          <CollapsibleCode
            label="Response body"
            value={`{ "error": "service unavailable" }`}
            defaultOpen
          />
        </div>
        <CodeSnippet
          code={`import { CollapsibleCode } from "@/components/shared/copyable-code";\n\n<CollapsibleCode label="Response body" value={json} defaultOpen={failed} />`}
        />
      </div>
    </Section>
  );
}

// A 1x1 transparent PNG: the reference page must be self-contained, so the
// sample image is inline rather than a network fetch.
const sampleEvidenceImage =
  "data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg==";

function EvidenceImageSection() {
  return (
    <Section
      id="evidence-image"
      title="Evidence image (captioned)"
      description="A captured image shown as evidence, wrapped in <figure>/<figcaption>. Use whenever an image is a PROBE ARTIFACT rather than decoration: the caption must say when and from where it was captured, and must not overclaim what it proves. The image is a link to its own full-resolution URL (no lightbox dependency), and scales to the container so it stays usable on a phone. Used by the incident screenshot card."
    >
      <div className="grid gap-3 rounded-md border bg-card p-4 md:grid-cols-[minmax(0,1fr)_minmax(0,1fr)] md:items-start">
        <figure className="space-y-2">
          <a
            href={sampleEvidenceImage}
            target="_blank"
            rel="noopener noreferrer"
            className="block overflow-hidden rounded-md border bg-muted"
          >
            <img
              src={sampleEvidenceImage}
              alt="Sample captured evidence"
              loading="lazy"
              className="h-24 w-full max-w-full object-cover"
            />
          </a>
          <figcaption className="text-xs text-muted-foreground">
            Captured 21/08/2026, 09:14:02 from eu-west, shortly after failure
            detection.
          </figcaption>
        </figure>
        <CodeSnippet
          code={`<figure className="space-y-2">\n  <a href={url} target="_blank" rel="noopener noreferrer"\n     className="block overflow-hidden rounded-md border bg-muted">\n    <img src={url} alt={alt} loading="lazy" className="h-auto w-full max-w-full" />\n  </a>\n  <figcaption className="text-xs text-muted-foreground">{caption}</figcaption>\n</figure>`}
        />
      </div>
    </Section>
  );
}

function CollapsibleSectionSection() {
  const [errSignal, setErrSignal] = useState(0);
  return (
    <Section
      id="collapsible-section"
      title="Collapsible section"
      description="Progressive-disclosure section for long forms: a header that shows the section title, a value-summary line while collapsed, and a 'customized' badge when values deviate from defaults. Bump expandSignal to force-open + scroll a section (validation error on submit, or a ?section= deep-link). Default it open when it already holds non-default values. Used to tame the check create/edit form."
    >
      <div className="grid gap-3 rounded-md border bg-card p-4 md:grid-cols-[minmax(0,1fr)_minmax(0,1fr)] md:items-start">
        <div className="space-y-3">
          <CollapsibleSection
            id="demo-flapping"
            title="Flapping"
            summary="window 6h, cooldown ×5 (defaults)"
          >
            <p className="text-sm text-muted-foreground">
              Tuning knobs live here — collapsed by default because most users
              never touch them.
            </p>
          </CollapsibleSection>
          <CollapsibleSection
            title="Incident tracking"
            summary="confirm 300s, recover 120s"
            customized
            defaultOpen
          >
            <p className="text-sm text-muted-foreground">
              Opens by default and shows the "customized" badge because its
              values deviate from the defaults.
            </p>
          </CollapsibleSection>
          <CollapsibleSection
            title="Advanced"
            summary="timeout 15s (default)"
            expandSignal={errSignal}
          >
            <p className="text-sm text-destructive">
              A field in here has an error — the section force-expanded.
            </p>
          </CollapsibleSection>
          <Button
            size="sm"
            variant="outline"
            onClick={() => setErrSignal((n) => n + 1)}
          >
            Simulate submit error (expand "Advanced")
          </Button>
        </div>
        <CodeSnippet
          code={`import { CollapsibleSection } from "@/components/ui/collapsible-section";\n\n<CollapsibleSection\n  id="flapping"\n  title="Flapping"\n  summary="window 6h, cooldown ×5 (defaults)"\n  customized={isCustomized}\n  defaultOpen={hasNonDefaults}\n  expandSignal={hasError ? submitNonce : 0}\n>\n  {/* fields */}\n</CollapsibleSection>`}
        />
      </div>
    </Section>
  );
}

/** Escapes a value for safe interpolation into a double-quoted HTML attribute
 * — same helper `StatusPageWidgetCard` uses to build its preview `srcDoc`. */
function demoEscapeHtmlAttr(value: string): string {
  return value
    .replace(/&/g, "&amp;")
    .replace(/"/g, "&quot;")
    .replace(/</g, "&lt;")
    .replace(/>/g, "&gt;");
}

function SandboxedPreviewSection() {
  const [label, setLabel] = useState('Try: "><b>bold</b>');
  // The frame has an opaque origin, so it cannot read the app's CSS variables —
  // it renders on browser defaults (black on transparent) and goes unreadable
  // over a dark page. Hand it concrete colors instead, the same way
  // StatusPageWidgetCard builds its own preview.
  const dark = useIsDarkTheme();
  const surface = dark
    ? { background: "#0b1220", color: "#e5e7eb", border: "#374151" }
    : { background: "#f8fafc", color: "#1f2937", border: "#d1d5db" };
  const srcDoc = `<!doctype html><html><body style="margin:0;padding:16px;font-family:ui-sans-serif,system-ui,sans-serif;display:flex;align-items:center;justify-content:center;height:100%;box-sizing:border-box;background:${surface.background};color:${surface.color};">
  <span data-label="${demoEscapeHtmlAttr(label)}" style="border:1px solid ${surface.border};border-radius:9999px;padding:6px 12px;">${demoEscapeHtmlAttr(label)}</span>
</body></html>`;

  return (
    <Section
      id="sandboxed-preview"
      title="Sandboxed preview (iframe)"
      description={
        "Renders third-party or user-typed HTML byte-for-byte — not a React " +
        "replica that could drift — via a sandboxed <iframe srcDoc>. Two rules " +
        'keep it safe: sandbox="allow-scripts" only (no allow-same-origin, so ' +
        "the frame gets an opaque origin and can't reach the parent document " +
        "or its storage), and every interpolated value is HTML-attribute-" +
        "escaped before it goes into the srcDoc string — the preview parses " +
        "user input as markup, so an unescaped quote could inject a new " +
        "attribute or tag. Used by StatusPageWidgetCard to preview the real " +
        "/embed/v1/widget.js script with the operator's own label overrides."
      }
    >
      <div className="grid gap-3 rounded-md border bg-card p-4 md:grid-cols-[minmax(0,1fr)_minmax(0,1fr)] md:items-start">
        <div className="space-y-3">
          <div className="space-y-2">
            <Label htmlFor="demo-sandboxed-preview-input">
              Label (try a quote-breakout attempt)
            </Label>
            <Input
              id="demo-sandboxed-preview-input"
              value={label}
              onChange={(event) => setLabel(event.target.value)}
            />
          </div>
          <div className="overflow-hidden rounded-lg border border-dashed">
            <iframe
              title="Sandboxed preview demo"
              srcDoc={srcDoc}
              sandbox="allow-scripts"
              className="h-28 w-full"
            />
          </div>
          <p className="text-xs text-muted-foreground">
            The typed value renders as inert text even when it contains markup —
            escaping happens before the string ever reaches the iframe's HTML
            parser. The frame also gets explicit background and text colors: an
            opaque origin can't read the app's CSS variables, so a frame left on
            browser defaults renders black-on-transparent and disappears against
            dark mode.
          </p>
        </div>
        <CodeSnippet
          code={`function escapeHtmlAttr(value: string): string {\n  return value\n    .replace(/&/g, "&amp;")\n    .replace(/"/g, "&quot;")\n    .replace(/</g, "&lt;")\n    .replace(/>/g, "&gt;");\n}\n\n// Sandboxed frames can't see --background / --foreground: hand them\n// concrete colors, tracked off the app's own light/dark class.\nconst dark = useIsDarkTheme();\nconst surface = dark\n  ? { background: "#0b1220", color: "#e5e7eb" }\n  : { background: "#f8fafc", color: "#1f2937" };\n\nconst srcDoc = \`<!doctype html><html>...\n  <body style="background:\${surface.background};color:\${surface.color}">\n    <span>\${escapeHtmlAttr(userValue)}</span>\n  </body>\n...</html>\`;\n\n<iframe srcDoc={srcDoc} sandbox="allow-scripts" title="Preview" />`}
        />
      </div>
    </Section>
  );
}

function StepperSection() {
  return (
    <Section
      id="stepper"
      title="Stepper"
      description="Horizontal progress indicator for multi-step wizards (e.g. the guided agent-registration flow). Wraps via flex-wrap so labels stack under their circle instead of overflowing on a narrow phone. Step index and any sensitive step data (a one-shot secret, say) should live in component state, not the URL — a reload can't recover a secret that was shown exactly once anyway."
    >
      <div className="space-y-4 rounded-md border bg-card p-4">
        <Stepper
          steps={[
            { label: "Pick location" },
            { label: "Mint token" },
            { label: "Run the agent" },
            { label: "Wait for connection" },
          ]}
          current={2}
        />
      </div>
      <CodeSnippet
        code={`import { Stepper } from "@/components/ui/stepper";
import {
  PagingCoverageCell,
  EmailOnlyBadge,
} from "@/components/notifications/member-coverage";\n\n<Stepper\n  steps={[{ label: "Pick location" }, { label: "Mint token" }, { label: "Run the agent" }, { label: "Wait for connection" }]}\n  current={step}\n/>`}
      />
    </Section>
  );
}

function FeedbackSection() {
  const { org } = Route.useParams();

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
                <AlertDescription>
                  Neutral, informational message.
                </AlertDescription>
              </Alert>
              <Alert variant="success">
                <CheckCircle2 />
                <AlertTitle>Success</AlertTitle>
                <AlertDescription>
                  The action completed successfully.
                </AlertDescription>
              </Alert>
              <Alert variant="warning">
                <AlertTriangle />
                <AlertTitle>Warning</AlertTitle>
                <AlertDescription>
                  Something is degraded but still functional.
                </AlertDescription>
              </Alert>
              <Alert variant="destructive">
                <AlertCircle />
                <AlertTitle>Destructive</AlertTitle>
                <AlertDescription>
                  An error occurred. Action failed.
                </AlertDescription>
              </Alert>
            </div>
          }
          importLine={`import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert";`}
        />

        <h3 className="text-sm font-medium">Over-limit banner</h3>
        <p className="text-sm text-muted-foreground">
          An org-level entitlement breach that is silently costing the customer
          something — here, check executions dropped by the per-minute rate
          gate. It is an amber <code className="rounded bg-muted px-1 py-0.5 text-xs">warning</code>{" "}
          Alert, never destructive: nothing is permanently lost and the remedy
          is the customer&apos;s to choose. It renders nothing when the org is
          inside its cap, so a page can mount it unconditionally.
        </p>
        <ExampleRow
          preview={
            <div className="w-full max-w-md">
              <CheckRateLimitBanner
                org={org}
                checksPerMinute={{ demand: 240, limit: 120, skippedToday: 613 }}
                showUsageLink
              />
            </div>
          }
          importLine={`import { CheckRateLimitBanner } from "@/components/shared/check-rate-limit-banner";`}
        />

        <h3 className="text-sm font-medium">Quota meter (pending draft)</h3>
        <p className="text-sm text-muted-foreground">
          A quota bar on a page that <em>edits</em> what fills it. The saved
          figure is struck through and the draft figure follows an arrow, so the
          consequence of an unsaved change is legible before anything is
          written — a bar showing only the saved number turns such a page into a
          guessing game. Over the cap it goes amber, matching the over-limit
          banner it sits with; red is reserved for destructive states. An absent
          limit means unlimited: the figure stays, the bar goes away, because
          there is nothing to be a fraction of.
        </p>
        <ExampleRow
          preview={
            <div className="w-full max-w-md space-y-6">
              <CheckRateMeter saved={84} draft={84} limit={120} />
              <CheckRateMeter saved={240} draft={96} limit={120} />
              <CheckRateMeter saved={240} draft={240} limit={null} />
            </div>
          }
          importLine={`import { CheckRateMeter } from "@/components/shared/check-rate-meter";\n\n<CheckRateMeter saved={savedTotal} draft={draftTotal} limit={limit} />`}
        />

        <h3 className="text-sm font-medium">Tinted panel</h3>
        <p className="text-sm text-muted-foreground">
          A sub-surface that needs to read as visually distinct from the{" "}
          <code className="rounded bg-muted px-1 py-0.5 text-xs">bg-card</code>{" "}
          it sits on — never a bare border alone. Neutral content (e.g. the
          incident timeline entries) uses{" "}
          <code className="rounded bg-muted px-1 py-0.5 text-xs">
            bg-muted/30
          </code>
          ; content that IS the error (e.g. the incident detail failure
          snapshot) uses the destructive tint plus{" "}
          <code className="rounded bg-muted px-1 py-0.5 text-xs">
            text-destructive
          </code>{" "}
          on the error text itself, so it reads as &quot;this is the error&quot;
          at a glance. Tokens only, no raw hex, so both stay correct in dark
          mode.
        </p>
        <ExampleRow
          preview={
            <div className="flex w-full max-w-md flex-col gap-2">
              <div className="space-y-1 rounded-md border bg-muted/30 p-3 text-sm">
                <div className="text-xs text-muted-foreground">
                  Neutral (timeline entries)
                </div>
                <div>
                  Use for grouped detail that isn&apos;t itself an error.
                </div>
              </div>
              <div className="space-y-1 rounded-lg border border-destructive/30 bg-destructive/5 p-3 text-sm">
                <div className="text-xs text-muted-foreground">
                  Destructive (failure snapshot)
                </div>
                <div className="font-mono text-destructive">
                  connect ECONNREFUSED
                </div>
              </div>
            </div>
          }
          importLine={`// Neutral\n<div className="rounded-md border bg-muted/30 p-3">...</div>\n\n// Destructive (this IS the error)\n<div className="rounded-lg border border-destructive/30 bg-destructive/5 p-4">\n  <div className="font-mono text-destructive">{errorText}</div>\n</div>`}
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
                  <DialogDescription>
                    Modal content for non-destructive flows.
                  </DialogDescription>
                </DialogHeader>
                <p className="text-sm">
                  Use Dialog for inline forms, multi-step pickers, or any
                  cancelable flow that doesn&apos;t carry irreversible
                  consequences.
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
                    This action is permanent. Use AlertDialog (not Dialog) for
                    destructive confirmations.
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

        <h3 className="text-sm font-medium">Danger zone + confirm-by-typing</h3>
        <ExampleRow
          preview={
            <DangerZone
              title="Danger zone"
              description="Irreversible actions live here, at the bottom of a settings page."
            >
              <ConfirmByTypingButton
                buttonLabel="Delete organization"
                title="Delete this organization?"
                description="Everything it owns stops working. This cannot be undone."
                inputLabel="Type acme to confirm"
                confirmValue="acme"
                confirmLabel="Delete organization"
                onConfirm={() => {
                  toast.success("Confirmed");
                }}
              />
            </DangerZone>
          }
          importLine={`import {\n  ConfirmByTypingButton,\n  DangerZone,\n} from "@/components/shared/danger-zone";`}
        />

        <h3 className="text-sm font-medium">Toast (sonner)</h3>
        <p className="text-sm text-muted-foreground">
          Confirmation for an action the operator just took — never for
          information they have to keep. A toast keeps the neutral popover
          surface and carries its meaning in the{" "}
          <strong>icon color alone</strong> (
          <code className="mx-1 rounded bg-muted px-1 py-0.5 text-xs">
            --status-ok
          </code>{" "}
          /{" "}
          <code className="mx-1 rounded bg-muted px-1 py-0.5 text-xs">
            --status-warning
          </code>{" "}
          /{" "}
          <code className="mx-1 rounded bg-muted px-1 py-0.5 text-xs">
            --status-error
          </code>
          ), so success and failure read apart at a glance without a saturated
          banner sliding over the UI. That mapping lives once in{" "}
          <code className="mx-1 rounded bg-muted px-1 py-0.5 text-xs">
            components/ui/sonner.tsx
          </code>{" "}
          — call the typed helper and the color follows; never hand-color a
          toast at the call site.
        </p>
        <p className="text-sm text-muted-foreground">
          <strong>Always use a typed helper.</strong> A bare{" "}
          <code className="mx-1 rounded bg-muted px-1 py-0.5 text-xs">
            toast("…")
          </code>{" "}
          has no type, and sonner gives an untyped toast no icon at all — it
          arrives as unlabelled text, and there is no setting that adds one,
          because the icon lookup is keyed on the type it's missing. A neutral,
          non-status message is{" "}
          <code className="mx-1 rounded bg-muted px-1 py-0.5 text-xs">
            toast.info()
          </code>
          , which carries the blue (i).
        </p>
        <ExampleRow
          preview={
            <div className="flex flex-wrap gap-2">
              <Button
                variant="outline"
                onClick={() =>
                  toast.info("Maintenance window starts in 10 minutes")
                }
              >
                Info toast
              </Button>
              <Button
                variant="outline"
                onClick={() => toast.success("Check enabled")}
              >
                Success toast
              </Button>
              <Button
                variant="outline"
                onClick={() => toast.warning("Check is flapping")}
              >
                Warning toast
              </Button>
              <Button
                variant="outline"
                onClick={() => toast.error("Something went wrong")}
              >
                Error toast
              </Button>
            </div>
          }
          importLine={`import { toast } from "sonner";\n\n// Typed helpers only — the icon and its color come from the type.\ntoast.success("Check enabled");\ntoast.warning("Check is flapping");\ntoast.error("Something went wrong");\ntoast.info("Maintenance window starts in 10 minutes");\n\n// Never a bare toast("…"): an untyped toast renders with no icon at\n// all, and sonner exposes no way to give it one.`}
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
                  Popover content — useful for inline pickers and contextual
                  hints that need more space than a tooltip.
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

        <h3 className="text-sm font-medium">Error state (boundary fallback)</h3>
        <ExampleRow
          preview={
            <ErrorFallbackCard
              error={
                new Error(
                  "Example failure — shown inside a collapsible details block",
                )
              }
              onRetry={() => toast.info("Retry clicked")}
            />
          }
          importLine={`import { ErrorFallbackCard, RouteErrorFallback } from "@/components/shared/error-boundary";\n// RouteErrorFallback is already wired as the router's defaultErrorComponent\n// (main.tsx): route errors render this card inside the layout, keeping the\n// sidebar usable. Reuse ErrorFallbackCard for any custom error surface.`}
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
          Brand swatches (kept distinct from primary / destructive /
          status-error)
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

// CHECK_TYPE_FAMILY_TABLE documents the family → tone grouping for the
// design-reference page. It is display-only prose (kept in sync with
// CHECK_TYPE_IDENTITY by eye, same as the spec's own table) — the canonical,
// enforced source is CHECK_TYPE_IDENTITY in check-type-identity.tsx, guarded
// by check-type-identity.test.ts.
const CHECK_TYPE_FAMILY_TABLE: {
  family: string;
  types: string;
  tone: string;
}[] = [
  {
    family: "Web",
    types: "http/https, websocket, browser",
    tone: "blue (shipped)",
  },
  {
    family: "Raw network",
    types: "tcp, udp, ntp, snmp",
    tone: "cyan (shipped)",
  },
  { family: "Naming", types: "dns, domain, dnsbl", tone: "amber (shipped)" },
  { family: "Reachability", types: "icmp/ping", tone: "purple (shipped)" },
  { family: "Certificates", types: "ssl/tls", tone: "emerald (shipped)" },
  { family: "Remote access", types: "ssh, sftp, ftp, rdp", tone: "teal" },
  { family: "Mail", types: "smtp, pop3, imap, email", tone: "rose" },
  {
    family: "Databases",
    types: "postgresql, mysql, mssql, oracle, clickhouse, redis, mongodb",
    tone: "indigo",
  },
  {
    family: "Messaging/RPC",
    types: "grpc, kafka, mqtt, rabbitmq",
    tone: "fuchsia",
  },
  { family: "Game", types: "a2s, minecraft", tone: "lime" },
  {
    family: "Infra",
    types: "docker, prometheus, freebox_line, kubernetes",
    tone: "sky",
  },
  {
    family: "Scripted/synthetic",
    types: "js, sleep, heartbeat, sip",
    tone: "slate",
  },
];

const CHECK_TYPE_BADGE_SAMPLES = [
  "http",
  "tcp",
  "dns",
  "icmp",
  "ssl",
  "ssh",
  "smtp",
  "postgresql",
  "grpc",
  "minecraft",
  "docker",
  "heartbeat",
];

function CheckTypeIdentitySection() {
  return (
    <Section
      id="check-type-badge"
      title="Check type identity"
      description="The one canonical visual identity for a check type — label, tint, and icon — sourced from CHECK_TYPE_IDENTITY (check-type-identity.tsx) and used everywhere a check type is rendered: the checks list chip, the check detail page, and the new-check type picker. Keyed by the raw backend type string; a drift-guard test fails the build if a check type or docs-anchor entry ships without a registry entry."
    >
      <div className="space-y-2">
        <h3 className="text-sm font-medium">CheckTypeBadge — the 10px chip</h3>
        <p className="text-xs text-muted-foreground">
          Uppercase mono at 10px so a column of them aligns like a fixed-width
          key, one tinted family per type. Text-first, deliberately no icon
          inside the chip — at this size an abstract glyph is noise and the
          acronym is the signal. An unrecognized type falls back to the plain
          outline badge with its raw name, rather than inventing a new color.
        </p>
        <ExampleRow
          preview={
            <div className="flex flex-wrap items-center gap-2">
              {CHECK_TYPE_BADGE_SAMPLES.map((type) => (
                <CheckTypeBadge key={type} type={type} />
              ))}
              <CheckTypeBadge type="custom" />
            </div>
          }
          importLine={`import { CheckTypeBadge } from "@/components/shared/check-type-identity";\n\n<CheckTypeBadge type={check.type} />\n\n// Tone comes from the registry; an unknown type renders the plain\n// outline badge with its raw name — never a guessed color.`}
        />
      </div>

      <div className="space-y-2">
        <h3 className="text-sm font-medium">
          CheckTypeIcon — leading glyph (type picker, check detail)
        </h3>
        <p className="text-xs text-muted-foreground">
          A leading icon tinted to the same tone, for surfaces with room for one
          — the new-check type picker rows and the check detail header. Never
          inside the 10px badge itself. Lucide today; the slot accepts any
          component rendering <code>currentColor</code> on a square viewBox, so
          an internally designed icon set can replace entries in the registry
          later with no call-site changes.
        </p>
        <ExampleRow
          preview={
            <div className="flex flex-wrap items-center gap-3">
              {CHECK_TYPE_BADGE_SAMPLES.slice(0, 6).map((type) => (
                <span key={type} className="inline-flex items-center gap-1.5">
                  <CheckTypeIcon type={type} />
                  <CheckTypeBadge type={type} />
                </span>
              ))}
            </div>
          }
          importLine={`import { CheckTypeIcon, CheckTypeBadge } from "@/components/shared/check-type-identity";\n\n<span className="inline-flex items-center gap-1.5">\n  <CheckTypeIcon type={check.type} />\n  <CheckTypeBadge type={check.type} />\n</span>`}
        />
      </div>

      <div className="space-y-2">
        <h3 className="text-sm font-medium">Family → tone table</h3>
        <p className="text-xs text-muted-foreground">
          40 distinguishable hues don't exist, so tones are assigned per family.
          The five marked "shipped" are the original protocol-badge tints and
          must never change color — they're in users' muscle memory. Every other
          family gets its own hue, none colliding with the status colors
          (green=ok / red=down stay reserved).
        </p>
        <div className="overflow-x-auto rounded-md border">
          <table className="w-full text-left text-sm">
            <thead className="bg-muted/30 text-xs text-muted-foreground">
              <tr>
                <th className="px-3 py-2 font-medium">Family</th>
                <th className="px-3 py-2 font-medium">Types</th>
                <th className="px-3 py-2 font-medium">Tone</th>
              </tr>
            </thead>
            <tbody className="divide-y">
              {CHECK_TYPE_FAMILY_TABLE.map((row) => (
                <tr key={row.family}>
                  <td className="px-3 py-2 font-medium">{row.family}</td>
                  <td className="px-3 py-2 font-mono text-xs">{row.types}</td>
                  <td className="px-3 py-2">{row.tone}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      </div>

      <div className="space-y-2">
        <h3 className="text-sm font-medium">Accessibility</h3>
        <p className="text-xs text-muted-foreground">
          Same rule as every other tinted badge in this app: the tint and the
          icon are decoration layered on the label, never the only signal. The
          text label always spells the check type out —{" "}
          <code>CheckTypeBadge</code> renders it even for a type with no
          registry entry, and the icon is marked <code>aria-hidden</code>.
        </p>
      </div>
    </Section>
  );
}

const EVENT_TONE_SAMPLES: { type: string; label: string }[] = [
  { type: "incident.created", label: "Incident Opened" },
  { type: "incident.resolved", label: "Incident Resolved" },
  { type: "incident.acknowledged", label: "Incident Acknowledged" },
  { type: "check.updated", label: "Check Updated" },
  { type: "org.activation.signup_completed", label: "Signup Completed" },
  { type: "something.unmapped", label: "Unmapped" },
];

// EVENT_BADGE_SAMPLES demonstrates the canonical per-event-type registry
// (EVENT_TYPE_REGISTRY in event-display.tsx) — the emoji+tone pairing that is
// binding across dash0 AND the backend chat integrations (msteamsbot.go,
// Telegram, Slack). The last two rows are deliberately NOT in the registry,
// to show the family fallback still renders a sane badge.
const EVENT_BADGE_SAMPLES: { type: string; label: string }[] = [
  { type: "incident.created", label: "Incident Created" },
  { type: "incident.reopened", label: "Incident Reopened" },
  { type: "incident.escalated", label: "Incident Escalated" },
  { type: "incident.escalation_failed", label: "Escalation Failed" },
  { type: "incident.resolved", label: "Incident Resolved" },
  { type: "incident.acknowledged", label: "Incident Acknowledged" },
  { type: "incident.unacknowledged", label: "Incident Unacknowledged" },
  { type: "incident.snoozed", label: "Incident Snoozed" },
  { type: "check.updated", label: "Check Updated" },
  { type: "something.unmapped", label: "Unmapped" },
];

// designReferenceEventT is a stand-in for the real `t` from
// useTranslation("events") — this page is a static catalog, not localized —
// resolving `types.<eventType>` from the sample labels above and otherwise
// behaving like i18next's own `defaultValue` fallback.
function designReferenceEventT(
  key: string,
  options?: Record<string, unknown>,
): string {
  const sample = EVENT_BADGE_SAMPLES.find((s) => key === `types.${s.type}`);
  if (sample) return sample.label;
  const fallback = options?.defaultValue;
  return typeof fallback === "string" ? fallback : key;
}

// designReferenceFlappingT is a stand-in for the real `t` from
// useTranslation("incidents") — this page is a static catalog, not
// localized — reproducing the two keys FlappingBadge needs
// (locales/en/incidents.json: "flapping", "flappingHint").
function designReferenceFlappingT(
  key: string,
  options?: Record<string, unknown>,
): string {
  const count = typeof options?.count === "number" ? options.count : 0;
  if (key === "flapping") return `flapping ×${count}`;
  if (key === "flappingHint") {
    return `Opened at flap level ${count} — adaptive recovery escalated the required stability before this incident's auto-resolve.`;
  }
  return key;
}

function EventToneSection() {
  return (
    <Section
      id="event-tone"
      title="Event tone badge"
      description="The badge used for one row of an audit log, tinted by what KIND of thing happened so a long log can be triaged by color before it is read: failure and escalation red, recovery emerald, operator acknowledgement amber, configuration blue, onboarding milestones violet. Get the classes from getEventTone(eventType) rather than hand-picking a tint — that keeps every surface showing the same event agreeing on its color. Same rule as the protocol badge: the tint is decoration layered on the translated label, never the only signal, and an unmapped type falls back to the plain outline badge instead of inventing a seventh color."
    >
      <div className="space-y-2">
        <h3 className="text-sm font-medium">Tone families</h3>
        <ExampleRow
          preview={
            <div className="flex flex-wrap items-center gap-2">
              {EVENT_TONE_SAMPLES.map((sample) => (
                <Badge
                  key={sample.type}
                  variant="outline"
                  className={cn(
                    "gap-1.5 text-xs font-medium",
                    getEventTone(sample.type),
                  )}
                >
                  {sample.label}
                </Badge>
              ))}
            </div>
          }
          importLine={`import { getEventLabel, getEventTone } from "@/components/dashboard/event-display";\n\n<Badge\n  variant="outline"\n  className={cn("gap-1.5 text-xs font-medium", getEventTone(event.eventType))}\n>\n  {getEventLabel(event.eventType, t)}\n</Badge>\n\n// Pair it with a relative timestamp rather than a full locale string —\n// DurationAgo (non-ticking) is the right one for a historical log:\n//   <span title={new Date(event.createdAt).toLocaleString()}>\n//     <DurationAgo since={event.createdAt} />\n//   </span>`}
        />
      </div>
      <div className="space-y-2">
        <h3 className="text-sm font-medium">
          Per-event-type badge (emoji + label + tone)
        </h3>
        <p className="text-xs text-muted-foreground">
          <code>EventTypeBadge</code> is the canonical rendering of "which event
          was this" — used in notification lists (incident detail, notification
          detail), the events page, the dashboard feed, and the incident
          timeline. It layers a per-type emoji (from the EVENT_TYPE_REGISTRY map
          in event-display.tsx) on top of the same tone + label as above; a type
          with no registry entry (last two rows) still renders a plain badge via
          the family fallback. The emoji pairing is binding product-wide —
          msteamsbot.go, Telegram, and Slack are kept aligned to the same emoji
          per event type.
        </p>
        <ExampleRow
          preview={
            <div className="flex flex-wrap items-center gap-2">
              {EVENT_BADGE_SAMPLES.map((sample) => (
                <EventTypeBadge
                  key={sample.type}
                  eventType={sample.type}
                  t={designReferenceEventT}
                />
              ))}
            </div>
          }
          importLine={`import { EventTypeBadge } from "@/components/dashboard/event-display";\n\n<EventTypeBadge eventType={row.eventType} t={t} />`}
        />
      </div>
    </Section>
  );
}

function LiveDotSection() {
  return (
    <Section
      id="live-dot"
      title="Live & pulse dots"
      description="Bare status dots for list rows, where a full StatusDot with its label is too heavy. The pulsing variant marks a condition that is live RIGHT NOW (an active incident) — reserve the animation for that, because a page where several things pulse reads as noise and nothing draws the eye. Everything settled uses the static dot. For a labelled dot with an enabled/disabled state, use StatusDot instead."
    >
      <div className="space-y-2">
        <h3 className="text-sm font-medium">Pulsing (active incident)</h3>
        <ExampleRow
          preview={
            <div className="flex items-center gap-6">
              <span className="relative flex h-3 w-3" title="Active">
                <span className="animate-ping absolute inline-flex h-full w-full rounded-full bg-destructive opacity-75" />
                <span className="relative inline-flex h-3 w-3 rounded-full bg-destructive" />
              </span>
              <span className="text-muted-foreground text-xs">
                destructive · animate-ping
              </span>
            </div>
          }
          importLine={`// Two stacked spans: the ping expands and fades, the solid dot stays put.\n// The wrapper carries the tooltip so the animation never eats the hover.\n<span className="relative flex h-3 w-3" title={t("active")}>\n  <span className="animate-ping absolute inline-flex h-full w-full rounded-full bg-destructive opacity-75" />\n  <span className="relative inline-flex h-3 w-3 rounded-full bg-destructive" />\n</span>`}
        />
      </div>

      <div className="space-y-2">
        <h3 className="text-sm font-medium">Static (settled state)</h3>
        <ExampleRow
          preview={
            <div className="flex items-center gap-6">
              <span
                className="flex h-2.5 w-2.5 rounded-full bg-emerald-500"
                title="Resolved"
              />
              <span className="text-muted-foreground text-xs">
                emerald · no animation
              </span>
            </div>
          }
          importLine={`<span className="flex h-2.5 w-2.5 rounded-full bg-emerald-500" title={t("resolved")} />`}
        />
      </div>

      <div className="space-y-2">
        <h3 className="text-sm font-medium">
          radar-ping utility (heartbeat halo)
        </h3>
        <ExampleRow
          preview={
            <div className="flex items-center gap-6">
              <span className="radar-ping flex h-2.5 w-2.5 rounded-full bg-emerald-500" />
              <span className="text-muted-foreground text-xs">
                box-shadow halo · 2.5s loop
              </span>
            </div>
          }
          importLine={`// Defined in index.css (@utility radar-ping + @keyframes radar-pulse).\n// Unlike animate-ping this needs no second element — the halo is a\n// box-shadow on the dot itself, so it drops into a flex row cleanly.\n<span className="radar-ping flex h-2.5 w-2.5 rounded-full bg-emerald-500" />`}
        />
      </div>
    </Section>
  );
}

function ListSurfaceSection() {
  return (
    <Section
      id="list-surface"
      title="List surface"
      description="How an index page wraps its table: a card-elevated surface with the header tinted a step down from the rows, and a hover tint that makes the whole row read as one click target. Row titles are the only thing that underlines on hover — everything else in the row stays quiet so the eye lands on the name. Pair with the empty state below; a list that renders nothing must say so rather than showing an empty frame."
    >
      <div className="space-y-2">
        <h3 className="text-sm font-medium">Card-wrapped table</h3>
        <ExampleRow
          preview={
            <div className="w-full overflow-hidden rounded-xl border bg-card shadow-card">
              <Table>
                <TableHeader className="bg-muted/30">
                  <TableRow>
                    <TableHead>Name</TableHead>
                    <TableHead>Type</TableHead>
                    <TableHead className="w-[100px] text-right">
                      Interval
                    </TableHead>
                  </TableRow>
                </TableHeader>
                <TableBody>
                  {[
                    { name: "api.example.com", type: "http" },
                    { name: "db.internal", type: "tcp" },
                  ].map((row) => (
                    <TableRow
                      key={row.name}
                      className="transition-colors hover:bg-muted/40"
                    >
                      <TableCell>
                        <span className="font-medium text-foreground transition-colors hover:text-primary hover:underline">
                          {row.name}
                        </span>
                      </TableCell>
                      <TableCell>
                        <CheckTypeBadge type={row.type} />
                      </TableCell>
                      <TableCell className="text-right font-mono text-xs text-muted-foreground">
                        60s
                      </TableCell>
                    </TableRow>
                  ))}
                </TableBody>
              </Table>
            </div>
          }
          importLine={`// The card wrapper owns the radius + shadow; overflow-hidden keeps the\n// table's own corners inside it.\n<div className="rounded-xl border bg-card shadow-card overflow-hidden">\n  <Table>\n    <TableHeader className="bg-muted/30">…</TableHeader>\n    <TableBody>\n      <TableRow className="hover:bg-muted/40 transition-colors">\n        <TableCell>\n          <Link className="font-medium text-foreground hover:text-primary hover:underline transition-colors">\n            {check.name}\n          </Link>\n        </TableCell>\n      </TableRow>\n    </TableBody>\n  </Table>\n</div>`}
        />
      </div>

      <div className="space-y-2">
        <h3 className="text-sm font-medium">Empty state</h3>
        <p className="text-sm text-muted-foreground">
          The page's toolbar (search input, filters, Refresh — see{" "}
          <a href="#button-placement" className="text-primary hover:underline">
            Button placement
          </a>
          ) stays rendered above the empty state; only the table area swaps out,
          so the page doesn't jump. A truly empty list — zero rows, no filter
          applied — gets an icon, a title, and a one-line hint —{" "}
          <strong>no CTA button</strong>. The create action already lives once,
          in the page header (top right); repeating it inside the card would
          just duplicate it.
        </p>
        <ExampleRow
          preview={
            <div className="w-full space-y-3 rounded-xl border bg-card p-12 text-center shadow-card">
              <div className="mx-auto flex h-12 w-12 items-center justify-center rounded-full bg-muted">
                <Inbox className="h-6 w-6 text-muted-foreground" />
              </div>
              <p className="text-sm font-medium text-foreground">
                No checks yet
              </p>
              <p className="mx-auto max-w-sm text-xs text-muted-foreground">
                Create your first check to start monitoring.
              </p>
            </div>
          }
          importLine={`// Same card surface as the table it replaces, so the page doesn't jump.\n// Tint the icon circle when the emptiness is GOOD news (no open incidents):\n//   bg-emerald-500/10 text-emerald-600 dark:text-emerald-400\n// No CTA here — the create action already lives once, in the page header.\n<div className="rounded-xl border bg-card p-12 text-center shadow-card space-y-3">\n  <div className="mx-auto flex h-12 w-12 items-center justify-center rounded-full bg-muted">\n    <Inbox className="h-6 w-6 text-muted-foreground" />\n  </div>\n  <p className="font-medium text-sm text-foreground">No checks yet</p>\n  <p className="text-xs text-muted-foreground max-w-sm mx-auto">…</p>\n</div>`}
        />
      </div>

      <div className="space-y-2">
        <h3 className="text-sm font-medium">Empty state: no search matches</h3>
        <p className="text-sm text-muted-foreground">
          Rows exist, but the current search/filter hides all of them. Same card
          surface, a{" "}
          <code className="rounded bg-muted px-1 py-0.5 text-xs">Search</code>{" "}
          icon instead of the resource icon, title only —{" "}
          <strong>no CTA</strong> (the fix is to clear the filter, not to create
          a duplicate). Mirrors{" "}
          <code className="rounded bg-muted px-1 py-0.5 text-xs">
            escalation-policies.index.tsx
          </code>
          .
        </p>
        <ExampleRow
          preview={
            <div className="w-full space-y-3 rounded-xl border bg-card p-12 text-center shadow-card">
              <div className="mx-auto flex h-12 w-12 items-center justify-center rounded-full bg-muted">
                <Search className="h-6 w-6 text-muted-foreground" />
              </div>
              <p className="text-sm font-medium text-foreground">
                No checks match your search
              </p>
            </div>
          }
          importLine={`// Rows exist; the current filter just hides all of them — no CTA.\n<div className="rounded-xl border bg-card p-12 text-center shadow-card space-y-3">\n  <div className="mx-auto flex h-12 w-12 items-center justify-center rounded-full bg-muted">\n    <Search className="h-6 w-6 text-muted-foreground" />\n  </div>\n  <p className="font-medium text-sm text-foreground">No checks match your search</p>\n</div>`}
        />
      </div>

      <div className="space-y-2">
        <h3 className="text-sm font-medium">Row accents</h3>
        <ExampleRow
          preview={
            <div className="flex flex-wrap items-center gap-4">
              <span className="flex h-7 w-7 items-center justify-center rounded-lg bg-muted/60">
                <Inbox className="h-3.5 w-3.5 text-muted-foreground/70" />
              </span>
              <span className="flex h-6 w-6 shrink-0 items-center justify-center rounded-full bg-primary/10 text-[10px] font-bold text-primary">
                3
              </span>
              <span className="rounded bg-muted px-1.5 py-0.5 font-mono text-xs font-semibold text-muted-foreground">
                #42
              </span>
            </div>
          }
          importLine={`// Icon tile — a squircle behind a row-leading glyph\n<span className="flex h-7 w-7 items-center justify-center rounded-lg bg-muted/60" />\n\n// Ordinal pip — position in an ordered list (escalation steps, rotation order)\n<span className="flex h-6 w-6 shrink-0 items-center justify-center rounded-full bg-primary/10 text-[10px] font-bold text-primary" />\n\n// Mono ref chip — incident numbers, short ids\n<span className="font-mono text-xs font-semibold text-muted-foreground px-1.5 py-0.5 rounded bg-muted" />`}
        />
      </div>
    </Section>
  );
}

function ElevationSection() {
  return (
    <Section
      id="elevation"
      title="Elevation, aurora & glass"
      description="Depth tokens that add polish without adding a new color, already baked into Button's default variant and Card — reach for the utilities only when styling a bespoke surface. Two families: the action shadows (--shadow-primary / --shadow-destructive) tint with their own hue via color-mix and so track the theme automatically, while --shadow-card is a fixed neutral slate for ambient lift. The aurora panel + glass utility are for marketing surfaces ONLY (login split-screen, hero strips, empty-state splashes) — never operator data views."
    >
      <div className="space-y-2">
        <h3 className="text-sm font-medium">
          Elevation (action shadows tint with their hue; card shadow is neutral)
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
          importLine={`// Defined in index.css @theme.\n<div className="shadow-card" />      {/* cards, KPI tiles, list surfaces — ambient lift */}\n<Button className="shadow-primary" /> {/* primary CTA glow (default variant has it) */}\n<Card className="hover:shadow-card-hover transition" /> {/* lift on hover */}\n\n// --shadow-primary / --shadow-destructive use color-mix against their own\n// token, so they follow the theme. --shadow-card is a fixed slate rgba: it\n// reads correctly on light surfaces and stays deliberately near-invisible in\n// dark mode, where the card's own border does the separating instead.`}
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
        preview={<LabelFilter org={org} value={labels} onChange={setLabels} />}
        importLine={snippet}
      />
    </Section>
  );
}

function FacetedFilterSection() {
  const [selected, setSelected] = useState<string[]>(["down"]);
  const options = [
    { value: "up", label: "Up" },
    { value: "down", label: "Down" },
    { value: "validating", label: "Validating" },
    { value: "warning", label: "Warning" },
    { value: "created", label: "Pending" },
  ];
  const triggerLabel = facetedFilterTriggerLabel(selected, options, {
    all: "All statuses",
    count: (count) => `${count} statuses`,
    plusOne: (label, extra) => `${label} +${extra}`,
  });
  const snippet = `import { FacetedFilter } from "@/components/shared/faceted-filter";
import {
  facetedFilterTriggerLabel,
  parseFacetedFilterParam,
  serializeFacetedFilterParam,
} from "@/lib/faceted-filter";

// Multi-select popover for a small, known option set (status, check type) —
// the checkbox sibling of LabelFilter's open-ended key:value picker. The
// caller owns URL state: read selected values with parseFacetedFilterParam
// (lenient — unknown tokens are dropped so a stale URL never wedges the UI),
// compute the trigger text with facetedFilterTriggerLabel (none → "all",
// one → its label, two → "label +1", 3+ → "N selected"), and write back with
// serializeFacetedFilterParam.
const selected = parseFacetedFilterParam(statusParam, new Set(["up", "down", …]));
<FacetedFilter
  options={options}
  selected={selected}
  onChange={(next) =>
    void navigate({
      search: (prev) => ({ ...prev, status: serializeFacetedFilterParam(next) || undefined }),
      replace: true,
    })
  }
  triggerLabel={facetedFilterTriggerLabel(selected, options, statusFilterStrings)}
  testId="status-filter"
/>`;
  return (
    <Section
      id="faceted-filter"
      title="Faceted filter"
      description="Multi-select popover for a small, known option set — used for the checks-list status and check-type filters. A checkbox per option, trigger text reflects the selection (All / one label / label +1 / N selected). Reuse this instead of a single-value Select whenever several values can be picked at once; reuse LabelFilter instead when the facet is an open-ended key:value pair. Try it below — the trigger starts on “Down”."
    >
      <ExampleRow
        preview={
          <FacetedFilter
            options={options}
            selected={selected}
            onChange={setSelected}
            triggerLabel={triggerLabel}
            testId="design-reference-faceted-filter"
          />
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
      <div className="text-3xl font-bold tracking-tight tabular-nums">42</div>
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
        <Link to="/orgs/$org/checks" params={{ org }} className="block">
          <Card className="transition hover:-translate-y-0.5 hover:bg-accent/40 hover:shadow-card-hover">
            <CardHeader className="flex flex-row items-center justify-between space-y-0 pb-2">
              <CardTitle className="text-sm font-medium text-muted-foreground">
                Monitored
              </CardTitle>
              <AlertTriangle className="h-4 w-4 text-muted-foreground" />
            </CardHeader>
            <CardContent>
              <div className="text-3xl font-bold tracking-tight tabular-nums">
                42
              </div>
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
            <div className="text-3xl font-bold tracking-tight tabular-nums">
              99.98%
            </div>
            <p className="text-xs text-muted-foreground mt-1">24h window</p>
          </CardContent>
        </Card>
      </div>
      <CodeSnippet code={snippet} />
    </Section>
  );
}

function ClickableStatusBannerSection() {
  const { org } = Route.useParams();
  const snippet = `import { Link } from "@tanstack/react-router";
import { AlertTriangle } from "lucide-react";

// A full-width status banner that IS the fastest path to acting on the
// problem it announces, not just a description of it — incidents take
// priority over a bare down check, since an incident carries the
// ack/snooze/resolve workflow. Wrap in <Link>, never a div + onClick, so
// keyboard focus, middle-click and copy-link keep working. The hover
// affordance (cursor-pointer + a stronger border/background) is what tells
// the operator the whole card is clickable, not just a decorative alert.
<Link
  to="/orgs/$org/incidents"
  params={{ org }}
  search={{ state: "active", showSuppressed: undefined, checkUid: undefined }}
  data-testid="overall-status-banner"
  className="block"
>
  <div className="relative overflow-hidden rounded-xl border border-destructive/30 bg-destructive/10 p-3.5 sm:p-4 shadow-sm cursor-pointer transition hover:border-destructive/50 hover:bg-destructive/15">
    <div className="flex items-center gap-3">
      <AlertTriangle className="h-4 w-4 text-destructive" />
      <h2 className="text-sm font-semibold text-destructive">Issues detected</h2>
      <span className="text-xs text-muted-foreground">1 active incident</span>
    </div>
  </div>
</Link>`;
  return (
    <Section
      id="clickable-status-banner"
      title="Clickable status banner"
      description="The org dashboard's full-width status banner (OverallStatusBanner in dashboard-page.tsx): a red or amber alert that is ALSO the fastest path to the list it is complaining about. Link priority is incidents first (an active incident carries the ack/snooze/resolve workflow; a down check without one is just a state), then the checks list filtered to the relevant status. The all-clear green banner stays inert — there's nothing to jump to. Same hover treatment as KPI tiles (cursor-pointer plus a stronger border/background), applied to a banner instead of a tile."
    >
      <ExampleRow
        preview={
          <div className="w-full max-w-xl space-y-2">
            <Link
              to="/orgs/$org/incidents"
              params={{ org }}
              search={{
                state: "active" as const,
                showSuppressed: undefined,
                checkUid: undefined,
              }}
              data-testid="design-reference-clickable-banner-incidents"
              className="block"
            >
              <div className="relative overflow-hidden rounded-xl border border-destructive/30 bg-destructive/10 p-3.5 sm:p-4 shadow-sm cursor-pointer transition hover:border-destructive/50 hover:bg-destructive/15">
                <div className="flex items-center gap-3">
                  <AlertTriangle className="h-4 w-4 text-destructive" />
                  <h2 className="text-sm font-semibold text-destructive">
                    Issues detected
                  </h2>
                  <span className="text-xs text-muted-foreground">
                    1 active incident
                  </span>
                </div>
              </div>
            </Link>
            <Link
              to="/orgs/$org/checks"
              params={{ org }}
              search={{ status: "warning" }}
              data-testid="design-reference-clickable-banner-checks"
              className="block"
            >
              <div className="relative overflow-hidden rounded-xl border border-amber-500/30 bg-amber-500/10 p-3.5 sm:p-4 shadow-sm cursor-pointer transition hover:border-amber-500/50 hover:bg-amber-500/15">
                <div className="flex items-center gap-3">
                  <AlertTriangle className="h-4 w-4 text-amber-600 dark:text-amber-500" />
                  <h2 className="text-sm font-semibold text-amber-900 dark:text-amber-100">
                    Some checks degraded
                  </h2>
                  <span className="text-xs text-amber-800/80 dark:text-amber-300/80">
                    Timeouts detected on 3 checks
                  </span>
                </div>
              </div>
            </Link>
          </div>
        }
        importLine={snippet}
      />
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
    if (i === 22 || i === 23)
      availabilityPct = undefined; // most recent hours: no data yet
    else if (i === 10)
      availabilityPct = 0; // a full outage hour
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

// 24 hourly cells, oldest → newest, exercising every state the server can send:
// up, degraded, down, and no-data. Shaped exactly like the API's
// AvailabilityBucket so the section doubles as the response's documentation.
//
// Built once at module scope rather than per render: the reference page is a
// static catalog, and reading the clock during render is both impure and
// pointless here.
const AVAILABILITY_STRIP_SAMPLE = (() => {
  const now = Date.now();

  return Array.from({ length: 24 }, (_, i) => {
    const start = new Date(now - (23 - i) * 60 * 60 * 1000);
    const end = new Date(start.getTime() + 60 * 60 * 1000);
    let total = 60;
    let successful = 60;
    if (i === 22 || i === 23) total = 0; // most recent hours: not measured yet
    else if (i === 10) successful = 12; // a bad hour
    else if (i === 11) successful = 59; // one failed probe: degraded, not red
    const hasData = total > 0;
    return {
      periodStart: start.toISOString(),
      periodEnd: end.toISOString(),
      hasData,
      availabilityPct: hasData ? (successful / total) * 100 : null,
      totalChecks: hasData ? total : 0,
      successfulChecks: hasData ? successful : 0,
      status: !hasData
        ? ("noData" as const)
        : successful === total
          ? ("up" as const)
          : total - successful <= 1
            ? ("degraded" as const)
            : ("down" as const),
    };
  });
})();

function AvailabilityStripSection() {
  const cells = AVAILABILITY_STRIP_SAMPLE;

  const snippet = `import { AvailabilityStrip } from "@/components/ui/availability-strip";

// Presentational only. Feed it the cells from
// GET /api/v1/orgs/:org/checks/:check/availability/buckets?from=&to=&bucket=&region=
// (see useCheckAvailabilityBuckets) — the colour comes from the SERVER's
// \`status\`, so this strip and the public status page can never paint the same
// numbers differently. (The badge SVG keeps its own four-tier scale on purpose.)
<AvailabilityStrip
  cells={buckets.data}
  testIdPrefix="my-strip"
  height="sm" // "sm" under a chart, "md" as a standalone widget
/>`;

  return (
    <Section
      id="availability-strip"
      title="Availability strip"
      description="The colour-banded availability strip rendered under the check-detail response-time chart. One cell per bucket, oldest → newest, sharing the chart's window, zoom and region filter. Buckets are always whole-hour multiples (day → 1h, week → 6h, month → 1d); below a 3h window no strip is drawn at all and the chart header shows a single window figure instead. A cell with no probes is a distinct gray state — never a manufactured 100%."
    >
      <div className="rounded-md border bg-card p-4 space-y-4">
        <AvailabilityStrip cells={cells} height="md" />
        <p className="text-xs text-muted-foreground">
          Mostly green, one bad hour (red), one hour with a single failed probe
          (amber — the shared small-bucket guard never paints one failure red),
          and the two most recent hours awaiting data (gray).
        </p>
        <AvailabilityStrip cells={cells} height="sm" />
        <p className="text-xs text-muted-foreground">
          The same cells at <code>height="sm"</code>, the size used under the
          chart so the strip reads as an axis annotation rather than a second
          chart.
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
          <Swatch
            key={t.varName}
            varName={t.varName}
            label={t.name}
            description={t.description}
          />
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
        import {"{ CheckMultiPicker }"} from
        "@/components/shared/check-multi-picker"
      </p>
      <div className="grid gap-4 sm:grid-cols-2 max-w-2xl">
        <div className="space-y-2">
          <Label>Checks</Label>
          <CheckMultiPicker
            org={org}
            kind="checks"
            value={checkUids}
            onChange={setCheckUids}
          />
        </div>
        <div className="space-y-2">
          <Label>Check groups</Label>
          <CheckMultiPicker
            org={org}
            kind="groups"
            value={groupUids}
            onChange={setGroupUids}
          />
        </div>
      </div>
    </Section>
  );
}

function CheckGroupPickerSection() {
  const { org } = Route.useParams();
  const [groupUid, setGroupUid] = useState<string | undefined>();
  const [groupLabel, setGroupLabel] = useState<string | undefined>();

  return (
    <Section
      id="check-group-picker"
      title="Check group picker"
      description='Single-select for one check GROUP — the group twin of CheckPicker, with the same popover + search + arrow-key navigation shape. Each entry is labelled with its member count ("N checks"), which is operator-only context: the public status page never says a component aggregates several probes. Used by the status page editor to publish a group as one component.'
    >
      <p className="text-xs text-muted-foreground">
        import {"{ CheckGroupPicker }"} from
        "@/components/shared/check-group-picker"
      </p>
      <div className="grid gap-4 sm:grid-cols-2 max-w-2xl">
        <div className="space-y-2">
          <Label>Check group</Label>
          <CheckGroupPicker
            org={org}
            value={groupUid}
            selectedLabel={groupLabel}
            onChange={(uid, group) => {
              setGroupUid(uid);
              setGroupLabel(group ? group.name : undefined);
            }}
          />
        </div>
      </div>
    </Section>
  );
}

function TokenChipsInputSection() {
  const [valid, setValid] = useState<string[]>(["ops@example.com"]);
  const [withInvalid, setWithInvalid] = useState<string[]>([
    "oncall@example.com",
    "not-an-email",
  ]);
  const [statusCodes, setStatusCodes] = useState<string[]>(["200", "4XX"]);

  return (
    <Section
      id="token-chips-input"
      title="Token chips input"
      description="Generic chip/tag input for a free-form list of validated tokens — parameterized by validate, an optional normalize, placeholder, and data-testid. Each entry is a removable Badge chip, destructive-red when it fails validate. Typing a separator (space/comma/semicolon), pressing Enter, pasting a delimited list, or blurring all commit the current token(s) — normalize (if given) runs on commit and also sets the de-dupe key. Backspace on an empty field pops the last chip. RecipientsInput (email recipients, below) is a thin wrapper over this component; the HTTP check form's expected-status field is another."
    >
      <p className="text-xs text-muted-foreground">
        import {"{ TokenChipsInput }"} from
        "@/components/shared/token-chips-input"
      </p>
      <div className="grid gap-4 sm:grid-cols-2 max-w-2xl">
        <div className="space-y-2">
          <Label>Email recipients — all valid</Label>
          <p className="text-xs text-muted-foreground">
            import {"{ RecipientsInput }"} from
            "@/components/shared/recipients-input"
          </p>
          <RecipientsInput
            value={valid}
            onChange={setValid}
            placeholder="ops@example.com"
          />
        </div>
        <div className="space-y-2">
          <Label>Email recipients — with an invalid entry</Label>
          <RecipientsInput
            value={withInvalid}
            onChange={setWithInvalid}
            placeholder="ops@example.com"
          />
        </div>
        <div className="space-y-2">
          <Label>HTTP expected-status codes (exact or NXX wildcard)</Label>
          <TokenChipsInput
            value={statusCodes}
            onChange={setStatusCodes}
            validate={isValidStatusPattern}
            normalize={normalizeStatusPattern}
            placeholder="200"
          />
        </div>
      </div>
    </Section>
  );
}

function JsonAssertionEditorSection() {
  const [empty, setEmpty] = useState<AssertionNode | null>(null);
  const [single, setSingle] = useState<AssertionNode | null>({
    type: "assertion",
    path: "$.status",
    operator: "eq",
    value: "ok",
  });
  const [group, setGroup] = useState<AssertionNode | null>({
    type: "and",
    children: [
      { type: "assertion", path: "$.status", operator: "eq", value: "ok" },
      { type: "assertion", path: "$.uptime", operator: "gt", value: "0" },
    ],
  });

  return (
    <Section
      id="json-assertion-editor"
      title="JSON assertion editor"
      description="Recursive editor for the HTTP checker's JSONPath assertion AST — a leaf tests one JSONPath expression against an operator (eq/neq/gt/gte/lt/lte/contains/regex/exists/not_exists), and and/or group nodes nest arbitrarily. value is a required (string) argument, or an empty ready state before the first field is filled in; onChange(null) clears the whole tree. Used in the HTTP check form's Advanced section; JsonAssertionResults (not shown here) renders the matching evaluation result on a failed check."
    >
      <p className="text-xs text-muted-foreground">
        import {"{ JsonAssertionEditor }"} from
        "@/components/checks/json-assertion-editor"
      </p>
      <div className="grid gap-4 max-w-2xl">
        <div className="space-y-2">
          <Label>Empty — shows the add-assertion button</Label>
          <JsonAssertionEditor value={empty} onChange={setEmpty} />
        </div>
        <div className="space-y-2">
          <Label>Single leaf assertion</Label>
          <JsonAssertionEditor value={single} onChange={setSingle} />
        </div>
        <div className="space-y-2">
          <Label>AND group with two assertions</Label>
          <JsonAssertionEditor value={group} onChange={setGroup} />
        </div>
      </div>
    </Section>
  );
}

function JobsPrimitivesSection() {
  const [tab, setTab] = useState("first");
  const [segmented, setSegmented] = useState("first");

  return (
    <Section
      id="jobs-primitives"
      title="Jobs primitives"
      description="Building blocks for the admin Jobs observability page: compact stat tiles for the activity overview, in-page Tabs (dependency-free), a two-way segmented Button toggle, and a read-only JSON viewer for config/output blocks."
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
          import {"{ Tabs, TabsList, TabsTrigger, TabsContent }"} from
          "@/components/ui/tabs"
        </p>
        <Tabs value={tab} onValueChange={setTab}>
          <TabsList>
            <TabsTrigger value="first">First</TabsTrigger>
            <TabsTrigger value="second">Second</TabsTrigger>
          </TabsList>
          <TabsContent value="first">
            <div className="rounded-md border bg-card p-4 text-sm">
              First panel
            </div>
          </TabsContent>
          <TabsContent value="second">
            <div className="rounded-md border bg-card p-4 text-sm">
              Second panel
            </div>
          </TabsContent>
        </Tabs>
      </div>

      <div className="space-y-2">
        <h3 className="text-sm font-medium">
          Segmented toggle (two-way view switch)
        </h3>
        <p className="text-xs text-muted-foreground">
          import {"{ SegmentedControl }"} from
          "@/components/ui/segmented-control"
        </p>
        <p className="text-xs text-muted-foreground">
          The pattern used for the jobs page's org-scope toggle and the checks
          index's &ldquo;Group by&rdquo; view switch. Always render it through
          the <code>SegmentedControl</code> primitive rather than hand-rolling
          Buttons — the elevation is easy to get backwards.
        </p>
        <p className="text-xs text-muted-foreground">
          <strong>Selected = raised pill on a recessed track.</strong> The track
          is <code>bg-muted</code> and the selected segment is a{" "}
          <code>ghost</code> Button with{" "}
          <code>bg-card shadow-sm hover:bg-card</code>; unselected segments stay
          plain <code>ghost</code>. Never make the selected segment the darker
          one (the old <code>variant=&quot;secondary&quot;</code> pattern did
          exactly that, and read as recessed).
        </p>
        <p className="text-xs text-muted-foreground">
          <strong>The track flips token in dark:</strong>{" "}
          <code>dark:bg-background</code>. Dark <code>--muted</code> is{" "}
          <code>0.22</code>, <em>lighter</em> than dark <code>--card</code> at{" "}
          <code>0.18</code>, so reusing <code>bg-muted</code> in dark would put
          the pill below its own track and re-invert the control. The invariant
          to hold in both themes is <em>pill lighter than its track</em>.
        </p>
        <p className="text-xs text-muted-foreground">
          When the toggle is a page's primary navigation (like a view mode),
          drive it from a URL search param and push a history entry on change
          (see tech note on frontend URL state); use <code>replace: true</code>{" "}
          instead for incidental refinements. Each option carries an optional{" "}
          <code>testId</code> (rendered as <code>data-testid</code>) and{" "}
          <code>tooltip</code>; the primitive always emits{" "}
          <code>aria-pressed</code>.
        </p>
        <SegmentedControl
          value={segmented}
          onValueChange={setSegmented}
          aria-label="Group by"
          options={[
            { value: "first", label: "Groups" },
            {
              value: "second",
              label: "Host",
              tooltip: "Bucket checks by the host they target",
            },
          ]}
        />
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
      Date.UTC(
        now.getUTCFullYear(),
        now.getUTCMonth(),
        now.getUTCDate(),
        22,
        0,
      ),
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
          {'<Input type="number">'} paired with a unit {"<Select>"}. Also used
          inline in <code>check-form.tsx</code>'s Scheduling card for the check
          period (minutes/hours/days/weeks) and the optional "Region Spread"
          override (seconds/minutes/hours — spread needs finer granularity than
          a whole-minute period).
        </p>
        <div className="flex flex-wrap gap-2 max-w-xs">
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
              <SelectItem value="seconds">seconds</SelectItem>
              <SelectItem value="minutes">minutes</SelectItem>
              <SelectItem value="hours">hours</SelectItem>
            </SelectContent>
          </Select>
        </div>
      </div>

      <div className="space-y-2">
        <h3 className="text-sm font-medium">Schedule summary panel</h3>
        <p className="text-xs text-muted-foreground">
          import {"{ MaintenanceScheduleSummary }"} from
          "@/components/shared/maintenance-schedule-summary"
        </p>
        <MaintenanceScheduleSummary window={sampleWindow} />
      </div>
    </Section>
  );
}

function StatsStripSection() {
  const snippet = `// Compact wrapping row of labeled min/avg/max/p95(+count) numbers, scoped
// to a selected facet (e.g. one region) over the page's current time window.
// Renders ONLY when a specific facet is selected — never for an "All" state,
// since a combined-facet number is usually meaningless. Values combined
// client-side from an already-fetched dataset (no dedicated stats endpoint)
// get a "~" prefix on any estimated (non-exact) figure — e.g. when the
// window mixes raw rows with aggregated rollups, avg/p95 become weighted
// combinations rather than exact values; min/max stay exact either way.
<div className="flex flex-wrap items-center gap-x-4 gap-y-1 rounded-md border bg-muted/30 px-3 py-2 text-sm">
  <span className="text-xs text-muted-foreground">Last week</span>
  <span><span className="text-muted-foreground">Min: </span><span className="font-medium">42ms</span></span>
  <span><span className="text-muted-foreground">Avg: </span><span className="font-medium">~108ms</span></span>
  <span><span className="text-muted-foreground">Max: </span><span className="font-medium">891ms</span></span>
  <span><span className="text-muted-foreground">P95: </span><span className="font-medium">~340ms</span></span>
  <span><span className="text-muted-foreground">Samples: </span><span className="font-medium">1,204</span></span>
</div>`;
  return (
    <Section
      id="stats-strip"
      title="Stats strip"
      description="A compact, mobile-friendly summary row for min/avg/max/p95(+count)-style numbers scoped to one selected facet — used by the check-detail Recent Results card once a region is selected. Only render it for a specific selection, never for an unfiltered/'All' state. Prefix any client-combined (non-exact) figure with a plain '~' so it reads as an estimate, not a precise measurement."
    >
      <ExampleRow
        preview={
          <div className="flex flex-wrap items-center gap-x-4 gap-y-1 rounded-md border bg-muted/30 px-3 py-2 text-sm">
            <span className="text-xs text-muted-foreground">Last week</span>
            <span>
              <span className="text-muted-foreground">Min: </span>
              <span className="font-medium">42ms</span>
            </span>
            <span>
              <span className="text-muted-foreground">Avg: </span>
              <span className="font-medium">~108ms</span>
            </span>
            <span>
              <span className="text-muted-foreground">Max: </span>
              <span className="font-medium">891ms</span>
            </span>
            <span>
              <span className="text-muted-foreground">P95: </span>
              <span className="font-medium">~340ms</span>
            </span>
            <span>
              <span className="text-muted-foreground">Samples: </span>
              <span className="font-medium">1,204</span>
            </span>
          </div>
        }
        importLine={snippet}
      />
      <p className="text-xs text-muted-foreground">
        Resize your viewport — the row wraps to multiple lines rather than
        overflowing or shrinking its text.
      </p>
    </Section>
  );
}

function SwatchLegendChipsSection() {
  const regions = [
    { slug: "eu-west", label: "🇪🇺 EU West", color: "var(--chart-1)" },
    { slug: "us-east", label: "🇺🇸 US East", color: "var(--chart-2)" },
    { slug: "ap-south", label: "🇸🇬 AP South", color: "var(--chart-3)" },
  ];
  const [selected, setSelected] = useState<string | null>(null);
  const snippet = `// Segmented Button filter chips that double as a multi-series chart legend:
// each chip gets a small leading color swatch matching its line's stroke
// color. The "All" chip (no single facet selected) gets NO swatch — it
// doesn't correspond to any one line. Only add swatches when there truly
// are multiple simultaneously-rendered series to key against; a single
// selected facet (or a single-series chart) goes back to plain chips.
<Button variant={selected === null ? "default" : "outline"} size="sm" onClick={() => setSelected(null)}>
  All regions
</Button>
{regions.map((r) => (
  <Button key={r.slug} variant={selected === r.slug ? "default" : "outline"} size="sm" className="gap-1.5" onClick={() => setSelected(r.slug)}>
    <span className="inline-block h-2.5 w-2.5 shrink-0 rounded-sm" style={{ backgroundColor: r.color }} aria-hidden="true" />
    {r.label}
  </Button>
))}`;
  return (
    <Section
      id="swatch-legend-chips"
      title="Swatch-legend chips"
      description="Region/facet filter chips (the segmented-Button pattern) that double as a legend when a chart renders more than one simultaneous colored series — each chip gains a small leading color swatch matching that series' stroke color. Used by the check-detail response-time chart's multi-region 'All regions' view. The 'All' chip itself never gets a swatch (it isn't one line); selecting a single facet goes back to plain chips with no swatch, since there's only one line left to describe."
    >
      <ExampleRow
        preview={
          <div className="flex flex-wrap items-center gap-1" role="group">
            <Button
              variant={selected === null ? "default" : "outline"}
              size="sm"
              className="px-2 text-xs"
              onClick={() => setSelected(null)}
            >
              All regions
            </Button>
            {regions.map((r) => (
              <Button
                key={r.slug}
                variant={selected === r.slug ? "default" : "outline"}
                size="sm"
                className="gap-1.5 px-2 text-xs"
                onClick={() => setSelected(r.slug)}
              >
                <span
                  className="inline-block h-2.5 w-2.5 shrink-0 rounded-sm"
                  style={{ backgroundColor: r.color }}
                  aria-hidden="true"
                />
                {r.label}
              </Button>
            ))}
          </div>
        }
        importLine={snippet}
      />
    </Section>
  );
}

function PagingCoverageSection() {
  const snippet = `import {
  PagingCoverageCell,
  EmailOnlyBadge,
  useEmailOnlyUserUids,
} from "@/components/notifications/member-coverage";

// In a member table row:
<PagingCoverageCell coverage={coverageByUser.get(member.userUid)} />

// Next to any rostered member / \`user\` escalation target:
const emailOnly = useEmailOnlyUserUids(org);
{emailOnly.has(member.userUid) && <EmailOnlyBadge />}`;

  return (
    <Section
      id="paging-coverage"
      title="Paging coverage"
      description="How reachable a member actually is. Solid icon = enabled AND verified (it can page); dashed/muted = unverified or disabled (it cannot). When email is all that remains, the warning badge says so out loud — a row of quiet icons must never read as 'covered'. Data comes from the admin-only coverage endpoint, which exposes channel types and flags but never a contact value."
    >
      <div className="space-y-4 rounded-md border bg-card p-4">
        <div className="flex flex-wrap items-center gap-6">
          <div className="space-y-1">
            <p className="text-xs text-muted-foreground">Well covered</p>
            <PagingCoverageCell
              coverage={{
                userUid: "u1",
                email: "zoe@example.com",
                role: "admin",
                emailFallbackOnly: false,
                channels: [
                  { type: "email", verified: true, enabled: true },
                  { type: "phone", verified: true, enabled: true },
                  { type: "telegram", verified: true, enabled: true },
                ],
              }}
            />
          </div>
          <div className="space-y-1">
            <p className="text-xs text-muted-foreground">Unverified phone</p>
            <PagingCoverageCell
              coverage={{
                userUid: "u2",
                email: "bob@example.com",
                role: "user",
                emailFallbackOnly: true,
                channels: [
                  { type: "email", verified: true, enabled: true },
                  { type: "phone", verified: false, enabled: true },
                ],
              }}
            />
          </div>
          <div className="space-y-1">
            <p className="text-xs text-muted-foreground">Badge on its own</p>
            <EmailOnlyBadge />
          </div>
        </div>
      </div>
      <CodeSnippet code={snippet} />
    </Section>
  );
}

function OnboardingChecklistSection() {
  const { org } = Route.useParams();
  const snippet = `import { OnboardingChecklist } from "@/components/dashboard/onboarding-checklist";

// Container: derives every step from real resources and reads/writes the
// per-user dismissal (\`onboarding.<org>\` ui-state) itself.
<OnboardingChecklist org={org} totalChecks={stats.total} firstCheckUid={checks[0]?.uid} />

// Presentation only, for a surface that already has the data:
import { OnboardingChecklistCard } from "@/components/dashboard/onboarding-checklist";
<OnboardingChecklistCard org={org} steps={steps} allSet={false} onDismiss={hide} />`;

  return (
    <Section
      id="onboarding-checklist"
      title="Onboarding checklist"
      description="The dashboard's getting-started card. Every row's tick is DERIVED from a real resource — never a stored per-step flag — so it stays honest for a user who joins an already-configured org. The only persisted bit is the dismissal, held server-side per user per org so hiding it here hides it on every device; the account profile page brings it back. Reuse this pattern for any 'guide the user through setup' surface: derived state, one dismissal, an explicit way back."
    >
      <ExampleRow
        preview={
          <div className="max-w-2xl">
            <OnboardingChecklistCard
              org={org}
              steps={[
                { id: "check", done: true },
                { id: "alerts", done: true },
                { id: "report", done: false },
                { id: "statusPage", done: false },
                { id: "team", done: false },
              ]}
              allSet={false}
              onDismiss={() => {}}
            />
          </div>
        }
        importLine={'import { OnboardingChecklistCard } from "@/components/dashboard/onboarding-checklist";'}
      />
      <CodeSnippet code={snippet} />
    </Section>
  );
}

function MagicWandSection() {
  const snippet = `import { Wand2 } from "lucide-react";

// One-click sensible default, next to the page's primary "New …" action.
// Visible only while the derivation helper says the step is unsatisfied —
// it disappears the instant the resource it offers to create exists.
<Button
  variant="outline"
  onClick={onWandClick}
  disabled={createMutation.isPending}
  data-testid="wand-create-email-alerts"
  aria-label={t("wand.createEmailAlerts")}
>
  {createMutation.isPending ? (
    <Loader2 className="h-4 w-4 animate-spin sm:mr-2" />
  ) : (
    <Wand2 className="h-4 w-4 sm:mr-2" />
  )}
  <span className="hidden sm:inline">{t("wand.createEmailAlerts")}</span>
</Button>`;

  return (
    <Section
      id="magic-wand"
      title="Magic wand"
      description="One-click sensible default for a Getting Started step (spec 2026-08-29-03). Two flavors: DIRECT CREATE for a private, reversible resource whose default is exactly what the backend already seeds (alerts, report) — one click and the resource exists; PREFILL ONLY for a public-facing artifact with a slug (the status page) — the wand fills the form and the operator still clicks Create. Always outline variant with the Wand2 icon, icon-only on mobile. Visibility is derived the same way the checklist itself derives step completion — never a stored flag — so the wand vanishes the moment its step is satisfied, from any source (wand click, manual creation, or an org that was seeded from the start)."
    >
      <div className="flex flex-wrap items-center gap-3">
        <div className="space-y-1">
          <p className="text-xs text-muted-foreground">
            Direct create (enabled, ready to click)
          </p>
          <Button variant="outline" data-testid="wand-create-email-alerts">
            <Wand2 className="h-4 w-4 sm:mr-2" />
            <span className="hidden sm:inline">Set up email alerts for me</span>
          </Button>
        </div>
        <div className="space-y-1">
          <p className="text-xs text-muted-foreground">Pending (mutation in flight)</p>
          <Button variant="outline" disabled>
            <Loader2 className="h-4 w-4 animate-spin sm:mr-2" />
            <span className="hidden sm:inline">Set up email alerts for me</span>
          </Button>
        </div>
        <div className="space-y-1">
          <p className="text-xs text-muted-foreground">
            Prefill only (status page — form still needs Create)
          </p>
          <Button variant="outline" data-testid="wand-prefill-status-page">
            <Wand2 className="h-4 w-4 sm:mr-2" />
            <span className="hidden sm:inline">Prefill for me</span>
          </Button>
        </div>
      </div>
      <CodeSnippet code={snippet} />
    </Section>
  );
}
