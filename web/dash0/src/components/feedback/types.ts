// Annotation types reserved for a future spec — not used in v1 (the dialog
// posts the screenshot as captured, no overlay tools yet). Defining the
// shape now keeps the API contract with the backend forward-compatible.

export type AnnotationKind = "rect" | "arrow" | "text";

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

export interface ArrowAnnotation extends BaseAnnotation {
  type: "arrow";
  x2: number;
  y2: number;
}

export interface TextAnnotation extends BaseAnnotation {
  type: "text";
  text: string;
}

export type Annotation = RectAnnotation | ArrowAnnotation | TextAnnotation;
