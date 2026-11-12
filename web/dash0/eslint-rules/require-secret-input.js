/**
 * ESLint rule: require-secret-input
 *
 * dash0 records PostHog sessions with typed values unmasked, on purpose (see
 * src/lib/analytics.ts), so `data-secret-input` (which makes <Input> and
 * <Textarea> add `ph-no-capture`) is the only thing keeping a credential out of
 * the replay. This rule flags an <Input> / <Textarea> whose `id`, `name` or
 * `data-testid` says secret, password, token, kubeconfig, webhook or private
 * but that carries no `data-secret-input`. A literal type="password" input is
 * exempt (rrweb masks it). <PasswordInput> is always safe.
 * Spec 2026-11-12-04.
 */
const SECRETISH = /(secret|password|token|kubeconfig|webhook|private)/i;
const GUARDED = new Set(["Input", "Textarea"]);
const ATTRS = new Set(["id", "name", "data-testid"]);

function inScope(filename) {
  return /(^|[\\/])src[\\/]/.test(filename);
}

/** Static text of an attribute value (string literal or template quasis). */
function staticText(value) {
  if (!value) return "";
  if (value.type === "Literal") return String(value.value);
  if (value.type === "JSXExpressionContainer") {
    const e = value.expression;
    if (e.type === "Literal") return String(e.value);
    if (e.type === "TemplateLiteral") return e.quasis.map((q) => q.value.cooked).join(" ");
  }
  return "";
}

export default {
  meta: {
    type: "problem",
    schema: [],
    messages: {
      missing:
        "Input named like a credential must carry `data-secret-input` (or be a <PasswordInput>) so the session replay never records it.",
    },
  },
  create(context) {
    if (!inScope(context.filename ?? context.getFilename())) return {};
    return {
      JSXOpeningElement(node) {
        if (node.name.type !== "JSXIdentifier" || !GUARDED.has(node.name.name)) return;
        const attrs = node.attributes.filter((a) => a.type === "JSXAttribute");
        if (attrs.some((a) => a.name.name === "data-secret-input")) return;
        // An explicit no-capture class does the same job.
        const src = context.sourceCode ?? context.getSourceCode();
        const cls = attrs.find((a) => a.name.name === "className");
        if (cls && /NO_CAPTURE_CLASS|ph-no-capture/.test(src.getText(cls))) return;
        if (node.attributes.some((a) => a.type === "JSXSpreadAttribute")) return;
        // A native type="password" input is masked by rrweb by default.
        if (attrs.some((a) => a.name.name === "type" && staticText(a.value) === "password")) return;
        const named = attrs.some(
          (a) => ATTRS.has(a.name.name) && SECRETISH.test(staticText(a.value)),
        );
        if (named) context.report({ node, messageId: "missing" });
      },
    };
  },
};
