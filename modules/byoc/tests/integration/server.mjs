// A fake Restate Cloud region grant API for the integration harness.
//
// It speaks the calls the restate provider makes for a region grant and records every request, every response and every
// event the harness posts to /__evt/<name>, in order, one line each:
//
//   REQ <operation> <body>
//   RES <operation> <status> <body>
//   EVT <name>
//
// Describe and Delete answer after SLOW_MS, so that a call which starts while another is still
// open shows up in the log as a REQ before the other's RES.
//
// Usage: node server.mjs <log file> <file that receives the port>
import fs from "node:fs";
import http from "node:http";

const [logFile, portFile] = process.argv.slice(2);
const slowMs = Number(process.env.SLOW_MS ?? 600);
const grants = new Map();
let counter = 0;

const log = (line) => fs.appendFileSync(logFile, `${line}\n`);
const live = (g) => g.status === "PENDING" || g.status === "READY";
const describe = (g) => ({
  grantId: g.grantId,
  owner: g.owner,
  ownerId: "acc_1",
  regionIds: [],
  createdAt: "2026-11-12T00:00:00.000Z",
  displayName: g.displayName,
  cloud: g.cloud,
  [g.cloud]: JSON.parse(g.target),
  status: g.status,
  ...(g.status === "PENDING" && { statusReason: g.confirming ? "CONFIRMING" : "AWAITING_CONFIRMATION" }),
  ...(live(g) && {
    grantInputs: {
      restateIssuer: "https://oidc.example.test",
      grantSubject: `grant:${g.grantId}`,
      tokenAudience: "audience",
      resourcePrefix: `rgr-${g.grantId.slice(4)}`,
    },
  }),
});

const handlers = {
  // The restate provider checks its credential with this read when it is configured.
  DescribeAccount: () => [200, { environments: [] }],
  CreateRegionGrant(body) {
    const target = JSON.stringify(body[body.cloud]);
    let grant = [...grants.values()].find((g) => live(g) && g.cloud === body.cloud && g.target === target);
    if (grant && grant.displayName !== body.displayName) return [409, { message: "the grant exists with other settings" }];
    if (!grant) {
      const grantId = `rgr_${String(++counter).padStart(10, "0")}`;
      grant = { grantId, owner: body.owner, displayName: body.displayName, cloud: body.cloud, target, status: "PENDING" };
      grants.set(grantId, grant);
    }
    return [200, describe(grant)];
  },
  ListRegionGrants: () => [200, { grants: [...grants.values()].filter(live).map(describe) }],
  DescribeRegionGrant(body) {
    const grant = grants.get(body.grantId);
    if (!grant) return [404, {}];
    if (grant.confirming && grant.status === "PENDING") grant.status = "READY";
    return [200, describe(grant)];
  },
  ConfirmRegionGrant(body) {
    const grant = grants.get(body.grantId);
    if (!grant) return [404, {}];
    if (!live(grant)) return [410, { message: "the grant ended, create a new one" }];
    // Restate Cloud signs in through the grant in the background, so the grant is READY only on a
    // later describe.
    grant.confirming = true;
    return [200, describe(grant)];
  },
  DeleteRegionGrant(body) {
    const grant = grants.get(body.grantId);
    if (!grant) return [404, {}];
    grant.status = "DELETED";
    return [200, describe(grant)];
  },
};
const slow = new Set(["DescribeRegionGrant", "DeleteRegionGrant"]);

const server = http.createServer((req, res) => {
  let raw = "";
  req.on("data", (chunk) => (raw += chunk));
  req.on("end", async () => {
    const operation = req.url.split("/").pop();
    if (req.url.startsWith("/__evt/")) {
      log(`EVT ${operation}`);
      res.end();
      return;
    }
    log(`REQ ${operation} ${raw}`);
    if (slow.has(operation)) await new Promise((resolve) => setTimeout(resolve, slowMs));
    const [status, payload] = handlers[operation]?.(raw ? JSON.parse(raw) : {}) ?? [404, {}];
    const out = JSON.stringify(payload);
    log(`RES ${operation} ${status} ${out}`);
    res.writeHead(status, { "content-type": "application/json" });
    res.end(out);
  });
});

server.listen(0, "127.0.0.1", () => fs.writeFileSync(portFile, String(server.address().port)));
