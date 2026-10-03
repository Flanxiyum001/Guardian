import { NextResponse } from "next/server";
import { withRedis } from "@/lib/redis";

// POST /api/reveal  { tokens: ["[MASKED_…]"] }  with header x-audit-key
//
// Only an authorized security officer (holding AUDIT_REVEAL_KEY) can pull the
// original values back out of Redis. The audit table itself never exposes them.
export async function POST(request) {
  const expected = process.env.AUDIT_REVEAL_KEY;
  if (!expected) {
    return NextResponse.json(
      { error: "reveal_disabled", message: "Set AUDIT_REVEAL_KEY to enable reveal." },
      { status: 503 },
    );
  }

  const provided = request.headers.get("x-audit-key") || "";
  if (provided !== expected) {
    return NextResponse.json({ error: "unauthorized" }, { status: 401 });
  }

  let payload;
  try {
    payload = await request.json();
  } catch {
    return NextResponse.json({ error: "invalid_json" }, { status: 400 });
  }

  const tokens = Array.isArray(payload?.tokens) ? payload.tokens : [];
  if (tokens.length === 0) {
    return NextResponse.json({ values: {} });
  }
  if (tokens.length > 50) {
    return NextResponse.json({ error: "too_many_tokens" }, { status: 400 });
  }
  if (!tokens.every((t) => typeof t === "string")) {
    return NextResponse.json({ error: "invalid_tokens" }, { status: 400 });
  }

  const values = await withRedis((client) =>
    client.mGet(tokens.map((token) => `tok:${token}`)),
  );

  const result = {};
  tokens.forEach((token, i) => {
    result[token] = values[i] ?? null; // null = expired or never stored
  });
  return NextResponse.json({ values: result });
}
