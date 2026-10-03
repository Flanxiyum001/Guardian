import { readAlerts, readStats } from "@/lib/redis";
import RefreshButton from "./refresh-button";

// Counters are live; never prerender or cache them at build time.
export const dynamic = "force-dynamic";
export const revalidate = 0;

function Card({ label, value, hint, tone = "slate" }) {
  const valueTone =
    tone === "red"
      ? "text-red-500"
      : tone === "emerald"
        ? "text-emerald-400"
        : tone === "amber"
          ? "text-amber-400"
          : "text-white";
  const labelTone =
    tone === "red"
      ? "text-red-400"
      : tone === "emerald"
        ? "text-emerald-400"
        : tone === "amber"
          ? "text-amber-400"
          : "text-slate-400";

  return (
    <section className="rounded-xl border border-slate-700 bg-slate-800 p-6 shadow-lg">
      <h2 className={`text-sm font-semibold uppercase tracking-wider ${labelTone}`}>
        {label}
      </h2>
      <p className={`mt-2 text-4xl font-black ${valueTone}`}>{value}</p>
      <span className="mt-2 block text-xs text-slate-500">{hint}</span>
    </section>
  );
}

export default async function Dashboard() {
  const [stats, alerts] = await Promise.all([readStats(), readAlerts()]);
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
              Guardian Enterprise Dashboard
            </h1>
            <p className="mt-1 text-sm text-slate-400">
              Real-time local LLM privacy monitoring, token auditing and cost control
            </p>
          </div>
          <RefreshButton />
        </header>

        {!stats.ok && (
          <div className="mb-8 rounded-lg border border-amber-500/40 bg-amber-500/10 px-4 py-3 text-sm text-amber-300">
            Cannot reach Redis. Showing zeros.
            {stats.error ? ` (${stats.error})` : null}
          </div>
        )}

        <main className="grid grid-cols-1 gap-6 md:grid-cols-2">
          <Card
            label="Total Evaluated Traffic"
            value={stats.totalRequests}
            hint="Incoming API requests intercepted by the gateway"
          />
          <Card
            label="PII Leaks Blocked"
            value={stats.leaksBlocked}
            hint="Sensitive entities tokenized before leaving your network"
            tone="red"
          />
          <Card
            label="Tracked AI Spend"
            value={`$${stats.costUsd.toFixed(4)}`}
            hint="Estimated USD this month across all teams"
            tone="emerald"
          />
          <Card
            label="Policy Actions"
            value={stats.budgetBlocks + stats.presidioFailures}
            hint={`${stats.budgetBlocks} budget blocks · ${stats.presidioFailures} scan failures`}
            tone="amber"
          />
        </main>

        <section className="mt-8 rounded-xl border border-slate-700 bg-slate-800 p-6 shadow-lg">
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
            {blockRate}% of evaluated requests contained at least one masked entity
          </span>
        </section>

        <section className="mt-8">
          <h2 className="mb-3 text-sm font-semibold uppercase tracking-wider text-slate-400">
            Recent Alerts
          </h2>
          {alerts.length === 0 ? (
            <p className="text-sm text-slate-500">No alerts. All systems nominal.</p>
          ) : (
            <ul className="space-y-2">
              {alerts.map((alert, i) => (
                <li
                  key={`${alert.ts}-${i}`}
                  className="flex flex-wrap items-center gap-3 rounded-lg border border-slate-800 bg-slate-900 px-4 py-3 text-sm"
                >
                  <span
                    className={`rounded px-2 py-0.5 text-xs font-semibold uppercase ${
                      alert.level === "budget"
                        ? "bg-red-500/15 text-red-400"
                        : "bg-amber-500/15 text-amber-400"
                    }`}
                  >
                    {alert.level}
                  </span>
                  <span className="text-slate-300">{alert.message}</span>
                  <span className="ml-auto text-xs text-slate-600">
                    {new Date(alert.ts * 1000).toLocaleString()}
                  </span>
                </li>
              ))}
            </ul>
          )}
        </section>

        <footer className="mt-10 text-xs text-slate-600">
          Tokens are stored in Redis with a TTL. Original values never reach the
          upstream provider.
        </footer>
      </div>
    </div>
  );
}
