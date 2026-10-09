import { useTranslation } from "react-i18next";
import {
  ArrowUpRight,
  EyeOff,
  Highlighter,
  MousePointer,
  Pencil,
  Redo2,
  Square,
  Type,
  Undo2,
} from "lucide-react";

import { Button } from "@/components/ui/button";
import { cn } from "@/lib/utils";
import { ANNOTATION_COLORS } from "./types";

export type AnnotationTool =
  | "select"
  | "rect"
  | "arrow"
  | "text"
  | "pen"
  | "highlight"
  | "blur";

interface AnnotationToolbarProps {
  tool: AnnotationTool;
  onToolChange: (tool: AnnotationTool) => void;
  color: string;
  onColorChange: (color: string) => void;
  onUndo: () => void;
  onRedo: () => void;
  canUndo: boolean;
  canRedo: boolean;
}

export function AnnotationToolbar({
  tool,
  onToolChange,
  color,
  onColorChange,
  onUndo,
  onRedo,
  canUndo,
  canRedo,
}: AnnotationToolbarProps) {
  const { t } = useTranslation("feedback");

  const tools: { id: AnnotationTool; label: string; icon: React.ReactNode }[] = [
    { id: "select", label: t("tool_select"), icon: <MousePointer className="h-4 w-4" /> },
    { id: "rect", label: t("tool_rect"), icon: <Square className="h-4 w-4" /> },
    { id: "arrow", label: t("tool_arrow"), icon: <ArrowUpRight className="h-4 w-4" /> },
    { id: "pen", label: t("tool_pen"), icon: <Pencil className="h-4 w-4" /> },
    { id: "highlight", label: t("tool_highlight"), icon: <Highlighter className="h-4 w-4" /> },
    { id: "blur", label: t("tool_blur"), icon: <EyeOff className="h-4 w-4" /> },
    { id: "text", label: t("tool_text"), icon: <Type className="h-4 w-4" /> },
  ];

  return (
    <div
      className="flex flex-wrap items-center gap-1 rounded border bg-background px-1 py-0.5"
      data-testid="annotation-toolbar"
    >
      {tools.map((item) => (
        <Button
          key={item.id}
          type="button"
          variant={tool === item.id ? "default" : "ghost"}
          size="sm"
          onClick={() => onToolChange(item.id)}
          title={item.label}
          aria-label={item.label}
          aria-pressed={tool === item.id}
          className="h-10 w-10 p-0"
          data-testid={`tool-${item.id}`}
        >
          {item.icon}
        </Button>
      ))}
      <div className="mx-1 h-6 w-px bg-border" />
      {ANNOTATION_COLORS.map((c) => (
        <button
          key={c}
          type="button"
          onClick={() => onColorChange(c)}
          aria-label={t("color", { color: c })}
          aria-pressed={color === c}
          data-testid={`color-${c.slice(1)}`}
          className="flex h-10 w-8 items-center justify-center"
        >
          <span
            className={cn(
              "h-6 w-6 rounded-full border-2",
              color === c ? "border-foreground" : "border-transparent",
            )}
            style={{ backgroundColor: c }}
          />
        </button>
      ))}
      <div className="mx-1 h-6 w-px bg-border" />
      <Button
        type="button"
        variant="ghost"
        size="sm"
        onClick={onUndo}
        disabled={!canUndo}
        title={t("undo")}
        aria-label={t("undo")}
        className="h-10 w-10 p-0"
        data-testid="annotation-undo"
      >
        <Undo2 className="h-4 w-4" />
      </Button>
      <Button
        type="button"
        variant="ghost"
        size="sm"
        onClick={onRedo}
        disabled={!canRedo}
        title={t("redo")}
        aria-label={t("redo")}
        className="h-10 w-10 p-0"
        data-testid="annotation-redo"
      >
        <Redo2 className="h-4 w-4" />
      </Button>
    </div>
  );
}
