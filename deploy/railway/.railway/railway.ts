// SolidPing on Railway: one service from the published image, SQLite on a volume.
// Docs: https://solidping.io/docs/installation/railway
//
// Apply from deploy/railway/:
//   npm install railway && railway config plan && railway config apply
import { defineRailway, image, project, service, volume } from "railway/iac";

export default defineRailway(() => {
  const data = volume("solidping-data");

  const solidping = service("solidping", {
    source: image("ghcr.io/fclairamb/solidping:0.38"),
    env: {
      // Railway mounts volumes owned by root and the image runs as the
      // distroless nonroot user, which then cannot write the SQLite file.
      RAILWAY_RUN_UID: "0",
      // Resolved by Railway at deploy time. The domain itself is created with
      // `railway domain`, generated domains are not part of this file.
      SP_BASE_URL: "https://${{RAILWAY_PUBLIC_DOMAIN}}",
    },
    volumeMounts: { "/data": data },
    healthcheck: "/api/mgmt/health",
    healthcheckTimeout: 60,
  });

  return project("solidping", { resources: [data, solidping] });
});
