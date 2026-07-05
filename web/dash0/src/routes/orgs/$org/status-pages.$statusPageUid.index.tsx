import { useState } from "react";
import { createFileRoute, Link, useNavigate } from "@tanstack/react-router";
import { useTranslation } from "react-i18next";
import {
  ArrowLeft,
  Eye,
  ExternalLink,
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
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
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
import { CheckPicker } from "@/components/shared/check-picker";
import { QueryErrorView } from "@/components/shared/error-views";
import { ApiError } from "@/api/client";

export const Route = createFileRoute(
  "/orgs/$org/status-pages/$statusPageUid/"
)({
  component: StatusPageDetailPage,
});

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
}: {
  org: string;
  statusPageUid: string;
}) {
  const { t } = useTranslation(["statusPages", "common"]);
  const [open, setOpen] = useState(false);
  const [name, setName] = useState("");
  const [slug, setSlug] = useState("");
  const [slugManuallyEdited, setSlugManuallyEdited] = useState(false);
  const createSection = useCreateSection(org, statusPageUid);

  const handleSubmit = async () => {
    try {
      await createSection.mutateAsync({
        name,
        slug: slug || slugify(name),
      });
      toast.success(t("statusPages:sections.created"));
      setName("");
      setSlug("");
      setSlugManuallyEdited(false);
      setOpen(false);
    } catch (err) {
      toast.error(err instanceof ApiError ? err.message : t("statusPages:sections.createFailed"));
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
          <DialogDescription>{t("statusPages:sections.addDescription")}</DialogDescription>
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
        </div>
        <DialogFooter>
          <Button variant="outline" onClick={() => setOpen(false)}>
            {t("common:cancel")}
          </Button>
          <Button onClick={handleSubmit} disabled={!name || createSection.isPending}>
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
  open,
  onOpenChange,
}: {
  org: string;
  statusPageUid: string;
  section: StatusPageSection;
  open: boolean;
  onOpenChange: (next: boolean) => void;
}) {
  const { t } = useTranslation(["statusPages", "common"]);
  const [name, setName] = useState(section.name);
  const [slug, setSlug] = useState(section.slug);
  const updateSection = useUpdateSection(org, statusPageUid, section.uid);

  const handleSubmit = async () => {
    try {
      await updateSection.mutateAsync({ name, slug: slug || slugify(name) });
      toast.success(t("statusPages:sections.updated"));
      onOpenChange(false);
    } catch (err) {
      toast.error(
        err instanceof ApiError ? err.message : t("statusPages:sections.updateFailed"),
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
        </div>
        <DialogFooter>
          <Button variant="outline" onClick={() => onOpenChange(false)}>
            {t("common:cancel")}
          </Button>
          <Button onClick={handleSubmit} disabled={!name || updateSection.isPending}>
            {t("statusPages:sections.save")}
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
}: {
  org: string;
  statusPageUid: string;
  sectionUid: string;
  existingCheckUids: Set<string>;
}) {
  const { t } = useTranslation(["statusPages", "common"]);
  const [open, setOpen] = useState(false);
  const [selectedCheckUid, setSelectedCheckUid] = useState<string | undefined>();
  const [selectedCheckLabel, setSelectedCheckLabel] = useState<string | undefined>();
  const createResource = useCreateResource(org, statusPageUid, sectionUid);

  const reset = () => {
    setSelectedCheckUid(undefined);
    setSelectedCheckLabel(undefined);
  };

  const handleSubmit = async () => {
    if (!selectedCheckUid) return;
    try {
      await createResource.mutateAsync({ checkUid: selectedCheckUid });
      toast.success(t("statusPages:resources.added"));
      reset();
      setOpen(false);
    } catch (err) {
      toast.error(err instanceof ApiError ? err.message : t("statusPages:resources.addFailed"));
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
          <DialogDescription>{t("statusPages:resources.addDescription")}</DialogDescription>
        </DialogHeader>
        <div className="py-4">
          <CheckPicker
            org={org}
            value={selectedCheckUid}
            selectedLabel={selectedCheckLabel}
            excludeUids={existingCheckUids}
            placeholder={t("statusPages:resources.selectCheck")}
            onChange={(uid, c) => {
              setSelectedCheckUid(uid);
              setSelectedCheckLabel(c ? c.name || c.slug || undefined : undefined);
            }}
          />
        </div>
        <DialogFooter>
          <Button variant="outline" onClick={() => setOpen(false)}>
            {t("common:cancel")}
          </Button>
          <Button
            onClick={handleSubmit}
            disabled={!selectedCheckUid || createResource.isPending}
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
}: {
  resource: StatusPageResource;
  index: number;
  total: number;
  neighbors: StatusPageResource[];
  org: string;
  statusPageUid: string;
  sectionUid: string;
}) {
  const { t } = useTranslation(["statusPages", "common"]);
  const [deleteOpen, setDeleteOpen] = useState(false);
  const deleteResource = useDeleteResource(org, statusPageUid, sectionUid);
  const updateResource = useUpdateResource(org, statusPageUid, sectionUid);

  const handleDelete = async () => {
    try {
      await deleteResource.mutateAsync(resource.uid);
      toast.success(t("statusPages:resources.removed"));
    } catch (err) {
      toast.error(err instanceof ApiError ? err.message : t("statusPages:resources.removeFailed"));
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

  const { attributes, listeners, setNodeRef, transform, transition, isDragging } =
    useSortable({ id: resource.uid });
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
        isDragging && "opacity-60 shadow-md bg-background relative z-10"
      )}
    >
      <button
        type="button"
        className="touch-none cursor-grab active:cursor-grabbing text-muted-foreground/50 hover:text-muted-foreground focus:outline-none focus-visible:ring-2 focus-visible:ring-ring rounded"
        aria-label="Drag to reorder"
        {...attributes}
        {...listeners}
      >
        <GripVertical className="h-4 w-4" />
      </button>
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
      <StatusDot status={resource.check?.status} />
      <span className="flex-1 text-sm">
        {resource.publicName || resource.check?.name || resource.checkUid.slice(0, 8)}
      </span>
      {resource.check?.type && (
        <Badge variant="outline" className="text-xs">
          {resource.check.type}
        </Badge>
      )}
      {resource.check?.status && (
        <StatusBadge status={resource.check.status} />
      )}
      <Link
        to="/orgs/$org/checks/$checkUid"
        params={{ org, checkUid: resource.checkUid }}
        search={{ graphPeriod: undefined, graphFull: undefined, graphRegion: undefined }}
      >
        <Button variant="ghost" size="icon" className="h-7 w-7">
          <Eye className="h-3 w-3" />
        </Button>
      </Link>
      <AlertDialog open={deleteOpen} onOpenChange={setDeleteOpen}>
        <Button
          variant="ghost"
          size="icon"
          className="h-7 w-7"
          onClick={() => setDeleteOpen(true)}
        >
          <Trash2 className="h-3 w-3" />
        </Button>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>{t("statusPages:resources.remove")}</AlertDialogTitle>
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
}: {
  section: StatusPageSection;
  org: string;
  statusPageUid: string;
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

  const existingCheckUids = new Set(
    (section.resources || []).map((r) => r.checkUid)
  );

  // Pointer sensor with a small activation distance so click-and-release on
  // the drag handle doesn't get hijacked as a drag (matters for keyboard /
  // accessibility tooling).
  const sensors = useSensors(
    useSensor(PointerSensor, { activationConstraint: { distance: 4 } }),
    useSensor(KeyboardSensor, { coordinateGetter: sortableKeyboardCoordinates })
  );

  const handleDeleteSection = async () => {
    try {
      await deleteSection.mutateAsync(section.uid);
      toast.success(t("statusPages:sections.deleted"));
    } catch (err) {
      toast.error(err instanceof ApiError ? err.message : t("statusPages:sections.deleteFailed"));
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
        toast.error(err instanceof ApiError ? err.message : "Failed to reorder");
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
              aria-label={t("statusPages:sections.dragHandle", "Drag to reorder section")}
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
                  <AlertDialogTitle>{t("statusPages:sections.delete")}</AlertDialogTitle>
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
        open={editOpen}
        onOpenChange={setEditOpen}
      />
      <CardContent>
        {section.resources && section.resources.length > 0 ? (
          <DndContext
            sensors={sensors}
            collisionDetection={closestCenter}
            onDragEnd={handleDragEnd}
          >
            <SortableContext
              items={section.resources.map((r) => r.uid)}
              strategy={verticalListSortingStrategy}
            >
              <div className="space-y-1">
                {section.resources.map((resource, idx) => (
                  <ResourceRow
                    key={resource.uid}
                    resource={resource}
                    index={idx}
                    total={section.resources?.length ?? 0}
                    neighbors={section.resources ?? []}
                    org={org}
                    statusPageUid={statusPageUid}
                    sectionUid={section.uid}
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
    useSensor(KeyboardSensor, { coordinateGetter: sortableKeyboardCoordinates }),
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
        toast.error(err instanceof ApiError ? err.message : "Failed to reorder");
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
        err instanceof ApiError ? err.message : t("statusPages:toast.deleteFailed"),
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
                  size="sm"
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
                to="/orgs/$org/status-pages/$statusPageUid/edit"
                params={{ org, statusPageUid }}
              >
                <Button
                  variant="outline"
                  size="sm"
                  aria-label={t("statusPages:edit")}
                >
                  <Pencil className="sm:mr-2 h-4 w-4" />
                  <span className="hidden sm:inline">{t("statusPages:edit")}</span>
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
                  size="sm"
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

      <div className="flex items-center justify-between">
        <h2 className="text-xl font-semibold">{t("statusPages:detail.sections")}</h2>
        <AddSectionDialog org={org} statusPageUid={statusPageUid} />
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
                />
              ))}
            </div>
          </SortableContext>
        </DndContext>
      ) : (
        <Card>
          <CardContent className="py-8 text-center text-muted-foreground">
            <p className="mb-2">{t("statusPages:detail.noSections")}</p>
            <AddSectionDialog org={org} statusPageUid={statusPageUid} />
          </CardContent>
        </Card>
      )}
    </div>
  );
}
