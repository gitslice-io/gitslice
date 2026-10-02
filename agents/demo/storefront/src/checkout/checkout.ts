import { Cart } from "../cart/cart";
import { total } from "../cart/totals";
import { capturePayment, type PaymentMethod } from "./payments";

export interface Order {
  id: string;
  totalCents: number;
  status: "pending" | "paid" | "failed";
}

export async function checkout(cart: Cart, method: PaymentMethod): Promise<Order> {
  if (cart.isEmpty()) throw new Error("cart is empty");
  const order: Order = { id: crypto.randomUUID(), totalCents: total(cart.items()), status: "pending" };
  const result = await capturePayment(order.id, order.totalCents, method);
  order.status = result.ok ? "paid" : "failed";
  return order;
}
