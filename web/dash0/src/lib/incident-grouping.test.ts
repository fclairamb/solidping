import { describe, expect, it } from "vitest";
import {
  groupHeaderCounts,
  groupIncidentsByCheckGroup,
} from "./incident-grouping";
import type { Check, CheckGroup, IncidentDetail } from "@/api/hooks";

const rabbitmq: CheckGroup = {
  uid: "grp-rabbit",
  name: "RabbitMQ",
  slug: "rabbitmq",
  sortOrder: 1,
  checkCount: 6,
  status: "down",
  createdAt: "",
  updatedAt: "",
};

function check(uid: string, groupUid?: string): Check {
  return { uid, slug: uid, checkGroupUid: groupUid };
}

function incident(
  uid: string,
  checkUid: string,
  state: "active" | "resolved" = "active",
): IncidentDetail {
  return { uid, checkUid, state, startedAt: "2026-08-23T23:23:30Z" };
}

describe("groupIncidentsByCheckGroup", () => {
  it("buckets members of one group under a single row", () => {
    const rows = groupIncidentsByCheckGroup(
      [incident("i1", "c-nonprod"), incident("i2", "c-prod")],
      [check("c-nonprod", rabbitmq.uid), check("c-prod", rabbitmq.uid)],
      [rabbitmq],
    );

    expect(rows).toHaveLength(1);
    expect(rows[0].group?.name).toBe("RabbitMQ");
    expect(rows[0].incidents.map((i) => i.uid)).toEqual(["i1", "i2"]);
  });

  it("leaves ungrouped incidents as bare rows in their original position", () => {
    const rows = groupIncidentsByCheckGroup(
      [
        incident("i1", "c-solo"),
        incident("i2", "c-prod"),
        incident("i3", "c-other"),
        incident("i4", "c-nonprod"),
      ],
      [
        check("c-solo"),
        check("c-prod", rabbitmq.uid),
        check("c-other"),
        check("c-nonprod", rabbitmq.uid),
      ],
      [rabbitmq],
    );

    // The group takes the position of its FIRST member (i2), and the later
    // member (i4) joins it rather than opening a second header.
    expect(rows.map((r) => r.key)).toEqual(["i1", rabbitmq.uid, "i3"]);
    expect(rows[1].incidents.map((i) => i.uid)).toEqual(["i2", "i4"]);
    expect(rows[0].group).toBeUndefined();
    expect(rows[2].group).toBeUndefined();
  });

  it("counts only ACTIVE members toward the down count", () => {
    const rows = groupIncidentsByCheckGroup(
      [
        incident("i1", "c-nonprod", "active"),
        incident("i2", "c-prod", "resolved"),
      ],
      [check("c-nonprod", rabbitmq.uid), check("c-prod", rabbitmq.uid)],
      [rabbitmq],
    );

    expect(rows[0].group?.downCount).toBe(1);
    expect(rows[0].incidents).toHaveLength(2);
  });

  it("renders bare when the check's group is not in the loaded groups", () => {
    // Naming a group we cannot name would be worse than no header at all.
    const rows = groupIncidentsByCheckGroup(
      [incident("i1", "c-prod")],
      [check("c-prod", "grp-unknown")],
      [rabbitmq],
    );

    expect(rows).toHaveLength(1);
    expect(rows[0].group).toBeUndefined();
    expect(rows[0].key).toBe("i1");
  });

  it("renders bare when the check itself did not load", () => {
    // Past the checks endpoint's 100-row clamp the mapping is incomplete; the
    // incident must still appear, just without a header.
    const rows = groupIncidentsByCheckGroup(
      [incident("i1", "c-prod")],
      [],
      [rabbitmq],
    );

    expect(rows).toHaveLength(1);
    expect(rows[0].group).toBeUndefined();
  });

  it("returns nothing for an empty or missing list", () => {
    expect(groupIncidentsByCheckGroup([], [], [])).toEqual([]);
    expect(groupIncidentsByCheckGroup(undefined, [], [])).toEqual([]);
  });
});

describe("groupHeaderCounts", () => {
  it("uses the N/M form when the member count can hold it", () => {
    const rows = groupIncidentsByCheckGroup(
      [incident("i1", "c-nonprod"), incident("i2", "c-prod")],
      [check("c-nonprod", rabbitmq.uid), check("c-prod", rabbitmq.uid)],
      [rabbitmq],
    );

    expect(groupHeaderCounts(rows[0])).toEqual({ down: 2, total: 6 });
  });

  it("drops the denominator rather than overstating it", () => {
    // A group whose loaded member incidents outnumber its enabled checks (a
    // check left the group mid-outage, say). Asserting "3/2 down" would be
    // nonsense, so the honest phrasing is the bare count.
    const small: CheckGroup = { ...rabbitmq, checkCount: 2 };
    const rows = groupIncidentsByCheckGroup(
      [incident("i1", "c-a"), incident("i2", "c-b"), incident("i3", "c-c")],
      [
        check("c-a", small.uid),
        check("c-b", small.uid),
        check("c-c", small.uid),
      ],
      [small],
    );

    expect(groupHeaderCounts(rows[0])).toEqual({ down: 3 });
  });
});
