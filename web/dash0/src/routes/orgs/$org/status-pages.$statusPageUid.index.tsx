import { useState } from "react";
import { createFileRoute, Link } from "@tanstack/react-router";
import {
  ArrowLeft,
  Eye,
  ExternalLink,
  Pencil,
  Plus,
  Trash2,
  Star,
  GripVertical,
} from "lucide-react";
import { toast } from "sonner";
import {
  useStatusPage,
  useChecks,
  useCreateSection,
  useDeleteSection,
  useCreateResource,
  useDeleteResource,
  type StatusPageSection,
  type StatusPageResource,
  type Check,
} from "@/api/hooks";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Badge } from "@/components/ui/badge";
import { Skeleton } from "@/components/ui/skeleton";
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
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
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
import { QueryErrorView } from "@/components/shared/error-views";
import { ApiError } from "@/api/client";

export const Route = createFileRoute(
  "/orgs/$org/status-pages/$statusPageUid/"
)({
  component: StatusPageDetailPage,
});

function slugify(name: string): string {
  return name
    .toLowerCase()
    .replace(/[^a-z0-9-]/g, "-")
    .replace(/-+/g, "-")
    .replace(/^-|-$/g, "")
    .slice(0, 40);
}

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

function AddSectionDialog({
  org,
  statusPageUid,
}: {
  org: string;
  statusPageUid: string;
}) {
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
      toast.success("Section created");
      setName("");
      setSlug("");
      setSlugManuallyEdited(false);
      setOpen(false);
    } catch (err) {
      toast.error(err instanceof ApiError ? err.message : "Failed to create section");
    }
  };

  return (
    <Dialog open={open} onOpenChange={setOpen}>
      <DialogTrigger asChild>
        <Button variant="outline" size="sm">
          <Plus className="mr-2 h-4 w-4" />
          Add Section
        </Button>
      </DialogTrigger>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>Add Section</DialogTitle>
          <DialogDescription>
            Create a new section to group checks on this status page.
          </DialogDescription>
        </DialogHeader>
        <div className="space-y-4 py-4">
          <div className="space-y-2">
            <Label>Name</Label>
            <Input
              value={name}
              onChange={(e) => {
                setName(e.target.value);
                if (!slugManuallyEdited) setSlug(slugify(e.target.value));
              }}
              placeholder="Core Services"
            />
          </div>
          <div className="space-y-2">
            <Label>Slug</Label>
            <Input
              value={slug}
              onChange={(e) => {
                setSlug(e.target.value);
                setSlugManuallyEdited(true);
              }}
              placeholder="core-services"
            />
          </div>
        </div>
        <DialogFooter>
          <Button variant="outline" onClick={() => setOpen(false)}>
            Cancel
          </Button>
          <Button onClick={handleSubmit} disabled={!name || createSection.isPending}>
            Create Section
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
  checks,
  existingCheckUids,
}: {
  org: string;
  statusPageUid: string;
  sectionUid: string;
  checks: Check[];
  existingCheckUids: Set<string>;
}) {
  const [open, setOpen] = useState(false);
  const [selectedCheckUid, setSelectedCheckUid] = useState("");
  const createResource = useCreateResource(org, statusPageUid, sectionUid);

  const availableChecks = checks.filter((c) => !existingCheckUids.has(c.uid));

  const handleSubmit = async () => {
    if (!selectedCheckUid) return;
    try {
      await createResource.mutateAsync({ checkUid: selectedCheckUid });
      toast.success("Check added to section");
      setSelectedCheckUid("");
      setOpen(false);
    } catch (err) {
      toast.error(err instanceof ApiError ? err.message : "Failed to add check");
    }
  };

  return (
    <Dialog open={open} onOpenChange={setOpen}>
      <DialogTrigger asChild>
        <Button variant="ghost" size="sm">
          <Plus className="mr-1 h-3 w-3" />
          Add Check
        </Button>
      </DialogTrigger>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>Add Check to Section</DialogTitle>
          <DialogDescription>Select a check to add to this section.</DialogDescription>
        </DialogHeader>
        <div className="py-4">
          <Select value={selectedCheckUid} onValueChange={setSelectedCheckUid}>
            <SelectTrigger>
              <SelectValue placeholder="Select a check..." />
            </SelectTrigger>
            <SelectContent>
              {availableChecks.map((check) => (
                <SelectItem key={check.uid} value={check.uid}>
                  {check.name || check.slug || check.uid.slice(0, 8)}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </div>
        <DialogFooter>
          <Button variant="outline" onClick={() => setOpen(false)}>
            Cancel
          </Button>
          <Button
            onClick={handleSubmit}
            disabled={!selectedCheckUid || createResource.isPending}
          >
            Add Check
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

function ResourceRow({
  resource,
  org,
  statusPageUid,
  sectionUid,
}: {
  resource: StatusPageResource;
  org: string;
  statusPageUid: string;
  sectionUid: string;
}) {
  const [deleteOpen, setDeleteOpen] = useState(false);
  const deleteResource = useDeleteResource(org, statusPageUid, sectionUid);

  const handleDelete = async () => {
    try {
      await deleteResource.mutateAsync(resource.uid);
      toast.success("Check removed from section");
    } catch (err) {
      toast.error(err instanceof ApiError ? err.message : "Failed to remove check");
    }
  };

  return (
    <div className="flex items-center gap-3 py-2 px-3 rounded-md hover:bg-muted/50">
      <GripVertical className="h-4 w-4 text-muted-foreground/50" />
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
        <Badge
          variant="secondary"
          className={
            resource.check.status === "up"
              ? "bg-green-500/10 text-green-500"
              : resource.check.status === "down"
                ? "bg-red-500/10 text-red-500"
                : ""
          }
        >
          {resource.check.status}
        </Badge>
      )}
      <Link
        to="/orgs/$org/checks/$checkUid"
        params={{ org, checkUid: resource.checkUid }}
        search={{ graphPeriod: undefined, graphFull: undefined }}
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
            <AlertDialogTitle>Remove Check</AlertDialogTitle>
            <AlertDialogDescription>
              Remove this check from the section? The check itself will not be deleted.
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>Cancel</AlertDialogCancel>
            <AlertDialogAction onClick={handleDelete}>Remove</AlertDialogAction>
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
  checks,
}: {
  section: StatusPageSection;
  org: string;
  statusPageUid: string;
  checks: Check[];
}) {
  const [deleteOpen, setDeleteOpen] = useState(false);
  const deleteSection = useDeleteSection(org, statusPageUid);

  const existingCheckUids = new Set(
    (section.resources || []).map((r) => r.checkUid)
  );

  const handleDeleteSection = async () => {
    try {
      await deleteSection.mutateAsync(section.uid);
      toast.success("Section deleted");
    } catch (err) {
      toast.error(err instanceof ApiError ? err.message : "Failed to delete section");
    }
  };

  return (
    <Card>
      <CardHeader className="pb-3">
        <div className="flex items-center justify-between">
          <div>
            <CardTitle className="text-lg">{section.name}</CardTitle>
            <CardDescription>{section.slug}</CardDescription>
          </div>
          <div className="flex gap-1">
            <AddResourceDialog
              org={org}
              statusPageUid={statusPageUid}
              sectionUid={section.uid}
              checks={checks}
              existingCheckUids={existingCheckUids}
            />
            <AlertDialog open={deleteOpen} onOpenChange={setDeleteOpen}>
              <Button
                variant="ghost"
                size="icon"
                className="h-8 w-8 text-muted-foreground"
                onClick={() => setDeleteOpen(true)}
              >
                <Trash2 className="h-4 w-4" />
              </Button>
              <AlertDialogContent>
                <AlertDialogHeader>
                  <AlertDialogTitle>Delete Section</AlertDialogTitle>
                  <AlertDialogDescription>
                    Delete this section and all its resource assignments? The checks
                    themselves will not be affected.
                  </AlertDialogDescription>
                </AlertDialogHeader>
                <AlertDialogFooter>
                  <AlertDialogCancel>Cancel</AlertDialogCancel>
                  <AlertDialogAction
                    onClick={handleDeleteSection}
                    className="bg-destructive text-destructive-foreground hover:bg-destructive/90"
                  >
                    Delete
                  </AlertDialogAction>
                </AlertDialogFooter>
              </AlertDialogContent>
            </AlertDialog>
          </div>
        </div>
      </CardHeader>
      <CardContent>
        {section.resources && section.resources.length > 0 ? (
          <div className="space-y-1">
            {section.resources.map((resource) => (
              <ResourceRow
                key={resource.uid}
                resource={resource}
                org={org}
                statusPageUid={statusPageUid}
                sectionUid={section.uid}
              />
            ))}
          </div>
        ) : (
          <p className="text-sm text-muted-foreground py-4 text-center">
            No checks in this section yet
          </p>
        )}
      </CardContent>
    </Card>
  );
}

function StatusPageDetailPage() {
  const { org, statusPageUid } = Route.useParams();

  const {
    data: page,
    isLoading,
    error,
    refetch,
  } = useStatusPage(org, statusPageUid, { with: "sections" });

  const { data: checks } = useChecks(org);

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
        resource="Status Page"
        backTo="/orgs/$org/status-pages"
        backLabel="Back to Status Pages"
        onRetry={() => refetch()}
      />
    );
  }

  if (!page) return null;

  return (
    <div className="space-y-6">
      <div className="flex items-center justify-between">
        <div className="flex items-center gap-4">
          <Link to="/orgs/$org/status-pages" params={{ org }}>
            <Button variant="ghost" size="icon">
              <ArrowLeft className="h-4 w-4" />
            </Button>
          </Link>
          <div>
            <div className="flex items-center gap-2">
              <h1 className="text-3xl font-bold tracking-tight">{page.name}</h1>
              {page.isDefault && (
                <Star className="h-4 w-4 text-yellow-500 fill-yellow-500" />
              )}
            </div>
            <p className="text-muted-foreground">
              /{page.slug}
              {page.description && ` - ${page.description}`}
            </p>
          </div>
        </div>
        <div className="flex gap-2">
          <Badge variant={page.visibility === "public" ? "default" : "secondary"}>
            {page.visibility}
          </Badge>
          <Badge variant={page.enabled ? "default" : "outline"}>
            {page.enabled ? "Enabled" : "Disabled"}
          </Badge>
          <a
            href={`/status0/${org}/${page.slug}`}
            target="_blank"
            rel="noopener noreferrer"
          >
            <Button variant="outline" size="sm">
              <ExternalLink className="mr-2 h-4 w-4" />
              View
            </Button>
          </a>
          <Link
            to="/orgs/$org/status-pages/$statusPageUid/edit"
            params={{ org, statusPageUid }}
          >
            <Button variant="outline" size="sm">
              <Pencil className="mr-2 h-4 w-4" />
              Edit
            </Button>
          </Link>
        </div>
      </div>

      <div className="flex items-center justify-between">
        <h2 className="text-xl font-semibold">Sections</h2>
        <AddSectionDialog org={org} statusPageUid={statusPageUid} />
      </div>

      {page.sections && page.sections.length > 0 ? (
        <div className="space-y-4">
          {page.sections.map((section) => (
            <SectionCard
              key={section.uid}
              section={section}
              org={org}
              statusPageUid={statusPageUid}
              checks={checks || []}
            />
          ))}
        </div>
      ) : (
        <Card>
          <CardContent className="py-8 text-center text-muted-foreground">
            <p className="mb-2">No sections yet</p>
            <AddSectionDialog org={org} statusPageUid={statusPageUid} />
          </CardContent>
        </Card>
      )}
    </div>
  );
}
