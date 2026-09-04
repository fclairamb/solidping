import { Link } from "@tanstack/react-router";
import { useTranslation } from "react-i18next";
import { Pencil } from "lucide-react";

import {
  useCheckDependencies,
  type CheckRef,
  type DependencyEdge,
} from "@/api/hooks";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { resolveCheckRefLabel } from "@/lib/dependency-graph";
import { DependencyWarnings } from "@/components/checks/dependency-warnings";
import {
  DependencyEmptyRow,
  DependencyKindBadge,
  DependencyRow,
  DependencyRowList,
  DependencyRowText,
} from "@/components/checks/dependency-row";

interface DependenciesCardProps {
  org: string;
  checkUid: string;
}

// DependenciesCard is READ-ONLY. The check detail page is the view surface for
// a check — every other attribute is displayed here and edited on
// /checks/$checkUid/edit — so dependencies are edited there too, via the
// form's Dependencies section (deep-linked from the header button below).
// Nothing on this card mutates anything.
export function DependenciesCard({ org, checkUid }: DependenciesCardProps) {
  const { t } = useTranslation(["dependencies", "common"]);

  const { data: deps, isLoading } = useCheckDependencies(org, checkUid);

  const dependsOn = deps?.dependsOn ?? [];
  const dependedOnBy = deps?.dependedOnBy ?? [];

  return (
    <Card>
      <CardHeader className="flex flex-row items-center justify-between gap-2 space-y-0">
        <CardTitle>{t("dependencies:title")}</CardTitle>
        <Button
          asChild
          size="sm"
          variant="ghost"
          data-testid="dependencies-edit-link"
        >
          <Link
            to="/orgs/$org/checks/$checkUid/edit"
            params={{ org, checkUid }}
            search={{ section: "dependencies" }}
            // Deliberately NOT an aria-label: the visible "Edit" text already
            // names the link, and any aria-label containing "edit" would make
            // the page header's own Edit button ambiguous to
            // `getByLabel("Edit")` in the e2e suite.
            title={t("dependencies:editDependencies")}
          >
            <Pencil className="h-3.5 w-3.5" />
            {t("common:edit", { defaultValue: "Edit" })}
          </Link>
        </Button>
      </CardHeader>
      <CardContent className="space-y-6">
        <DependencyWarnings warnings={deps?.warnings} />
        <DependencyEdgeSection
          org={org}
          title={t("dependencies:dependsOn")}
          help={t("dependencies:dependsOnHelp")}
          emptyLabel={t("dependencies:noDependencies")}
          edges={dependsOn}
          side="parent"
          loading={isLoading}
          testId="depends-on-list"
        />
        <DependencyEdgeSection
          org={org}
          title={t("dependencies:dependedOnBy")}
          help={t("dependencies:dependedOnByHelp")}
          emptyLabel={t("dependencies:noDependents")}
          edges={dependedOnBy}
          side="child"
          loading={isLoading}
          testId="depended-on-by-list"
        />
      </CardContent>
    </Card>
  );
}

interface DependencyEdgeSectionProps {
  org: string;
  title: string;
  help: string;
  emptyLabel: string;
  edges: DependencyEdge[];
  /** Which end of the edge to display — the other end is this check. */
  side: "parent" | "child";
  loading: boolean;
  testId: string;
}

function DependencyEdgeSection({
  org,
  title,
  help,
  emptyLabel,
  edges,
  side,
  loading,
  testId,
}: DependencyEdgeSectionProps) {
  return (
    <div className="space-y-2">
      <div>
        <h3 className="text-sm font-medium">{title}</h3>
        <p className="text-xs text-muted-foreground">{help}</p>
      </div>
      <DependencyRowList data-testid={testId}>
        {edges.map((edge) => (
          <DependencyRow
            key={edge.uid}
            interactive
            identity={
              <DependencyCheckLink
                org={org}
                check={side === "parent" ? edge.parentCheck : edge.childCheck}
              />
            }
            kind={<DependencyKindBadge kind={edge.kind} />}
            description={
              edge.description ? (
                <DependencyRowText>{edge.description}</DependencyRowText>
              ) : null
            }
          />
        ))}
        {!loading && edges.length === 0 && (
          <DependencyEmptyRow>{emptyLabel}</DependencyEmptyRow>
        )}
      </DependencyRowList>
    </div>
  );
}

// DependencyCheckLink renders a dependency row's check reference as a link to
// its detail page. The backend omits edges whose check ref didn't resolve
// (see checkdependencies.Service.ListForCheck), but this stays defensive: if
// `check.uid` is ever empty (e.g. a legacy row from before that filter
// shipped), render muted placeholder text instead of a link to nowhere, so
// the row never shows up as just a bare kind badge (issue #129).
function DependencyCheckLink({ org, check }: { org: string; check: CheckRef }) {
  const { t } = useTranslation(["dependencies"]);
  const label = resolveCheckRefLabel(check);

  if (!check.uid) {
    return (
      <span className="font-medium text-muted-foreground italic">
        {t("dependencies:unknownCheck")}
      </span>
    );
  }

  return (
    <Link
      to="/orgs/$org/checks/$checkUid"
      params={{ org, checkUid: check.uid }}
      search={{
        graphPeriod: undefined,
        graphFull: undefined,
        region: undefined,
      }}
      className="font-medium hover:underline"
    >
      {label}
    </Link>
  );
}
