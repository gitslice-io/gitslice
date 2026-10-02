import { test } from "node:test";
import assert from "node:assert/strict";
import { applyDiscount, formatPrice } from "../src/catalog/pricing";

test("formatPrice renders cents with the currency symbol", () => {
  assert.equal(formatPrice(2400, "USD"), "$24.00");
  assert.equal(formatPrice(150000, "JPY"), "¥1500");
});

test("applyDiscount rounds to whole cents", () => {
  assert.equal(applyDiscount(999, 10), 899);
});
