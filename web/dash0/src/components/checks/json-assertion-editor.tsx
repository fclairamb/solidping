import { useId } from "react";
import { useTranslation } from "react-i18next";
import { Button } from "@/components/ui/button";
import { Checkbox } from "@/components/ui/checkbox";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { Card } from "@/components/ui/card";
import { Plus, Trash2 } from "lucide-react";

export interface AssertionNode {
  type: "assertion" | "and" | "or";
  path?: string;
  operator?: string;
  value?: string;
  ignoreCase?: boolean;
  children?: AssertionNode[];
}

// The full operator vocabulary, used by JSONPath assertions where the subject
// is one value plucked out of a parsed JSON document.
export const JSON_ASSERTION_OPERATORS = [
  "eq",
  "neq",
  "gt",
  "gte",
  "lt",
  "lte",
  "contains",
  "not_contains",
  "regex",
  "exists",
  "not_exists",
] as const;

// Body assertions compare against the raw response body, so the numeric
// comparisons and exists/not_exists are meaningless there — a body always
// "exists", and accepting those would build an assertion that can never fail.
// The server rejects them with a VALIDATION_ERROR (checkhttp ValidateBody);
// this list is what keeps the UI from ever offering them.
export const BODY_ASSERTION_OPERATORS = [
  "eq",
  "neq",
  "contains",
  "not_contains",
  "regex",
] as const;

const NO_VALUE_OPERATORS = new Set(["exists", "not_exists"]);

// Operators whose comparison is textual, and therefore the only ones an
// "ignore case" flag means anything for.
const CASE_SENSITIVE_OPERATORS = new Set([
  "eq",
  "neq",
  "contains",
  "not_contains",
  "regex",
]);

interface AssertionEditorProps {
  value: AssertionNode | null;
  onChange: (value: AssertionNode | null) => void;
  // Whether leaves carry a JSONPath input. False for body assertions, whose
  // subject is the whole response body and which ignore `path` entirely.
  showPath?: boolean;
  operators?: readonly string[];
  // Prefix for every data-testid in the tree, so two editors can be mounted
  // on the same form without colliding.
  testIdPrefix?: string;
}

// JsonAssertionEditor renders the recursive assertion AST the HTTP checker
// evaluates. It backs BOTH assertion editors: JSONPath (path input shown, full
// operator list) and body text (no path, narrowed operator list) — the tree,
// the and/or grouping and the ignore-case flag are identical, so there is one
// component rather than a fork.
export function JsonAssertionEditor({
  value,
  onChange,
  showPath = true,
  operators = JSON_ASSERTION_OPERATORS,
  testIdPrefix = "json-assertion",
}: AssertionEditorProps) {
  const { t } = useTranslation("checks");

  const newLeaf = (): AssertionNode => ({
    type: "assertion",
    ...(showPath ? { path: "$.status" } : {}),
    operator: operators[0] ?? "eq",
    value: "",
  });

  if (!value) {
    return (
      <Button
        type="button"
        variant="outline"
        size="sm"
        onClick={() => onChange(newLeaf())}
        data-testid={`${testIdPrefix}-add`}
      >
        <Plus className="mr-2 h-4 w-4" />
        {t("addJsonAssertion")}
      </Button>
    );
  }

  return (
    <div data-testid={`${testIdPrefix}-editor`} className="space-y-2">
      <NodeEditor
        node={value}
        onChange={onChange}
        onRemove={() => onChange(null)}
        depth={0}
        showPath={showPath}
        operators={operators}
        testIdPrefix={testIdPrefix}
      />
    </div>
  );
}

// BodyAssertionEditor is JsonAssertionEditor with the path input hidden and
// the operator list narrowed to the ones the server accepts against a raw
// body. Same component, same AST, different subject.
export function BodyAssertionEditor(
  props: Pick<AssertionEditorProps, "value" | "onChange">,
) {
  return (
    <JsonAssertionEditor
      {...props}
      showPath={false}
      operators={BODY_ASSERTION_OPERATORS}
      testIdPrefix="body-assertion"
    />
  );
}

interface NodeEditorProps {
  node: AssertionNode;
  onChange: (node: AssertionNode) => void;
  onRemove: () => void;
  depth: number;
  showPath: boolean;
  operators: readonly string[];
  testIdPrefix: string;
}

function NodeEditor({
  node,
  onChange,
  onRemove,
  depth,
  showPath,
  operators,
  testIdPrefix,
}: NodeEditorProps) {
  const { t } = useTranslation("checks");
  const ignoreCaseId = useId();

  const emptyLeaf = (): AssertionNode => ({
    type: "assertion",
    ...(showPath ? { path: "" } : {}),
    operator: operators[0] ?? "eq",
    value: "",
  });

  if (node.type === "assertion") {
    const operator = node.operator ?? operators[0] ?? "eq";
    return (
      <div className="flex items-center gap-2 flex-wrap">
        {showPath && (
          <Input
            value={node.path ?? ""}
            onChange={(e) => onChange({ ...node, path: e.target.value })}
            placeholder="$.path"
            className="w-full font-mono text-sm sm:w-40"
            data-testid={`${testIdPrefix}-path`}
          />
        )}
        <Select
          value={operator}
          onValueChange={(op) => onChange({ ...node, operator: op })}
        >
          <SelectTrigger
            className="w-40"
            data-testid={`${testIdPrefix}-operator`}
          >
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            {operators.map((op) => (
              <SelectItem key={op} value={op}>
                {t(`assertionOperators.${op}`, op)}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
        {!NO_VALUE_OPERATORS.has(operator) && (
          <Input
            value={node.value ?? ""}
            onChange={(e) => onChange({ ...node, value: e.target.value })}
            placeholder={t("expectedValue")}
            className="w-full text-sm sm:w-40"
            data-testid={`${testIdPrefix}-value`}
          />
        )}
        {CASE_SENSITIVE_OPERATORS.has(operator) && (
          <div className="flex items-center gap-1.5">
            <Checkbox
              id={ignoreCaseId}
              checked={node.ignoreCase === true}
              onCheckedChange={(checked) =>
                // Written only at true, mirroring the server's
                // omit-at-default serialization: an untouched assertion never
                // gains an `ignoreCase` key.
                onChange(
                  checked === true
                    ? { ...node, ignoreCase: true }
                    : { ...node, ignoreCase: undefined },
                )
              }
              data-testid={`${testIdPrefix}-ignore-case`}
            />
            <Label
              htmlFor={ignoreCaseId}
              className="text-xs font-normal text-muted-foreground"
            >
              {t("assertionIgnoreCase", "Ignore case")}
            </Label>
          </div>
        )}
        <Button
          type="button"
          variant="ghost"
          size="icon"
          onClick={onRemove}
          className="h-8 w-8"
          data-testid={`${testIdPrefix}-remove`}
        >
          <Trash2 className="h-4 w-4" />
        </Button>
      </div>
    );
  }

  // Group node (and/or)
  const children = node.children ?? [];
  return (
    <Card className={`p-3 ${depth > 0 ? "ml-6" : ""}`}>
      <div className="flex items-center gap-2 mb-2">
        <Select
          value={node.type}
          onValueChange={(groupType) =>
            onChange({ ...node, type: groupType as "and" | "or" })
          }
        >
          <SelectTrigger
            className="w-20"
            data-testid={`${testIdPrefix}-group-type`}
          >
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value="and">{t("jsonAssertionAnd")}</SelectItem>
            <SelectItem value="or">{t("jsonAssertionOr")}</SelectItem>
          </SelectContent>
        </Select>
        <Button
          type="button"
          variant="ghost"
          size="icon"
          onClick={onRemove}
          className="h-8 w-8"
          data-testid={`${testIdPrefix}-remove-group`}
        >
          <Trash2 className="h-4 w-4" />
        </Button>
      </div>
      <div className="space-y-2 pl-4">
        {children.map((child, i) => (
          <NodeEditor
            key={i}
            node={child}
            onChange={(updated) => {
              const newChildren = [...children];
              newChildren[i] = updated;
              onChange({ ...node, children: newChildren });
            }}
            onRemove={() => {
              const newChildren = children.filter((_, j) => j !== i);
              if (newChildren.length === 0) {
                onRemove();
              } else {
                onChange({ ...node, children: newChildren });
              }
            }}
            depth={depth + 1}
            showPath={showPath}
            operators={operators}
            testIdPrefix={testIdPrefix}
          />
        ))}
        <div className="flex gap-2">
          <Button
            type="button"
            variant="outline"
            size="sm"
            onClick={() =>
              onChange({ ...node, children: [...children, emptyLeaf()] })
            }
            data-testid={`${testIdPrefix}-add-in-group`}
          >
            <Plus className="mr-1 h-3 w-3" />
            {t("addJsonAssertion")}
          </Button>
          <Button
            type="button"
            variant="outline"
            size="sm"
            onClick={() =>
              onChange({
                ...node,
                children: [
                  ...children,
                  { type: "and", children: [emptyLeaf()] },
                ],
              })
            }
            data-testid={`${testIdPrefix}-add-group`}
          >
            <Plus className="mr-1 h-3 w-3" />
            {t("addGroup")}
          </Button>
        </div>
      </div>
    </Card>
  );
}
