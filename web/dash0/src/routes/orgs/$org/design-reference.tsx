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
  Copy,
  Eye,
  Info,
  Moon,
  MoreVertical,
  Palette,
  Pencil,
  RotateCw,
  Save,
  Search,
  Sun,
  Trash2,
} from "lucide-react";
import { toast } from "sonner";

import { LabelFilter } from "@/components/shared/label-filter";
import { PageHeader } from "@/components/shared/page-header";
import { StatusBadge } from "@/components/shared/status-badge";
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
  { id: "buttons-badges", label: "Buttons & badges" },
  { id: "forms", label: "Forms" },
  { id: "data-display", label: "Data display" },
  { id: "feedback", label: "Feedback" },
  { id: "label-filter", label: "Label filter" },
  { id: "kpi-tiles", label: "KPI tiles" },
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
      <ButtonsBadgesSection />
      <FormsSection />
      <DataDisplaySection />
      <FeedbackSection />
      <LabelFilterSection />
      <KpiTileSection />
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
  return (
    <Section
      id="page-header"
      title="Page header"
      description="The icon-on-left + title + optional description + right-aligned actions pattern. Use it for new pages; existing inline headers will adopt it over time."
    >
      <div className="rounded-md border bg-card p-4">
        <PageHeader
          icon={Palette}
          title="Example page title"
          description="A short subtitle that explains what the page is for."
          actions={<Button size="sm">Primary action</Button>}
        />
      </div>
      <CodeSnippet code={`import { PageHeader } from "@/components/shared/page-header";`} />
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
      description="All variants and sizes shipped today. Pick the one with the least visual weight that still does the job. Action buttons should pair an icon with a short verb, collapse to icon-only on mobile, and the top-right back button is always icon-only."
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

        <h3 className="text-sm font-medium">Detail page header: back button + title + actions</h3>
        <p className="text-sm text-muted-foreground">
          On detail/edit pages, compose a single flex row:{" "}
          <strong>icon-only back button on the far left</strong>, the page{" "}
          <code className="mx-1 rounded bg-muted px-1 py-0.5 text-xs">h1</code> with{" "}
          <code className="mx-1 rounded bg-muted px-1 py-0.5 text-xs">flex-1</code> in the middle,
          and action buttons (e.g. Delete) on the far right. The back button is{" "}
          <strong>always icon-only</strong> — never paired with a &quot;Back&quot; label. Use{" "}
          <code className="mx-1 rounded bg-muted px-1 py-0.5 text-xs">ArrowLeft</code> with{" "}
          <code className="mx-1 rounded bg-muted px-1 py-0.5 text-xs">size=&quot;icon&quot;</code>{" "}
          and an <code className="mx-1 rounded bg-muted px-1 py-0.5 text-xs">aria-label</code>.
        </p>
        <ExampleRow
          preview={
            <div className="flex w-full items-center gap-3">
              <Button variant="ghost" size="icon" aria-label="Back">
                <ArrowLeft />
              </Button>
              <h1 className="flex-1 text-xl font-bold tracking-tight">Page title</h1>
              <Button variant="destructive" size="sm" aria-label="Delete">
                <Trash2 />
                <span className="hidden sm:inline">Delete</span>
              </Button>
            </div>
          }
          importLine={`<div className="flex items-center gap-3">\n  <Button variant="ghost" size="icon" aria-label="Back">\n    <ArrowLeft />\n  </Button>\n  <h1 className="flex-1 text-3xl font-bold tracking-tight">{title}</h1>\n  <Button variant="destructive" size="sm" onClick={handleDelete}>\n    <Trash2 />\n    Delete\n  </Button>\n</div>`}
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

type MockRow = { id: string; name: string; status: "up" | "down" | "warning"; latency: string };

const MOCK_ROWS: MockRow[] = [
  { id: "1", name: "api.example.com", status: "up", latency: "120 ms" },
  { id: "2", name: "checkout-prod", status: "up", latency: "85 ms" },
  { id: "3", name: "billing-staging", status: "warning", latency: "950 ms" },
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
  <Card className="transition-colors hover:bg-accent/40">
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
      description="Large-number summary cards used on the org dashboard. Link tiles 1–3 to drill-down list pages; leave purely metric tiles (e.g. % availability) static. Whole-card hover via transition-colors hover:bg-accent/40 — no nested interactive elements inside."
    >
      <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-3">
        <Link
          to="/orgs/$org/checks"
          params={{ org }}
          className="block"
        >
          <Card className="transition-colors hover:bg-accent/40">
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
