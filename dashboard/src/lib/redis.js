import { createClient } from "redis";

export const REDIS_URL = process.env.REDIS_URL || "redis://localhost:6379";

// Server components run in Node, so we use the full Redis client. One short-lived
// connection per request keeps things simple and avoids leaking sockets.
export async function withRedis(fn) {
  const client = createClient({ url: REDIS_URL });
  // node-redis emits 'error' events; an unhandled one would crash the render.
  client.on("error", () => {});
  await client.connect();
  try {
    return await fn(client);
  } finally {
    try {
      await client.quit();
    } catch {
      /* connection already gone */
    }
  }
}

const emptyStats = {
  ok: false,
  totalRequests: 0,
  leaksBlocked: 0,
  presidioFailures: 0,
  budgetBlocks: 0,
  costUsd: 0,
};

export async function readStats() {
  try {
    return await withRedis(async (client) => {
      const [totalRequests, leaksBlocked, presidioFailures, budgetBlocks, cost] =
        await Promise.all([
          client.get("stats:total_requests"),
          client.get("stats:leaks_blocked"),
          client.get("stats:presidio_failures"),
          client.get("stats:budget_blocks"),
          client.get("stats:cost_usd_total"),
        ]);
      return {
        ok: true,
        totalRequests: Number(totalRequests) || 0,
        leaksBlocked: Number(leaksBlocked) || 0,
        presidioFailures: Number(presidioFailures) || 0,
        budgetBlocks: Number(budgetBlocks) || 0,
        costUsd: Number(cost) || 0,
      };
    });
  } catch (error) {
    return { ...emptyStats, error: String(error?.message ?? error) };
  }
}

export async function readAlerts(limit = 10) {
  try {
    return await withRedis(async (client) => {
      const rows = await client.lRange("alerts", 0, limit - 1);
      return rows.map(parseJson).filter(Boolean);
    });
  } catch {
    return [];
  }
}

export async function readAudit(limit = 50) {
  try {
    return await withRedis(async (client) => {
      const rows = await client.lRange("audit:log", 0, limit - 1);
      return rows.map(parseJson).filter(Boolean);
    });
  } catch {
    return [];
  }
}

export async function readBudgets() {
  return withRedis(async (client) => {
    const raw = await client.get("config:active");
    const config = raw ? parseJson(raw) || {} : {};
    const budgets = config.budgets || {};
    const teams = Object.keys(budgets);
    const month = new Date().toISOString().slice(0, 7);

    const spends = await Promise.all(
      teams.map((team) => client.get(`budget:${team}:${month}`)),
    );

    return {
      month,
      rules: config.rules ?? 0,
      roles: config.roles ?? 0,
      pricing: config.pricing || {},
      teams: teams
        .map((team, i) => ({
          team,
          cap: Number(budgets[team]) || 0,
          spent: Number(spends[i]) || 0,
        }))
        .sort((a, b) => b.spent / (b.cap || 1) - a.spent / (a.cap || 1)),
    };
  });
}

function parseJson(raw) {
  try {
    return JSON.parse(raw);
  } catch {
    return null;
  }
}
