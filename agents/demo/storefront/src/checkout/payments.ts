export type PaymentMethod = { kind: "card"; token: string } | { kind: "wallet"; provider: "apple" | "google"; token: string };

export interface CaptureResult {
  ok: boolean;
  reference?: string;
  error?: string;
}

// capturePayment charges the customer once. Payment code is protected: changes
// need a human approval before they land.
export async function capturePayment(orderId: string, amountCents: number, method: PaymentMethod): Promise<CaptureResult> {
  if (amountCents <= 0) return { ok: false, error: "nothing to charge" };
  const response = await fetch("https://payments.example.invalid/capture", {
    method: "POST",
    headers: { "content-type": "application/json", "idempotency-key": orderId },
    body: JSON.stringify({ orderId, amountCents, method }),
  });
  if (!response.ok) return { ok: false, error: `capture failed: ${response.status}` };
  const body = (await response.json()) as { reference: string };
  return { ok: true, reference: body.reference };
}
