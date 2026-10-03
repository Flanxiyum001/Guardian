import { createClient } from "redis";
import RefreshButton from "./refresh-button";

// Counters are live; never prerender or cache them at build time.
export const dynamic = "force-dynamic";
export const revalidate = 0;

const REDIS_URL = process.env.REDIS_URL || "redis://localhost:6379";

async function getStats() {
  let client;
  try {
    client = createClient({ url: REDIS_URL });
    // node-redis emits 'error' events; an unhandled one would crash the page.
    client.on("error", () => {});
    await client.connect();

    const [totalRequests, leaksBlocked] = await Promise.all([
      client.get("stats:total_requests"),
      client.get("stats:leaks_blocked"),
    ]);

    return {
      ok: true,
      totalRequests: Number(totalRequests) || 0,
      leaksBlocked: Number(leaksBlocked) || 0,
    };
  } catch (error) {
    return {
      ok: false,
      totalRequests: 0,
      leaksBlocked: 0,
      error: String(error?.message ?? error),
    };
  } finally {
    try {
      await client?.quit();
    } catch {
      /* connection already gone */
    }
  }
}

export default async function AdminDashboard() {
  const stats = await getStats();
  const blockRate =
    stats.totalRequests > 0
      ? Math.round((stats.leaksBlocked / stats.totalRequests) * 100)
      : 0;

  return (
    <div className="min-h-screen bg-slate-950 font-sans text-slate-100">
      <div className="mx-auto max-w-5xl px-6 py-12">
        <header className="mb-10 flex flex-wrap items-end justify-between gap-4 border-b border-slate-800 pb-6">
          <div>
            <h1 className="text-3xl font-extrabold tracking-tight text-emerald-400">
              🛡️ Guardian Enterprise Dashboard
            </h1>
            <p className="mt-1 text-sm text-slate-400">
              Real-time local LLM privacy monitoring and token auditing gateway
            </p>
          </div>
          <RefreshButton />
        </header>

        {!stats.ok && (
          <div className="mb-8 rounded-lg border border-amber-500/40 bg-amber-500/10 px-4 py-3 text-sm text-amber-300">
            Cannot reach Redis at{" "}
            <code className="text-amber-200">{REDIS_URL}</code>. Showing zeros.
            {stats.error ? ` (${stats.error})` : null}
          </div>
        )}

        <main className="grid grid-cols-1 gap-6 md:grid-cols-2">
          <section className="rounded-xl border border-slate-700 bg-slate-800 p-6 shadow-lg">
            <h2 className="text-sm font-semibold uppercase tracking-wider text-slate-400">
              Total Evaluated Traffic
            </h2>
            <p className="mt-2 text-5xl font-black text-white">
              {stats.totalRequests}
            </p>
            <span className="mt-2 block text-xs text-slate-500">
              Incoming API endpoints intercepted natively
            </span>
          </section>

          <section className="relative overflow-hidden rounded-xl border border-red-900/50 bg-slate-800 p-6 shadow-lg">
            <div className="absolute right-0 top-0 h-24 w-24 rounded-full bg-red-500/5 blur-2xl" />
            <h2 className="text-sm font-semibold uppercase tracking-wider text-red-400">
              PII Leaks Blocked
            </h2>
            <p className="mt-2 text-5xl font-black text-red-500">
              {stats.leaksBlocked}
            </p>
            <span className="mt-2 block text-xs text-slate-500">
              Sensitive names, credentials, or keys stripped cleanly
            </span>
          </section>

          <section className="rounded-xl border border-slate-700 bg-slate-800 p-6 shadow-lg md:col-span-2">
            <h2 className="text-sm font-semibold uppercase tracking-wider text-slate-400">
              Block Rate
            </h2>
            <div className="mt-4 h-2 w-full overflow-hidden rounded-full bg-slate-700">
              <div
                className="h-full rounded-full bg-emerald-500 transition-all"
                style={{ width: `${Math.min(blockRate, 100)}%` }}
              />
            </div>
            <span className="mt-2 block text-xs text-slate-500">
              {blockRate}% of evaluated requests contained at least one masked
              entity
            </span>
          </section>
        </main>

        <footer className="mt-10 text-xs text-slate-600">
          Tokens are stored in-memory in Redis with a 1 hour TTL. Original values
          never reach the upstream provider.
        </footer>
      </div>
    </div>
  );
}
