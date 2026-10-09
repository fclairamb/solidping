// Annotation shapes drawn over the bug report screenshot. Coordinates are
// normalized to [0, 1] relative to the captured image, so they stay correct
// when the preview is resized between drawing and submit. The overlay is
// baked into the outgoing PNG client-side (blur included), so redacted pixels
// never leave the browser.

export type AnnotationKind =
  | "rect"
  | "arrow"
  | "text"
  | "pen"
  | "highlight"
  | "blur";

export interface BaseAnnotation {
  type: AnnotationKind;
  // Coordinates in [0, 1] relative to the captured image.
  x: number;
  y: number;
  color: string;
}

export interface RectAnnotation extends BaseAnnotation {
  type: "rect";
  w: number;
  h: number;
}

// HighlightAnnotation is a semi-transparent filled rectangle.
export interface HighlightAnnotation extends BaseAnnotation {
  type: "highlight";
  w: number;
  h: number;
}

// BlurAnnotation pixelates the region it covers.
export interface BlurAnnotation extends BaseAnnotation {
  type: "blur";
  w: number;
  h: number;
}

export interface ArrowAnnotation extends BaseAnnotation {
  type: "arrow";
  x2: number;
  y2: number;
}

export interface TextAnnotation extends BaseAnnotation {
  type: "text";
  text: string;
}

// PenAnnotation is a freehand stroke. x/y duplicate the first point.
export interface PenAnnotation extends BaseAnnotation {
  type: "pen";
  points: { x: number; y: number }[];
}

export type Annotation =
  | RectAnnotation
  | ArrowAnnotation
  | TextAnnotation
  | PenAnnotation
  | HighlightAnnotation
  | BlurAnnotation;

export const ANNOTATION_COLORS = ["#ef4444", "#eab308", "#22c55e", "#3b82f6"];
