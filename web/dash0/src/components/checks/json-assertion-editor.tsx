import { useTranslation } from "react-i18next";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { Card } from "@/components/ui/card";
import { Plus, Trash2 } from "lucide-react";

interface AssertionNode {
  type: "assertion" | "and" | "or";
  path?: string;
  operator?: string;
  value?: string;
  children?: AssertionNode[];
}

const OPERATORS = [
  "eq",
  "neq",
  "gt",
  "gte",
  "lt",
  "lte",
  "contains",
  "regex",
  "exists",
  "not_exists",
] as const;

const NO_VALUE_OPERATORS = new Set(["exists", "not_exists"]);

interface JsonAssertionEditorProps {
  value: AssertionNode | null;
  onChange: (value: AssertionNode | null) => void;
}

export function JsonAssertionEditor({
  value,
  onChange,
}: JsonAssertionEditorProps) {
  const { t } = useTranslation("checks");

  if (!value) {
    return (
      <Button
        type="button"
        variant="outline"
        size="sm"
        onClick={() =>
          onChange({
            type: "assertion",
            path: "$.status",
            operator: "eq",
            value: "",
          })
        }
        data-testid="json-assertion-add"
      >
        <Plus className="mr-2 h-4 w-4" />
        {t("addJsonAssertion")}
      </Button>
    );
  }

  return (
    <div data-testid="json-assertion-editor" className="space-y-2">
      <NodeEditor
        node={value}
        onChange={onChange}
        onRemove={() => onChange(null)}
        depth={0}
      />
    </div>
  );
}

interface NodeEditorProps {
  node: AssertionNode;
  onChange: (node: AssertionNode) => void;
  onRemove: () => void;
  depth: number;
}

function NodeEditor({ node, onChange, onRemove, depth }: NodeEditorProps) {
  const { t } = useTranslation("checks");

  if (node.type === "assertion") {
    return (
      <div className="flex items-center gap-2 flex-wrap">
        <Input
          value={node.path ?? ""}
          onChange={(e) => onChange({ ...node, path: e.target.value })}
          placeholder="$.path"
          className="w-40 font-mono text-sm"
        />
        <Select
          value={node.operator ?? "eq"}
          onValueChange={(op) => onChange({ ...node, operator: op })}
        >
          <SelectTrigger className="w-32">
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            {OPERATORS.map((op) => (
              <SelectItem key={op} value={op}>
                {op}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
        {!NO_VALUE_OPERATORS.has(node.operator ?? "") && (
          <Input
            value={node.value ?? ""}
            onChange={(e) => onChange({ ...node, value: e.target.value })}
            placeholder={t("expectedValue")}
            className="w-40 text-sm"
          />
        )}
        <Button
          type="button"
          variant="ghost"
          size="icon"
          onClick={onRemove}
          className="h-8 w-8"
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
          onValueChange={(t) =>
            onChange({ ...node, type: t as "and" | "or" })
          }
        >
          <SelectTrigger className="w-20">
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value="and">AND</SelectItem>
            <SelectItem value="or">OR</SelectItem>
          </SelectContent>
        </Select>
        <Button
          type="button"
          variant="ghost"
          size="icon"
          onClick={onRemove}
          className="h-8 w-8"
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
          />
        ))}
        <div className="flex gap-2">
          <Button
            type="button"
            variant="outline"
            size="sm"
            onClick={() =>
              onChange({
                ...node,
                children: [
                  ...children,
                  {
                    type: "assertion",
                    path: "",
                    operator: "eq",
                    value: "",
                  },
                ],
              })
            }
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
                  {
                    type: "and",
                    children: [
                      {
                        type: "assertion",
                        path: "",
                        operator: "eq",
                        value: "",
                      },
                    ],
                  },
                ],
              })
            }
          >
            <Plus className="mr-1 h-3 w-3" />
            {t("addGroup")}
          </Button>
        </div>
      </div>
    </Card>
  );
}
