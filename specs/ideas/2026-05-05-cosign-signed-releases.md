# Cosign / Signed Container Releases

## Question

Is it worth signing SolidPing's Docker images with cosign? What does it actually buy us, and at what point does the ceremony pay off?

## What signing buys

A cosign signature binds a tag to a specific image digest, signed by an identity (a key, or via keyless OIDC — typically the GitHub Actions workflow that built the image). The value only shows up when **somebody verifies the signature**:

- **Admission policies in Kubernetes** (Kyverno, Connaisseur, Sigstore policy-controller) refuse to run unsigned or untrusted images. This is the main concrete win: a compromised registry / stolen registry creds can't ship a malicious image to a verifying cluster.
- **Tag-pinning users.** Most operators pull `:latest` or `:vX.Y` rather than digests. A signature catches a tag overwrite (registry compromise, mirror MITM, typosquat).
- **Compliance.** SLSA L3, FedRAMP, EU CRA (in flight) require signed artifacts and provenance. Relevant the day a customer asks; irrelevant otherwise.
- **Forensics via Rekor.** The transparency log lets us later prove "we never published this image" if someone claims we did.

## What it does NOT protect against

- A compromise of the build pipeline itself. If an attacker is inside our GitHub Actions, they get a valid signature too. Cosign moves trust from "the registry" to "the build identity" — it doesn't eliminate trust.
- A user who pulls and runs without verifying. No verifier = no value.
- Vulnerabilities inside the image. Signing says "this is the image we built", not "this image is safe".

## Why this is borderline for SolidPing today

SolidPing is self-hosted by individual operators. The default deployment path is `docker-compose up` against an image pulled from a public registry. Realistically:

- Almost no self-hosters run an admission controller that verifies signatures.
- The ones who do (security-conscious k8s shops) are also the ones likely to ask for it explicitly.
- We don't have a compliance-driven customer asking yet.

So the immediate user-visible benefit is small. The cost, however, is also small — see below.

## What it would take

Keyless GitHub Actions flow, roughly:

```yaml
- uses: sigstore/cosign-installer@v3
- run: cosign sign --yes ghcr.io/fclairamb/solidping@${{ steps.build.outputs.digest }}
  env:
    COSIGN_EXPERIMENTAL: "1"
```

- ~10 lines of workflow YAML in the existing release pipeline.
- No long-lived keys to manage — identity is the GitHub Actions OIDC token.
- Signatures land in the registry alongside the image; Rekor logs them publicly.
- Optional follow-ups: attach SBOM (`cosign attach sbom`), SLSA provenance attestation.

## Verification story for users

If we sign, we should document the verification command in the README so operators who care can use it:

```bash
cosign verify ghcr.io/fclairamb/solidping:vX.Y \
  --certificate-identity-regexp "https://github.com/fclairamb/solidping/.*" \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com
```

Without this in the README, signing is invisible.

## Recommendation

**Defer until one of these triggers:**

1. We publish to a wider audience (Helm chart on Artifact Hub, marketplace listing) where signed-by-default is table stakes.
2. A user or customer asks for it.
3. We adopt SLSA / publish SBOMs for other reasons — at that point cosign is the cheapest delivery mechanism and we should bundle them.

**Or: do it opportunistically** when we next touch the release workflow. The keyless flow is genuinely cheap and the Rekor entry has standalone forensic value even if no one verifies. The downside is mostly "one more thing in CI to break".

Not a P0. Not pure ceremony either. Park here until a trigger hits.

## Status

idea — not scheduled
