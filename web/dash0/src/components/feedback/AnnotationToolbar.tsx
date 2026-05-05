import { useTranslation } from "react-i18next";
import {
  ArrowUpRight,
  MousePointer,
  Square,
  Type,
  Undo2,
} from "lucide-react";

import { Button } from "@/components/ui/button";

export type AnnotationTool = "select" | "rect" | "arrow" | "text";

interface AnnotationToolbarProps {
  tool: AnnotationTool;
  onToolChange: (tool: AnnotationTool) => void;
  onUndo: () => void;
  canUndo: boolean;
}

export function AnnotationToolbar({
  tool,
  onToolChange,
  onUndo,
  canUndo,
}: AnnotationToolbarProps) {
  const { t } = useTranslation("feedback");

  return (
    <div className="flex items-center gap-1 rounded border bg-background px-1 py-0.5">
      <ToolButton
        active={tool === "select"}
        title={t("tool_select")}
        onClick={() => onToolChange("select")}
      >
        <MousePointer className="h-4 w-4" />
      </ToolButton>
      <ToolButton
        active={tool === "rect"}
        title={t("tool_rect")}
        onClick={() => onToolChange("rect")}
      >
        <Square className="h-4 w-4" />
      </ToolButton>
      <ToolButton
        active={tool === "arrow"}
        title={t("tool_arrow")}
        onClick={() => onToolChange("arrow")}
      >
        <ArrowUpRight className="h-4 w-4" />
      </ToolButton>
      <ToolButton
        active={tool === "text"}
        title={t("tool_text")}
        onClick={() => onToolChange("text")}
      >
        <Type className="h-4 w-4" />
      </ToolButton>
      <div className="mx-1 h-6 w-px bg-border" />
      <Button
        type="button"
        variant="ghost"
        size="sm"
        onClick={onUndo}
        disabled={!canUndo}
        title={t("undo")}
      >
        <Undo2 className="h-4 w-4" />
      </Button>
    </div>
  );
}

interface ToolButtonProps {
  active: boolean;
  title: string;
  onClick: () => void;
  children: React.ReactNode;
}

function ToolButton({ active, title, onClick, children }: ToolButtonProps) {
  return (
    <Button
      type="button"
      variant={active ? "default" : "ghost"}
      size="sm"
      onClick={onClick}
      title={title}
      className="h-8 w-8 p-0"
    >
      {children}
    </Button>
  );
}
