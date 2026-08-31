import { useState } from "react";
import { createFileRoute, Link, useNavigate } from "@tanstack/react-router";
import { useTranslation } from "react-i18next";
import { StatusPageTvCard } from "@/components/shared/status-page-tv-card";
import {
  AlertTriangle,
  ArrowLeft,
  Eye,
  ExternalLink,
  Palette,
  Pencil,
  Plus,
  Trash2,
  Star,
  GripVertical,
  ChevronUp,
  ChevronDown,
} from "lucide-react";
import { toast } from "sonner";
import {
  DndContext,
  PointerSensor,
  KeyboardSensor,
  closestCenter,
  useSensor,
  useSensors,
  type DragEndEvent,
} from "@dnd-kit/core";
import {
  SortableContext,
  arrayMove,
  sortableKeyboardCoordinates,
  useSortable,
  verticalListSortingStrategy,
} from "@dnd-kit/sortable";
import { CSS } from "@dnd-kit/utilities";
import {
  useStatusPage,
  useCreateSection,
  useDeleteSection,
  useUpdateSection,
  useCreateResource,
  useDeleteResource,
  useDeleteStatusPage,
  useReorderResources,
  useReorderSections,
  useUpdateResource,
  type StatusPageSection,
  type StatusPageResource,
} from "@/api/hooks";
import { Badge } from "@/components/ui/badge";
import { StatusBadge } from "@/components/shared/status-badge";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Skeleton } from "@/components/ui/skeleton";
import {
  Tooltip,
  TooltipContent,
  TooltipTrigger,
} from "@/components/ui/tooltip";
import { cn, slugify } from "@/lib/utils";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
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
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@/components/ui/alert-dialog";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { CheckPicker } from "@/components/shared/check-picker";
import { CheckGroupPicker } from "@/components/shared/check-group-picker";
import { QueryErrorView } from "@/components/shared/error-views";
import { Alert, AlertDescription } from "@/components/ui/alert";
import {
  SectionMembership,
  membershipFromSelector,
  membershipIsComplete,
  selectorFromMembership,
  type SectionMembershipValue,
} from "@/components/shared/section-membership";
import { SelectorClaimedElsewhereAlert } from "@/components/shared/selector-claimed-elsewhere-alert";
import { ApiError } from "@/api/client";

export const Route = createFileRoute("/orgs/$org/status-pages/$statusPageUid/")(
  {
    component: StatusPageDetailPage,
  },
);

function StatusDot({ status }: { status?: string }) {
  const color =
    status === "up"
      ? "bg-green-500"
      : status === "down"
        ? "bg-red-500"
        : status === "degraded"
          ? "bg-yellow-500"
          : "bg-muted-foreground";

  return <div className={`h-2.5 w-2.5 rounded-full ${color}`} />;
}

function EnabledDot({
  enabled,
  enabledLabel,
  disabledLabel,
}: {
  enabled: boolean;
  enabledLabel: string;
  disabledLabel: string;
}) {
  const label = enabled ? enabledLabel : disabledLabel;
  return (
    <Tooltip>
      <TooltipTrigger asChild>
        <span
          aria-label={label}
          className={cn(
            "inline-block h-2 w-2 rounded-full shrink-0",
            enabled ? "bg-green-500" : "bg-muted-foreground/40",
          )}
        />
      </TooltipTrigger>
      <TooltipContent>{label}</TooltipContent>
    </Tooltip>
  );
}

function VisibilityDot({
  isPublic,
  publicLabel,
  restrictedLabel,
}: {
  isPublic: boolean;
  publicLabel: string;
  restrictedLabel: string;
}) {
  const label = isPublic ? publicLabel : restrictedLabel;
  return (
    <Tooltip>
      <TooltipTrigger asChild>
        <span
          aria-label={label}
          className={cn(
            "inline-block h-2 w-2 rounded-full shrink-0",
            isPublic ? "bg-blue-500" : "bg-amber-500",
          )}
        />
      </TooltipTrigger>
      <TooltipContent>{label}</TooltipContent>
    </Tooltip>
  );
}

function AddSectionDialog({
  org,
  statusPageUid,
  visibility,
}: {
  org: string;
  statusPageUid: string;
  visibility?: string;
}) {
  const { t } = useTranslation(["statusPages", "common"]);
  const [open, setOpen] = useState(false);
  const [name, setName] = useState("");
  const [slug, setSlug] = useState("");
  const [slugManuallyEdited, setSlugManuallyEdited] = useState(false);
  // Manual is the only default a section may be created with: auto-inclusion
  // has to be an explicit act, never something a form arrives at by itself.
  const [membership, setMembership] = useState<SectionMembershipValue>({
    mode: "manual",
    labels: {},
  });
  const createSection = useCreateSection(org, statusPageUid);

  const handleSubmit = async () => {
    try {
      const selector = selectorFromMembership(membership);
      await createSection.mutateAsync({
        name,
        slug: slug || slugify(name),
        // Omitted rather than null: on CREATE there is nothing to clear.
        ...(selector ? { selector } : {}),
      });
      toast.success(t("statusPages:sections.created"));
      setName("");
      setSlug("");
      setSlugManuallyEdited(false);
      setMembership({ mode: "manual", labels: {} });
      setOpen(false);
    } catch (err) {
      toast.error(
        err instanceof ApiError
          ? err.message
          : t("statusPages:sections.createFailed"),
      );
    }
  };

  return (
    <Dialog open={open} onOpenChange={setOpen}>
      <Tooltip>
        <TooltipTrigger asChild>
          <DialogTrigger asChild>
            <Button
              variant="outline"
              size="icon"
              aria-label={t("statusPages:sections.add")}
            >
              <Plus className="h-4 w-4" />
            </Button>
          </DialogTrigger>
        </TooltipTrigger>
        <TooltipContent>{t("statusPages:sections.add")}</TooltipContent>
      </Tooltip>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{t("statusPages:sections.add")}</DialogTitle>
          <DialogDescription>
            {t("statusPages:sections.addDescription")}
          </DialogDescription>
        </DialogHeader>
        <div className="space-y-4 py-4">
          <div className="space-y-2">
            <Label>{t("statusPages:sections.name")}</Label>
            <Input
              value={name}
              onChange={(e) => {
                setName(e.target.value);
                if (!slugManuallyEdited) setSlug(slugify(e.target.value));
              }}
              placeholder={t("statusPages:sections.namePlaceholder")}
            />
          </div>
          <div className="space-y-2">
            <Label>{t("statusPages:sections.slug")}</Label>
            <Input
              value={slug}
              onChange={(e) => {
                setSlug(e.target.value);
                setSlugManuallyEdited(true);
              }}
              placeholder={t("statusPages:sections.slugPlaceholder")}
            />
          </div>
          <SectionMembership
            org={org}
            value={membership}
            onChange={setMembership}
            visibility={visibility}
          />
        </div>
        <DialogFooter>
          <Button variant="outline" onClick={() => setOpen(false)}>
            {t("common:cancel")}
          </Button>
          <Button
            onClick={handleSubmit}
            disabled={
              !name ||
              !membershipIsComplete(membership) ||
              createSection.isPending
            }
            data-testid="section-create-submit"
          >
            {t("statusPages:sections.create")}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

// EditSectionDialog renders inside SectionCard so the useUpdateSection hook
// is bound to a specific section UID; mounting/unmounting per-card keeps the
// hook signature `(org, statusPageUid, sectionUid)` working.
function EditSectionDialog({
  org,
  statusPageUid,
  section,
  visibility,
  open,
  onOpenChange,
}: {
  org: string;
  statusPageUid: string;
  section: StatusPageSection;
  visibility?: string;
  open: boolean;
  onOpenChange: (next: boolean) => void;
}) {
  const { t } = useTranslation(["statusPages", "common"]);
  const [name, setName] = useState(section.name);
  const [slug, setSlug] = useState(section.slug);
  const [membership, setMembership] = useState<SectionMembershipValue>(() =>
    membershipFromSelector(section.selector),
  );
  const updateSection = useUpdateSection(org, statusPageUid, section.uid);

  const handleSubmit = async () => {
    try {
      // `selector: null` is load-bearing on update — it is what turns a
      // dynamic section back into a hand-curated one (and removes the
      // components the rule owned). Omitting the key would leave it in place.
      await updateSection.mutateAsync({
        name,
        slug: slug || slugify(name),
        selector: selectorFromMembership(membership),
      });
      toast.success(t("statusPages:sections.updated"));
      onOpenChange(false);
    } catch (err) {
      toast.error(
        err instanceof ApiError
          ? err.message
          : t("statusPages:sections.updateFailed"),
      );
    }
  };

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{t("statusPages:sections.editTitle")}</DialogTitle>
          <DialogDescription>
            {t("statusPages:sections.editDescription")}
          </DialogDescription>
        </DialogHeader>
        <div className="space-y-4 py-4">
          <div className="space-y-2">
            <Label>{t("statusPages:sections.name")}</Label>
            <Input
              value={name}
              onChange={(e) => setName(e.target.value)}
              placeholder={t("statusPages:sections.namePlaceholder")}
            />
          </div>
          <div className="space-y-2">
            <Label>{t("statusPages:sections.slug")}</Label>
            <Input
              value={slug}
              onChange={(e) => setSlug(e.target.value)}
              placeholder={t("statusPages:sections.slugPlaceholder")}
            />
          </div>
          <SectionMembership
            org={org}
            value={membership}
            onChange={setMembership}
            visibility={visibility}
          />
        </div>
        <DialogFooter>
          <Button variant="outline" onClick={() => onOpenChange(false)}>
            {t("common:cancel")}
          </Button>
          <Button
            onClick={handleSubmit}
            disabled={
              !name ||
              !membershipIsComplete(membership) ||
              updateSection.isPending
            }
            data-testid="section-edit-submit"
          >
            {t("statusPages:sections.save")}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

/**
 * A status page resource targets EITHER one check OR one check group; the two
 * are mutually exclusive end to end (API validation and a database XOR check
 * constraint). The dialog therefore picks a KIND first and only ever submits
 * the field for that kind.
 */
type ResourceTargetKind = "check" | "group";

/**
 * Kind tabs + the matching single-select picker. Shared by the add and the
 * "change target" dialogs so both speak the same XOR the API enforces: a kind
 * is chosen first, and only that kind's field is ever submitted.
 */
function ResourceTargetPicker({
  org,
  kind,
  onKindChange,
  selectedUid,
  selectedLabel,
  onSelect,
  excludeCheckUids,
  excludeGroupUids,
}: {
  org: string;
  kind: ResourceTargetKind;
  onKindChange: (kind: ResourceTargetKind) => void;
  selectedUid?: string;
  selectedLabel?: string;
  onSelect: (uid: string | undefined, label?: string) => void;
  excludeCheckUids?: Set<string>;
  excludeGroupUids?: Set<string>;
}) {
  const { t } = useTranslation(["statusPages", "common"]);

  return (
    <Tabs
      value={kind}
      onValueChange={(next) =>
        onKindChange(next === "group" ? "group" : "check")
      }
      className="py-2"
    >
      <TabsList className="w-full">
        <TabsTrigger
          value="check"
          className="flex-1"
          data-testid="resource-kind-check"
        >
          {t("statusPages:resources.kindCheck")}
        </TabsTrigger>
        <TabsTrigger
          value="group"
          className="flex-1"
          data-testid="resource-kind-group"
        >
          {t("statusPages:resources.kindGroup")}
        </TabsTrigger>
      </TabsList>
      <TabsContent value="check" className="pt-4">
        <CheckPicker
          org={org}
          value={kind === "check" ? selectedUid : undefined}
          selectedLabel={kind === "check" ? selectedLabel : undefined}
          excludeUids={excludeCheckUids}
          placeholder={t("statusPages:resources.selectCheck")}
          triggerTestId="resource-check-select"
          onChange={(uid, c) =>
            onSelect(uid, c ? c.name || c.slug || undefined : undefined)
          }
        />
      </TabsContent>
      <TabsContent value="group" className="pt-4 space-y-2">
        <CheckGroupPicker
          org={org}
          value={kind === "group" ? selectedUid : undefined}
          selectedLabel={kind === "group" ? selectedLabel : undefined}
          excludeUids={excludeGroupUids}
          placeholder={t("statusPages:resources.selectGroup")}
          triggerTestId="resource-group-select"
          onChange={(uid, g) => onSelect(uid, g ? g.name : undefined)}
        />
        <p className="text-xs text-muted-foreground">
          {t("statusPages:resources.groupHelp")}
        </p>
      </TabsContent>
    </Tabs>
  );
}

/**
 * "Change target" for an existing resource. This is a single-field change (the
 * resource's target), which the dash0 route convention explicitly leaves inline,
 * and it matches how this page already edits its other sub-entities
 * (EditSectionDialog) — a dedicated route for one picker would diverge from the
 * page's own pattern.
 */
function EditResourceTargetDialog({
  org,
  statusPageUid,
  sectionUid,
  resource,
  existingCheckUids,
  existingGroupUids,
}: {
  org: string;
  statusPageUid: string;
  sectionUid: string;
  resource: StatusPageResource;
  existingCheckUids: Set<string>;
  existingGroupUids: Set<string>;
}) {
  const { t } = useTranslation(["statusPages", "common"]);
  const [open, setOpen] = useState(false);
  const initialKind: ResourceTargetKind = resource.checkGroupUid
    ? "group"
    : "check";
  const [kind, setKind] = useState<ResourceTargetKind>(initialKind);
  const [selectedUid, setSelectedUid] = useState<string | undefined>(
    resource.checkGroupUid ?? resource.checkUid,
  );
  const [selectedLabel, setSelectedLabel] = useState<string | undefined>(
    resource.check?.name,
  );
  const updateResource = useUpdateResource(org, statusPageUid, sectionUid);

  const reset = () => {
    setKind(initialKind);
    setSelectedUid(resource.checkGroupUid ?? resource.checkUid);
    setSelectedLabel(resource.check?.name);
  };

  const handleSubmit = async () => {
    if (!selectedUid) return;
    try {
      await updateResource.mutateAsync({
        resourceUid: resource.uid,
        request:
          kind === "group"
            ? { checkGroupUid: selectedUid }
            : { checkUid: selectedUid },
      });
      toast.success(t("statusPages:resources.targetUpdated"));
      setOpen(false);
    } catch (err) {
      toast.error(
        err instanceof ApiError
          ? err.message
          : t("statusPages:resources.targetUpdateFailed"),
      );
    }
  };

  // The resource's own current target must stay selectable, so it is removed
  // from the exclusion sets it would otherwise be caught by.
  const excludeCheckUids = new Set(existingCheckUids);
  if (resource.checkUid) excludeCheckUids.delete(resource.checkUid);
  const excludeGroupUids = new Set(existingGroupUids);
  if (resource.checkGroupUid) excludeGroupUids.delete(resource.checkGroupUid);

  return (
    <Dialog
      open={open}
      onOpenChange={(next) => {
        setOpen(next);
        if (!next) reset();
      }}
    >
      <DialogTrigger asChild>
        <Button
          variant="ghost"
          size="icon"
          className="h-7 w-7"
          aria-label={t("statusPages:resources.changeTarget")}
          data-testid="resource-edit-target"
        >
          <Pencil className="h-3 w-3" />
        </Button>
      </DialogTrigger>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{t("statusPages:resources.changeTarget")}</DialogTitle>
          <DialogDescription>
            {t("statusPages:resources.changeTargetDescription")}
          </DialogDescription>
        </DialogHeader>
        <ResourceTargetPicker
          org={org}
          kind={kind}
          onKindChange={(next) => {
            setKind(next);
            setSelectedUid(undefined);
            setSelectedLabel(undefined);
          }}
          selectedUid={selectedUid}
          selectedLabel={selectedLabel}
          onSelect={(uid, label) => {
            setSelectedUid(uid);
            setSelectedLabel(label);
          }}
          excludeCheckUids={excludeCheckUids}
          excludeGroupUids={excludeGroupUids}
        />
        <DialogFooter>
          <Button variant="outline" onClick={() => setOpen(false)}>
            {t("common:cancel")}
          </Button>
          <Button
            onClick={handleSubmit}
            disabled={!selectedUid || updateResource.isPending}
            data-testid="resource-edit-submit"
          >
            {t("statusPages:resources.save")}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

function AddResourceDialog({
  org,
  statusPageUid,
  sectionUid,
  existingCheckUids,
  existingGroupUids,
}: {
  org: string;
  statusPageUid: string;
  sectionUid: string;
  existingCheckUids: Set<string>;
  existingGroupUids: Set<string>;
}) {
  const { t } = useTranslation(["statusPages", "common"]);
  const [open, setOpen] = useState(false);
  const [kind, setKind] = useState<ResourceTargetKind>("check");
  const [selectedUid, setSelectedUid] = useState<string | undefined>();
  const [selectedLabel, setSelectedLabel] = useState<string | undefined>();
  const createResource = useCreateResource(org, statusPageUid, sectionUid);

  const reset = () => {
    setKind("check");
    setSelectedUid(undefined);
    setSelectedLabel(undefined);
  };

  // Switching kind clears the selection: a check uid is never a valid group uid.
  const switchKind = (next: ResourceTargetKind) => {
    setKind(next);
    setSelectedUid(undefined);
    setSelectedLabel(undefined);
  };

  const handleSubmit = async () => {
    if (!selectedUid) return;
    try {
      await createResource.mutateAsync(
        kind === "group"
          ? { checkGroupUid: selectedUid }
          : { checkUid: selectedUid },
      );
      toast.success(t("statusPages:resources.added"));
      reset();
      setOpen(false);
    } catch (err) {
      toast.error(
        err instanceof ApiError
          ? err.message
          : t("statusPages:resources.addFailed"),
      );
    }
  };

  return (
    <Dialog
      open={open}
      onOpenChange={(next) => {
        setOpen(next);
        if (!next) reset();
      }}
    >
      <Tooltip>
        <TooltipTrigger asChild>
          <DialogTrigger asChild>
            <Button
              variant="ghost"
              size="icon"
              aria-label={t("statusPages:resources.add")}
            >
              <Plus className="h-4 w-4" />
            </Button>
          </DialogTrigger>
        </TooltipTrigger>
        <TooltipContent>{t("statusPages:resources.add")}</TooltipContent>
      </Tooltip>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{t("statusPages:resources.addToSection")}</DialogTitle>
          <DialogDescription>
            {t("statusPages:resources.addDescription")}
          </DialogDescription>
        </DialogHeader>
        <ResourceTargetPicker
          org={org}
          kind={kind}
          onKindChange={switchKind}
          selectedUid={selectedUid}
          selectedLabel={selectedLabel}
          onSelect={(uid, label) => {
            setSelectedUid(uid);
            setSelectedLabel(label);
          }}
          excludeCheckUids={existingCheckUids}
          excludeGroupUids={existingGroupUids}
        />
        <DialogFooter>
          <Button variant="outline" onClick={() => setOpen(false)}>
            {t("common:cancel")}
          </Button>
          <Button
            onClick={handleSubmit}
            disabled={!selectedUid || createResource.isPending}
            data-testid="resource-add-submit"
          >
            {t("statusPages:resources.add")}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

function ResourceRow({
  resource,
  index,
  total,
  neighbors,
  org,
  statusPageUid,
  sectionUid,
  existingCheckUids,
  existingGroupUids,
}: {
  resource: StatusPageResource;
  index: number;
  total: number;
  neighbors: StatusPageResource[];
  org: string;
  statusPageUid: string;
  sectionUid: string;
  existingCheckUids: Set<string>;
  existingGroupUids: Set<string>;
}) {
  const { t } = useTranslation(["statusPages", "common"]);
  const [deleteOpen, setDeleteOpen] = useState(false);
  const deleteResource = useDeleteResource(org, statusPageUid, sectionUid);
  const updateResource = useUpdateResource(org, statusPageUid, sectionUid);
  const managed = resource.managedBySelector === true;

  const handleDelete = async () => {
    try {
      await deleteResource.mutateAsync(resource.uid);
      toast.success(t("statusPages:resources.removed"));
    } catch (err) {
      toast.error(
        err instanceof ApiError
          ? err.message
          : t("statusPages:resources.removeFailed"),
      );
    }
  };

  // Swap positions with the adjacent neighbor. Two PATCHes keep the
  // backend's existing semantics intact (no renumbering helper needed).
  const move = async (delta: -1 | 1) => {
    const target = index + delta;
    if (target < 0 || target >= total) return;
    const neighbor = neighbors[target];
    if (!neighbor) return;
    try {
      await Promise.all([
        updateResource.mutateAsync({
          resourceUid: resource.uid,
          request: { position: neighbor.position },
        }),
        updateResource.mutateAsync({
          resourceUid: neighbor.uid,
          request: { position: resource.position },
        }),
      ]);
    } catch (err) {
      toast.error(err instanceof ApiError ? err.message : "Failed to reorder");
    }
  };

  const {
    attributes,
    listeners,
    setNodeRef,
    transform,
    transition,
    isDragging,
  } = useSortable({ id: resource.uid });
  const dragStyle = {
    transform: CSS.Transform.toString(transform),
    transition,
  };

  return (
    <div
      ref={setNodeRef}
      style={dragStyle}
      className={cn(
        "flex items-center gap-3 py-2 px-3 rounded-md hover:bg-muted/50",
        isDragging && "opacity-60 shadow-md bg-background relative z-10",
      )}
    >
      {/*
        A selector-owned row has no reorder or delete affordance: the section's
        membership rule decides both, and any change made here would be undone
        by the next reconcile. Offering a control that silently reverts is
        worse than not offering it. The spacer keeps the rows aligned.
      */}
      {managed ? (
        <div className="w-4" aria-hidden="true" />
      ) : (
        <button
          type="button"
          className="touch-none cursor-grab active:cursor-grabbing text-muted-foreground/50 hover:text-muted-foreground focus:outline-none focus-visible:ring-2 focus-visible:ring-ring rounded"
          aria-label="Drag to reorder"
          {...attributes}
          {...listeners}
        >
          <GripVertical className="h-4 w-4" />
        </button>
      )}
      {!managed && (
        <div className="flex flex-col -space-y-1">
          <Button
            variant="ghost"
            size="icon"
            className="h-4 w-4 p-0"
            disabled={index === 0 || updateResource.isPending}
            onClick={() => move(-1)}
            aria-label="Move up"
          >
            <ChevronUp className="h-3 w-3" />
          </Button>
          <Button
            variant="ghost"
            size="icon"
            className="h-4 w-4 p-0"
            disabled={index === total - 1 || updateResource.isPending}
            onClick={() => move(1)}
            aria-label="Move down"
          >
            <ChevronDown className="h-3 w-3" />
          </Button>
        </div>
      )}
      <StatusDot status={resource.check?.status} />
      <span className="flex-1 text-sm" data-testid="resource-row-name">
        {resource.publicName ||
          resource.check?.name ||
          (resource.checkUid ?? resource.checkGroupUid ?? "").slice(0, 8)}
      </span>
      {/*
        A group resource carries no check type — the public payload deliberately
        omits it — so the row labels the KIND instead. That is dashboard-only
        context; the public page never says a component is aggregated.
      */}
      {resource.checkGroupUid ? (
        <Badge
          variant="outline"
          className="text-xs"
          data-testid="resource-row-group-badge"
        >
          {t("statusPages:resources.kindGroup")}
        </Badge>
      ) : (
        resource.check?.type && (
          <Badge variant="outline" className="text-xs">
            {resource.check.type}
          </Badge>
        )
      )}
      {managed && (
        <Tooltip>
          <TooltipTrigger asChild>
            <Badge
              variant="outline"
              className="text-xs"
              data-testid="resource-row-auto-badge"
            >
              {t("statusPages:sections.membership.autoBadge")}
            </Badge>
          </TooltipTrigger>
          <TooltipContent>
            {t("statusPages:sections.membership.autoTooltip")}
          </TooltipContent>
        </Tooltip>
      )}
      {resource.check?.status && <StatusBadge status={resource.check.status} />}
      {resource.checkUid ? (
        <Link
          to="/orgs/$org/checks/$checkUid"
          params={{ org, checkUid: resource.checkUid }}
          search={{
            graphPeriod: undefined,
            graphFull: undefined,
            region: undefined,
          }}
        >
          <Button variant="ghost" size="icon" className="h-7 w-7">
            <Eye className="h-3 w-3" />
          </Button>
        </Link>
      ) : (
        resource.checkGroupUid && (
          <Link
            to="/orgs/$org/check-groups/$uid/edit"
            params={{ org, uid: resource.checkGroupUid }}
          >
            <Button variant="ghost" size="icon" className="h-7 w-7">
              <Eye className="h-3 w-3" />
            </Button>
          </Link>
        )
      )}
      {!managed && (
        <EditResourceTargetDialog
          org={org}
          statusPageUid={statusPageUid}
          sectionUid={sectionUid}
          resource={resource}
          existingCheckUids={existingCheckUids}
          existingGroupUids={existingGroupUids}
        />
      )}
      <AlertDialog open={deleteOpen} onOpenChange={setDeleteOpen}>
        {!managed && (
          <Button
            variant="ghost"
            size="icon"
            className="h-7 w-7 text-destructive"
            onClick={() => setDeleteOpen(true)}
          >
            <Trash2 className="h-3 w-3" />
          </Button>
        )}
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>
              {t("statusPages:resources.remove")}
            </AlertDialogTitle>
            <AlertDialogDescription>
              {t("statusPages:resources.removeDescription")}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>{t("common:cancel")}</AlertDialogCancel>
            <AlertDialogAction onClick={handleDelete}>
              {t("statusPages:resources.removeButton")}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </div>
  );
}

function SectionCard({
  section,
  org,
  statusPageUid,
  visibility,
}: {
  section: StatusPageSection;
  org: string;
  statusPageUid: string;
  visibility?: string;
}) {
  const { t } = useTranslation(["statusPages", "common"]);
  const [deleteOpen, setDeleteOpen] = useState(false);
  const [editOpen, setEditOpen] = useState(false);
  const deleteSection = useDeleteSection(org, statusPageUid);
  const reorderResources = useReorderResources(org, statusPageUid, section.uid);

  const {
    attributes,
    listeners,
    setNodeRef,
    transform,
    transition,
    isDragging,
  } = useSortable({ id: section.uid });
  const sortableStyle = {
    transform: CSS.Transform.toString(transform),
    transition,
    opacity: isDragging ? 0.5 : 1,
  };

  // Each kind gets its own exclusion set: a section may hold at most one
  // resource per check and at most one per group (two partial unique indexes).
  const existingCheckUids = new Set(
    (section.resources || [])
      .map((r) => r.checkUid)
      .filter((uid): uid is string => Boolean(uid)),
  );
  const existingGroupUids = new Set(
    (section.resources || [])
      .map((r) => r.checkGroupUid)
      .filter((uid): uid is string => Boolean(uid)),
  );

  // Manual rows always sort first, so the up/down bounds are over that prefix
  // only — the last manual row must not offer "move down" into managed rows it
  // cannot displace.
  const manualResourceCount = (section.resources || []).filter(
    (r) => !r.managedBySelector,
  ).length;

  // Pointer sensor with a small activation distance so click-and-release on
  // the drag handle doesn't get hijacked as a drag (matters for keyboard /
  // accessibility tooling).
  const sensors = useSensors(
    useSensor(PointerSensor, { activationConstraint: { distance: 4 } }),
    useSensor(KeyboardSensor, {
      coordinateGetter: sortableKeyboardCoordinates,
    }),
  );

  const handleDeleteSection = async () => {
    try {
      await deleteSection.mutateAsync(section.uid);
      toast.success(t("statusPages:sections.deleted"));
    } catch (err) {
      toast.error(
        err instanceof ApiError
          ? err.message
          : t("statusPages:sections.deleteFailed"),
      );
    }
  };

  const handleDragEnd = (event: DragEndEvent) => {
    const { active, over } = event;
    if (!over || active.id === over.id) return;
    const items = section.resources ?? [];
    const oldIndex = items.findIndex((r) => r.uid === active.id);
    const newIndex = items.findIndex((r) => r.uid === over.id);
    if (oldIndex === -1 || newIndex === -1) return;
    const reordered = arrayMove(items, oldIndex, newIndex).map((r) => r.uid);
    reorderResources.mutate(reordered, {
      onError: (err) => {
        toast.error(
          err instanceof ApiError ? err.message : "Failed to reorder",
        );
      },
    });
  };

  return (
    <Card ref={setNodeRef} style={sortableStyle}>
      <CardHeader className="pb-3">
        <div className="flex items-center justify-between">
          <div className="flex items-center gap-2">
            <button
              type="button"
              className="touch-none cursor-grab text-muted-foreground/60 hover:text-muted-foreground focus:outline-none focus-visible:ring-2 focus-visible:ring-ring rounded p-1"
              aria-label={t(
                "statusPages:sections.dragHandle",
                "Drag to reorder section",
              )}
              {...attributes}
              {...listeners}
            >
              <GripVertical className="h-4 w-4" />
            </button>
            <div>
              <CardTitle className="text-lg">{section.name}</CardTitle>
              <CardDescription>{section.slug}</CardDescription>
            </div>
          </div>
          <div className="flex gap-1">
            <AddResourceDialog
              org={org}
              statusPageUid={statusPageUid}
              sectionUid={section.uid}
              existingCheckUids={existingCheckUids}
              existingGroupUids={existingGroupUids}
            />
            <Button
              variant="ghost"
              size="icon"
              className="h-8 w-8 text-muted-foreground"
              onClick={() => setEditOpen(true)}
              aria-label={t("statusPages:sections.edit")}
              title={t("statusPages:sections.edit")}
            >
              <Pencil className="h-4 w-4" />
            </Button>
            <AlertDialog open={deleteOpen} onOpenChange={setDeleteOpen}>
              <Button
                variant="ghost"
                size="icon"
                className="h-8 w-8 text-destructive hover:text-destructive"
                onClick={() => setDeleteOpen(true)}
                aria-label={t("statusPages:sections.delete")}
                title={t("statusPages:sections.delete")}
              >
                <Trash2 className="h-4 w-4" />
              </Button>
              <AlertDialogContent>
                <AlertDialogHeader>
                  <AlertDialogTitle>
                    {t("statusPages:sections.delete")}
                  </AlertDialogTitle>
                  <AlertDialogDescription>
                    {t("statusPages:sections.deleteDescription")}
                  </AlertDialogDescription>
                </AlertDialogHeader>
                <AlertDialogFooter>
                  <AlertDialogCancel>{t("common:cancel")}</AlertDialogCancel>
                  <AlertDialogAction
                    onClick={handleDeleteSection}
                    className="bg-destructive text-destructive-foreground hover:bg-destructive/90"
                  >
                    {t("common:delete")}
                  </AlertDialogAction>
                </AlertDialogFooter>
              </AlertDialogContent>
            </AlertDialog>
          </div>
        </div>
      </CardHeader>
      <EditSectionDialog
        org={org}
        statusPageUid={statusPageUid}
        section={section}
        visibility={visibility}
        open={editOpen}
        onOpenChange={setEditOpen}
      />
      <CardContent className="space-y-3">
        {section.selectorTruncated && (
          <Alert variant="warning" data-testid="section-selector-truncated">
            <AlertTriangle />
            <AlertDescription>
              {t("statusPages:sections.membership.truncated", {
                shown: section.resources?.length ?? 0,
                total: section.selectorMatchTotal ?? 0,
              })}
            </AlertDescription>
          </Alert>
        )}
        <SelectorClaimedElsewhereAlert
          ownResourceCount={section.resources?.length ?? 0}
          claimedElsewhere={section.selectorClaimedElsewhere}
          claimantName={section.selectorClaimedSectionName}
        />
        {section.resources && section.resources.length > 0 ? (
          <DndContext
            sensors={sensors}
            collisionDetection={closestCenter}
            onDragEnd={handleDragEnd}
          >
            {/*
              Only MANUAL rows are sortable. A managed row's position is
              rewritten by the reconciler, so letting it into the sortable set
              would produce a drag that visibly springs back.
            */}
            <SortableContext
              items={section.resources
                .filter((r) => !r.managedBySelector)
                .map((r) => r.uid)}
              strategy={verticalListSortingStrategy}
            >
              <div className="space-y-1">
                {section.resources.map((resource, idx) => (
                  <ResourceRow
                    key={resource.uid}
                    resource={resource}
                    index={idx}
                    total={manualResourceCount}
                    neighbors={section.resources ?? []}
                    org={org}
                    statusPageUid={statusPageUid}
                    sectionUid={section.uid}
                    existingCheckUids={existingCheckUids}
                    existingGroupUids={existingGroupUids}
                  />
                ))}
              </div>
            </SortableContext>
          </DndContext>
        ) : (
          <p className="text-sm text-muted-foreground py-4 text-center">
            {t("statusPages:sections.empty")}
          </p>
        )}
      </CardContent>
    </Card>
  );
}

function StatusPageDetailPage() {
  const { t } = useTranslation(["statusPages", "common"]);
  const { org, statusPageUid } = Route.useParams();
  const navigate = useNavigate();

  const {
    data: page,
    isLoading,
    error,
    refetch,
  } = useStatusPage(org, statusPageUid, { with: "sections" });

  const reorderSections = useReorderSections(org, statusPageUid);
  const deleteStatusPage = useDeleteStatusPage(org);
  const [pageDeleteOpen, setPageDeleteOpen] = useState(false);

  const sectionDndSensors = useSensors(
    useSensor(PointerSensor, { activationConstraint: { distance: 4 } }),
    useSensor(KeyboardSensor, {
      coordinateGetter: sortableKeyboardCoordinates,
    }),
  );

  const handleSectionDragEnd = (event: DragEndEvent) => {
    const { active, over } = event;
    if (!over || !page?.sections || active.id === over.id) return;
    const items = page.sections;
    const oldIndex = items.findIndex((s) => s.uid === active.id);
    const newIndex = items.findIndex((s) => s.uid === over.id);
    if (oldIndex === -1 || newIndex === -1) return;
    const reordered = arrayMove(items, oldIndex, newIndex).map((s) => s.uid);
    reorderSections.mutate(reordered, {
      onError: (err) => {
        toast.error(
          err instanceof ApiError ? err.message : "Failed to reorder",
        );
      },
    });
  };

  const handleDeletePage = async () => {
    if (!page) return;
    try {
      await deleteStatusPage.mutateAsync(page.uid);
      toast.success(t("statusPages:toast.deleted"));
      navigate({ to: "/orgs/$org/status-pages", params: { org } });
    } catch (err) {
      toast.error(
        err instanceof ApiError
          ? err.message
          : t("statusPages:toast.deleteFailed"),
      );
    }
  };

  if (isLoading) {
    return (
      <div className="space-y-6">
        <Skeleton className="h-8 w-64" />
        <Skeleton className="h-48 rounded-lg" />
        <Skeleton className="h-48 rounded-lg" />
      </div>
    );
  }

  if (error) {
    return (
      <QueryErrorView
        error={error}
        org={org}
        resource={t("statusPages:title")}
        backTo="/orgs/$org/status-pages"
        backLabel={t("statusPages:title")}
        onRetry={() => refetch()}
      />
    );
  }

  if (!page) return null;

  return (
    <div className="space-y-6">
      <div className="flex items-start gap-3 justify-between">
        <div className="min-w-0 flex-1">
          <div className="flex items-center gap-2 flex-wrap">
            <h1 className="text-2xl sm:text-3xl font-bold tracking-tight truncate">
              {page.name}
            </h1>
            {page.isDefault && (
              <Star className="h-4 w-4 text-yellow-500 fill-yellow-500 shrink-0" />
            )}
            <EnabledDot
              enabled={page.enabled}
              enabledLabel={t("statusPages:enabled")}
              disabledLabel={t("statusPages:disabled")}
            />
            <VisibilityDot
              isPublic={page.visibility === "public"}
              publicLabel={t("statusPages:visibility.public")}
              restrictedLabel={t("statusPages:visibility.restricted")}
            />
          </div>
          <p className="text-muted-foreground mt-1">
            /{page.slug}
            {page.description && ` - ${page.description}`}
          </p>
        </div>
        <div className="flex gap-2 shrink-0">
          <Link to="/orgs/$org/status-pages" params={{ org }}>
            <Button
              variant="ghost"
              size="icon"
              aria-label={t("statusPages:backToList")}
            >
              <ArrowLeft className="h-4 w-4" />
            </Button>
          </Link>
          <Tooltip>
            <TooltipTrigger asChild>
              <a
                href={`/status0/${org}/${page.slug}`}
                target="_blank"
                rel="noopener noreferrer"
              >
                <Button
                  variant="outline"
                  aria-label={t("statusPages:detail.view")}
                >
                  <ExternalLink className="sm:mr-2 h-4 w-4" />
                  <span className="hidden sm:inline">
                    {t("statusPages:detail.view")}
                  </span>
                </Button>
              </a>
            </TooltipTrigger>
            <TooltipContent className="sm:hidden">
              {t("statusPages:detail.view")}
            </TooltipContent>
          </Tooltip>
          <Tooltip>
            <TooltipTrigger asChild>
              <Link
                to="/orgs/$org/status-pages/$statusPageUid/appearance"
                params={{ org, statusPageUid }}
              >
                <Button
                  variant="outline"
                  aria-label={t("statusPages:appearance.title")}
                  data-testid="status-page-appearance-nav"
                >
                  <Palette className="sm:mr-2 h-4 w-4" />
                  <span className="hidden sm:inline">
                    {t("statusPages:appearance.title")}
                  </span>
                </Button>
              </Link>
            </TooltipTrigger>
            <TooltipContent className="sm:hidden">
              {t("statusPages:appearance.title")}
            </TooltipContent>
          </Tooltip>
          <Tooltip>
            <TooltipTrigger asChild>
              <Link
                to="/orgs/$org/status-pages/$statusPageUid/edit"
                params={{ org, statusPageUid }}
              >
                <Button variant="outline" aria-label={t("statusPages:edit")}>
                  <Pencil className="sm:mr-2 h-4 w-4" />
                  <span className="hidden sm:inline">
                    {t("statusPages:edit")}
                  </span>
                </Button>
              </Link>
            </TooltipTrigger>
            <TooltipContent className="sm:hidden">
              {t("statusPages:edit")}
            </TooltipContent>
          </Tooltip>
          <AlertDialog open={pageDeleteOpen} onOpenChange={setPageDeleteOpen}>
            <Tooltip>
              <TooltipTrigger asChild>
                <Button
                  variant="outline"
                  className="text-destructive hover:text-destructive"
                  onClick={() => setPageDeleteOpen(true)}
                  aria-label={t("statusPages:delete")}
                >
                  <Trash2 className="sm:mr-2 h-4 w-4" />
                  <span className="hidden sm:inline">
                    {t("statusPages:delete")}
                  </span>
                </Button>
              </TooltipTrigger>
              <TooltipContent className="sm:hidden">
                {t("statusPages:delete")}
              </TooltipContent>
            </Tooltip>
            <AlertDialogContent>
              <AlertDialogHeader>
                <AlertDialogTitle>
                  {t("statusPages:deleteDialog.title")}
                </AlertDialogTitle>
                <AlertDialogDescription>
                  {t("statusPages:deleteDialog.description")}
                </AlertDialogDescription>
              </AlertDialogHeader>
              <AlertDialogFooter>
                <AlertDialogCancel>{t("common:cancel")}</AlertDialogCancel>
                <AlertDialogAction
                  onClick={handleDeletePage}
                  className="bg-destructive text-destructive-foreground hover:bg-destructive/90"
                >
                  {t("statusPages:delete")}
                </AlertDialogAction>
              </AlertDialogFooter>
            </AlertDialogContent>
          </AlertDialog>
        </div>
      </div>

      <StatusPageTvCard org={org} page={page} />

      <div className="flex items-center justify-between">
        <h2 className="text-xl font-semibold">
          {t("statusPages:detail.sections")}
        </h2>
        <AddSectionDialog
          org={org}
          statusPageUid={statusPageUid}
          visibility={page.visibility}
        />
      </div>

      {page.sections && page.sections.length > 0 ? (
        <DndContext
          sensors={sectionDndSensors}
          collisionDetection={closestCenter}
          onDragEnd={handleSectionDragEnd}
        >
          <SortableContext
            items={page.sections.map((s) => s.uid)}
            strategy={verticalListSortingStrategy}
          >
            <div className="space-y-4">
              {page.sections.map((section) => (
                <SectionCard
                  key={section.uid}
                  section={section}
                  org={org}
                  statusPageUid={statusPageUid}
                  visibility={page.visibility}
                />
              ))}
            </div>
          </SortableContext>
        </DndContext>
      ) : (
        <Card>
          <CardContent className="py-8 text-center text-muted-foreground">
            <p className="mb-2">{t("statusPages:detail.noSections")}</p>
            <AddSectionDialog
              org={org}
              statusPageUid={statusPageUid}
              visibility={page.visibility}
            />
          </CardContent>
        </Card>
      )}
    </div>
  );
}
