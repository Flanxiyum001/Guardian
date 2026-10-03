import { readBudgets } from "@/lib/redis";

export const dynamic = "force-dynamic";
export const revalidate = 0;

function Bar({ spent, cap }) {
  const pct = cap > 0 ? Math.min((spent / cap) * 100, 100) : 0;
  const tone =
    pct >= 100 ? "bg-red-500" : pct >= 80 ? "bg-amber-400" : "bg-emerald-500";
  return (
    <div className="mt-3 h-2 w-full overflow-hidden rounded-full bg-slate-700">
      <div className={`h-full rounded-full ${tone}`} style={{ width: `${pct}%` }} />
    </div>
  );
}

export default async function CostsPage() {
  let data;
  try {
    data = await readBudgets();
  } catch (error) {
    return (
      <div className="min-h-screen bg-slate-950 px-6 py-12 text-slate-100">
        <div className="mx-auto max-w-5xl">
          <h1 className="text-2xl font-extrabold text-emerald-400">
            Cost Tracking &amp; Budget Caps
          </h1>
          <p className="mt-4 rounded-lg border border-amber-500/40 bg-amber-500/10 px-4 py-3 text-sm text-amber-300">
            Cannot reach Redis: {String(error?.message ?? error)}
          </p>
        </div>
      </div>
    );
  }

  return (
    <div className="min-h-screen bg-slate-950 px-6 py-12 text-slate-100">
      <div className="mx-auto max-w-5xl">
        <header className="mb-10 border-b border-slate-800 pb-6">
          <h1 className="text-3xl font-extrabold tracking-tight text-emerald-400">
            💸 Cost Tracking &amp; Budget Caps
          </h1>
          <p className="mt-1 text-sm text-slate-400">
            Monthly spend per team for {data.month}. The gateway returns
            <code className="mx-1 text-amber-300">429 Too Many Requests</code>
            once a team hits its cap.
          </p>
          <p className="mt-2 text-xs text-slate-600">
            Active config: {data.rules} custom rule(s) · {data.roles} role policy(ies)
          </p>
        </header>

        {data.teams.length === 0 ? (
          <p className="text-sm text-slate-500">
            No budgets configured yet. The gateway publishes them from
            <code className="mx-1 text-slate-300">config/custom-rules.json</code>.
          </p>
        ) : (
          <ul className="space-y-4">
            {data.teams.map((row) => {
              const pct = row.cap > 0 ? Math.round((row.spent / row.cap) * 100) : 0;
              const over = row.cap > 0 && row.spent >= row.cap;
              return (
                <li
                  key={row.team}
                  className="rounded-xl border border-slate-700 bg-slate-800 p-6 shadow-lg"
                >
                  <div className="flex flex-wrap items-baseline justify-between gap-2">
                    <h2 className="text-lg font-bold text-white">{row.team}</h2>
                    <span
                      className={`text-sm font-semibold ${over ? "text-red-400" : "text-slate-300"}`}
                    >
                      ${row.spent.toFixed(4)} / ${row.cap.toFixed(2)}
                    </span>
                  </div>
                  <Bar spent={row.spent} cap={row.cap} />
                  <span className="mt-2 block text-xs text-slate-500">
                    {over
                      ? "🚫 Cap reached — further requests are blocked with 429."
                      : `${pct}% of the monthly budget used`}
                  </span>
                </li>
              );
            })}
          </ul>
        )}
      </div>
    </div>
  );
}
