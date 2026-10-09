import { useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import { toast } from "sonner";

import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Button } from "@/components/ui/button";
import { Textarea } from "@/components/ui/textarea";
import { AnnotationCanvas, renderAnnotations } from "./AnnotationCanvas";
import { AnnotationToolbar, type AnnotationTool } from "./AnnotationToolbar";
import { ANNOTATION_COLORS, type Annotation } from "./types";
import type { SubmitPayload } from "./useFeedback";

interface FeedbackDialogProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  screenshot: Blob | null;
  isCapturing: boolean;
  onSubmit: (payload: SubmitPayload) => Promise<void>;
}

export function FeedbackDialog({
  open,
  onOpenChange,
  screenshot,
  isCapturing,
  onSubmit,
}: FeedbackDialogProps) {
  const { t } = useTranslation("feedback");
  const [comment, setComment] = useState("");
  const [submitting, setSubmitting] = useState(false);
  const [tool, setTool] = useState<AnnotationTool>("select");
  const [annotations, setAnnotationsState] = useState<Annotation[]>([]);
  const [redoStack, setRedoStack] = useState<Annotation[]>([]);
  const [color, setColor] = useState(ANNOTATION_COLORS[0]);
  const [previewURL, setPreviewURL] = useState<string | null>(null);

  // Reset comment + annotations when the dialog opens fresh.
  useEffect(() => {
    if (!open) {
      setComment("");
      setAnnotationsState([]);
      setRedoStack([]);
      setTool("select");
      setColor(ANNOTATION_COLORS[0]);
    }
  }, [open]);

  // Build (and revoke) an object URL for the screenshot preview. Kept in
  // state so the preview renders as soon as the URL exists.
  useEffect(() => {
    if (!screenshot) {
      setPreviewURL(null);
      return undefined;
    }
    const url = URL.createObjectURL(screenshot);
    setPreviewURL(url);
    return () => URL.revokeObjectURL(url);
  }, [screenshot]);

  // Any new edit invalidates the redo history.
  function setAnnotations(next: Annotation[]) {
    setAnnotationsState(next);
    setRedoStack([]);
  }

  function undo() {
    const last = annotations[annotations.length - 1];
    if (!last) return;
    setAnnotationsState(annotations.slice(0, -1));
    setRedoStack((prev) => [...prev, last]);
  }

  function redo() {
    const last = redoStack[redoStack.length - 1];
    if (!last) return;
    setRedoStack(redoStack.slice(0, -1));
    setAnnotationsState([...annotations, last]);
  }

  async function handleSubmit() {
    setSubmitting(true);
    try {
      const baked = await bakeAnnotations(screenshot, annotations);
      await onSubmit({ comment, screenshot: baked });
      toast.success(t("success"));
      onOpenChange(false);
    } catch {
      toast.error(t("error"));
    } finally {
      setSubmitting(false);
    }
  }

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent
        className="max-h-[90dvh] max-w-sm w-[calc(100vw-1rem)] overflow-y-auto sm:max-w-2xl"
        // Escape in the inline text input cancels the text, not the dialog.
        onEscapeKeyDown={(event) => {
          if (
            document.activeElement instanceof HTMLElement &&
            document.activeElement.dataset.testid === "annotation-text-input"
          ) {
            event.preventDefault();
          }
        }}
      >
        <DialogHeader>
          <DialogTitle>{t("dialog_title")}</DialogTitle>
          <DialogDescription className="hidden sm:block">
            {isCapturing ? t("capturing") : ""}
          </DialogDescription>
        </DialogHeader>

        <div className="space-y-4">
          {previewURL && (
            <div className="space-y-2">
              <AnnotationToolbar
                tool={tool}
                onToolChange={setTool}
                color={color}
                onColorChange={setColor}
                onUndo={undo}
                onRedo={redo}
                canUndo={annotations.length > 0}
                canRedo={redoStack.length > 0}
              />
              <AnnotationCanvas
                imageURL={previewURL}
                tool={tool}
                color={color}
                annotations={annotations}
                onAnnotationsChange={setAnnotations}
              />
            </div>
          )}

          <label className="block">
            <span className="text-sm font-medium">{t("comment_label")}</span>
            <Textarea
              autoFocus
              value={comment}
              onChange={(event) => setComment(event.target.value)}
              placeholder={t("comment_placeholder")}
              rows={5}
              className="mt-1"
              data-testid="feedback-comment"
            />
          </label>
        </div>

        <DialogFooter>
          <Button
            type="button"
            variant="outline"
            onClick={() => onOpenChange(false)}
            disabled={submitting}
          >
            {t("cancel")}
          </Button>
          <Button
            type="button"
            onClick={handleSubmit}
            disabled={submitting || isCapturing}
            data-testid="feedback-submit"
          >
            {submitting ? t("sending") : t("submit")}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

// bakeAnnotations rasterizes the annotation overlay onto a copy of the
// captured screenshot and returns a fresh PNG blob. Returns the original
// blob unchanged when there are no annotations or no screenshot.
async function bakeAnnotations(
  screenshot: Blob | null,
  annotations: Annotation[],
): Promise<Blob | null> {
  if (!screenshot || annotations.length === 0) return screenshot;

  const bitmap = await createImageBitmap(screenshot);
  const canvas = document.createElement("canvas");
  canvas.width = bitmap.width;
  canvas.height = bitmap.height;

  const ctx = canvas.getContext("2d");
  if (!ctx) return screenshot;

  renderAnnotations(
    ctx,
    annotations,
    { width: bitmap.width, height: bitmap.height },
    bitmap,
  );

  return new Promise<Blob | null>((resolve) => {
    canvas.toBlob((blob) => resolve(blob || screenshot), "image/png");
  });
}
