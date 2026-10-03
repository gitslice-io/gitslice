// The storefront scenario: a hundred agents with real, mostly disjoint tasks,
// plus the collisions that show what Gitslice does with them.
//
// Each task has:
//   agent, task, kind   who does what
//   intent              paths it plans to touch (shared with other agents)
//   apply(root, ctx)    edits the checkout; returns { subject, body } or null
//   fix(root)           optional second push after a review asks for changes

import { readFile, writeFile, mkdir } from "node:fs/promises";
import { dirname, join } from "node:path";

const TRANSLATIONS = {
  es: ["Tu carrito está vacío", "Añadir al carrito", "Eliminar", "{count} artículos", "Finalizar compra", "Pagar {amount}", "¡Gracias! Tu pedido está confirmado."],
  fr: ["Votre panier est vide", "Ajouter au panier", "Supprimer", "{count} articles", "Paiement", "Payer {amount}", "Merci ! Votre commande est confirmée."],
  de: ["Dein Warenkorb ist leer", "In den Warenkorb", "Entfernen", "{count} Artikel", "Kasse", "{amount} bezahlen", "Danke! Deine Bestellung ist bestätigt."],
  it: ["Il tuo carrello è vuoto", "Aggiungi al carrello", "Rimuovi", "{count} articoli", "Cassa", "Paga {amount}", "Grazie! Il tuo ordine è confermato."],
  pt: ["Seu carrinho está vazio", "Adicionar ao carrinho", "Remover", "{count} itens", "Finalizar compra", "Pagar {amount}", "Obrigado! Seu pedido está confirmado."],
  nl: ["Je winkelwagen is leeg", "In winkelwagen", "Verwijderen", "{count} artikelen", "Afrekenen", "Betaal {amount}", "Bedankt! Je bestelling is bevestigd."],
  sv: ["Din varukorg är tom", "Lägg i varukorgen", "Ta bort", "{count} artiklar", "Kassa", "Betala {amount}", "Tack! Din beställning är bekräftad."],
  pl: ["Twój koszyk jest pusty", "Dodaj do koszyka", "Usuń", "{count} produktów", "Kasa", "Zapłać {amount}", "Dziękujemy! Twoje zamówienie zostało potwierdzone."],
  tr: ["Sepetiniz boş", "Sepete ekle", "Kaldır", "{count} ürün", "Ödeme", "{amount} öde", "Teşekkürler! Siparişiniz onaylandı."],
  ja: ["カートは空です", "カートに追加", "削除", "{count}点", "お会計", "{amount}を支払う", "ありがとうございます！ご注文が確定しました。"],
  ko: ["장바구니가 비어 있습니다", "장바구니에 담기", "삭제", "{count}개 상품", "결제", "{amount} 결제하기", "감사합니다! 주문이 확정되었습니다."],
  zh: ["购物车是空的", "加入购物车", "移除", "{count} 件商品", "结账", "支付 {amount}", "谢谢！您的订单已确认。"],
  hi: ["आपका कार्ट खाली है", "कार्ट में जोड़ें", "हटाएँ", "{count} आइटम", "चेकआउट", "{amount} का भुगतान करें", "धन्यवाद! आपका ऑर्डर कन्फ़र्म हो गया है।"],
  ar: ["سلة التسوق فارغة", "أضف إلى السلة", "إزالة", "{count} منتجات", "الدفع", "ادفع {amount}", "شكرًا لك! تم تأكيد طلبك."],
  id: ["Keranjang Anda kosong", "Tambah ke keranjang", "Hapus", "{count} barang", "Checkout", "Bayar {amount}", "Terima kasih! Pesanan Anda telah dikonfirmasi."],
  vi: ["Giỏ hàng của bạn trống", "Thêm vào giỏ hàng", "Xóa", "{count} sản phẩm", "Thanh toán", "Thanh toán {amount}", "Cảm ơn bạn! Đơn hàng của bạn đã được xác nhận."],
  uk: ["Ваш кошик порожній", "Додати до кошика", "Видалити", "{count} товарів", "Оформлення замовлення", "Сплатити {amount}", "Дякуємо! Ваше замовлення підтверджено."],
  cs: ["Váš košík je prázdný", "Přidat do košíku", "Odebrat", "{count} položek", "Pokladna", "Zaplatit {amount}", "Děkujeme! Vaše objednávka je potvrzena."],
  da: ["Din kurv er tom", "Læg i kurv", "Fjern", "{count} varer", "Kassen", "Betal {amount}", "Tak! Din ordre er bekræftet."],
  fi: ["Ostoskorisi on tyhjä", "Lisää ostoskoriin", "Poista", "{count} tuotetta", "Kassa", "Maksa {amount}", "Kiitos! Tilauksesi on vahvistettu."],
};
// Locales the store does not have yet: these agents create the file.
const NEW_LOCALES = {
  he: ["בית", "חיפוש", "עגלה", "העגלה שלך ריקה", "הוספה לעגלה", "הסרה", "{count} פריטים", "קופה", "תשלום {amount}", "תודה! ההזמנה שלך אושרה."],
  th: ["หน้าแรก", "ค้นหา", "ตะกร้า", "ตะกร้าของคุณว่างเปล่า", "เพิ่มลงตะกร้า", "ลบ", "{count} รายการ", "ชำระเงิน", "ชำระ {amount}", "ขอบคุณ! คำสั่งซื้อของคุณได้รับการยืนยันแล้ว"],
  ro: ["Acasă", "Căutare", "Coș", "Coșul tău este gol", "Adaugă în coș", "Elimină", "{count} produse", "Finalizare comandă", "Plătește {amount}", "Mulțumim! Comanda ta a fost confirmată."],
  hu: ["Kezdőlap", "Keresés", "Kosár", "A kosarad üres", "Kosárba", "Eltávolítás", "{count} termék", "Pénztár", "{amount} fizetése", "Köszönjük! A rendelésedet visszaigazoltuk."],
  el: ["Αρχική", "Αναζήτηση", "Καλάθι", "Το καλάθι σας είναι άδειο", "Προσθήκη στο καλάθι", "Αφαίρεση", "{count} προϊόντα", "Ολοκλήρωση αγοράς", "Πληρωμή {amount}", "Ευχαριστούμε! Η παραγγελία σας επιβεβαιώθηκε."],
  nb: ["Hjem", "Søk", "Handlekurv", "Handlekurven din er tom", "Legg i handlekurven", "Fjern", "{count} varer", "Kasse", "Betal {amount}", "Takk! Bestillingen din er bekreftet."],
  ms: ["Laman Utama", "Cari", "Troli", "Troli anda kosong", "Tambah ke troli", "Buang", "{count} item", "Pembayaran", "Bayar {amount}", "Terima kasih! Pesanan anda telah disahkan."],
  bn: ["হোম", "অনুসন্ধান", "কার্ট", "আপনার কার্ট খালি", "কার্টে যোগ করুন", "সরান", "{count}টি আইটেম", "চেকআউট", "{amount} পরিশোধ করুন", "ধন্যবাদ! আপনার অর্ডার নিশ্চিত হয়েছে।"],
};
const LANGUAGES = {
  es: "Spanish", fr: "French", de: "German", it: "Italian", pt: "Portuguese", nl: "Dutch", sv: "Swedish", pl: "Polish", tr: "Turkish",
  ja: "Japanese", ko: "Korean", zh: "Chinese", hi: "Hindi", ar: "Arabic", id: "Indonesian", vi: "Vietnamese", uk: "Ukrainian",
  cs: "Czech", da: "Danish", fi: "Finnish", he: "Hebrew", th: "Thai", ro: "Romanian", hu: "Hungarian", el: "Greek",
  nb: "Norwegian", ms: "Malay", bn: "Bengali",
};
const KEYS = ["cart.empty", "cart.add", "cart.remove", "cart.items", "checkout.title", "checkout.pay", "checkout.success"];

const COMPONENTS = {
  Button: "the button's text", IconButton: "the icon's meaning", Card: "the card title", Header: "the site title", Footer: "the footer text",
  NavBar: "Main navigation", SearchBox: "Search products", ProductTile: "the product name", ProductGallery: "Product images",
  Price: "the price", Rating: "the star rating", Badge: "the badge text", CartDrawer: "Shopping cart", CartLineItem: "the cart line",
  QuantityPicker: "Quantity", CheckoutForm: "Checkout", AddressForm: "Shipping address", Toast: "Notification", Modal: "Dialog",
  Pagination: "Pagination",
};

const PRODUCTS = [
  ["socks-merino", "Merino Socks", 1400, ["apparel", "wool"]], ["scarf-alpaca", "Alpaca Scarf", 4800, ["apparel", "winter"]],
  ["lamp-desk", "Desk Lamp", 6900, ["home", "lighting"]], ["candle-cedar", "Cedar Candle", 2200, ["home"]],
  ["backpack-day", "Day Backpack", 8900, ["bags", "outdoor"]], ["wallet-leather", "Leather Wallet", 4500, ["accessories"]],
  ["pen-brass", "Brass Pen", 3200, ["stationery"]], ["planter-clay", "Clay Planter", 2600, ["home", "garden"]],
  ["blanket-wool", "Wool Blanket", 11900, ["home", "winter"]], ["apron-linen", "Linen Apron", 3400, ["kitchen"]],
  ["board-cutting", "Cutting Board", 3900, ["kitchen"]], ["glass-set", "Glass Set", 4200, ["kitchen"]],
  ["umbrella-compact", "Compact Umbrella", 2900, ["accessories", "rain"]], ["beanie-knit", "Knit Beanie", 2400, ["apparel", "winter"]],
  ["journal-linen", "Linen Journal", 2800, ["stationery"]], ["coaster-cork", "Cork Coasters", 1500, ["home"]],
  ["towel-cotton", "Cotton Towel", 3600, ["home", "bath"]], ["soap-olive", "Olive Soap", 900, ["bath"]],
  ["poster-print", "Poster Print", 2500, ["art"]], ["keychain-steel", "Steel Keychain", 1100, ["accessories"]],
];

const TESTS = [
  ["search", "search", `import { search } from "../src/catalog/search";

const products = [
  { sku: "a", name: "Wool Cap", priceCents: 2200, currency: "USD", tags: ["winter"], stock: 1 },
  { sku: "b", name: "Canvas Tote", priceCents: 1600, currency: "USD", tags: ["bags"], stock: 1 },
];

test("search matches names and tags case-insensitively", () => {
  assert.deepEqual(search(products, "WOOL").map((p) => p.sku), ["a"]);
  assert.deepEqual(search(products, "bags").map((p) => p.sku), ["b"]);
});

test("every term must match", () => {
  assert.deepEqual(search(products, "wool bags"), []);
});
`],
  ["cart", "cart", `import { Cart } from "../src/cart/cart";

const tee = { sku: "tee", name: "Tee", priceCents: 2400, currency: "USD", tags: [], stock: 3 };

test("adding the same product twice merges the lines", () => {
  const cart = new Cart();
  cart.add(tee);
  cart.add(tee, 2);
  assert.equal(cart.items()[0].quantity, 3);
});

test("quantity must be positive", () => {
  assert.throws(() => new Cart().add(tee, 0));
});
`],
  ["totals", "totals", `import { shipping, tax } from "../src/cart/totals";

test("orders over the threshold ship free", () => {
  assert.equal(shipping(10000), 0);
});

test("tax rounds to whole cents", () => {
  assert.equal(Number.isInteger(tax(1999)), true);
});
`],
  ["catalog", "catalog", `import { inStock, loadCatalog } from "../src/catalog/catalog";

test("loadCatalog rejects duplicate SKUs", () => {
  const p = { sku: "x", name: "X", priceCents: 1, currency: "USD", tags: [], stock: 1 };
  assert.throws(() => loadCatalog([p, p]));
});

test("inStock skips sold-out products", () => {
  const catalog = loadCatalog([{ sku: "x", name: "X", priceCents: 1, currency: "USD", tags: [], stock: 0 }]);
  assert.deepEqual(inStock(catalog), []);
});
`],
  ["i18n", "i18n", `import { t } from "../src/i18n/i18n";

test("t falls back to English and fills variables", () => {
  assert.equal(t({}, { "cart.items": "{count} items" }, "cart.items", { count: 3 }), "3 items");
});

test("t keeps unknown variables visible", () => {
  assert.equal(t({ hi: "Hi {name}" }, {}, "hi"), "Hi {name}");
});
`],
  ["discount", "pricing", `import { applyDiscount } from "../src/catalog/pricing";

test("a 100% discount is free", () => {
  assert.equal(applyDiscount(5000, 100), 0);
});

test("discounts outside 0–100 are rejected", () => {
  assert.throws(() => applyDiscount(5000, 120));
});
`],
];

const DOCS = {
  architecture: ["src/catalog/catalog.ts", "src/cart/cart.ts", "src/checkout/checkout.ts"],
  catalog: ["src/catalog/catalog.ts", "src/catalog/search.ts", "src/catalog/pricing.ts"],
  cart: ["src/cart/cart.ts", "src/cart/totals.ts"],
  checkout: ["src/checkout/checkout.ts"],
  i18n: ["src/i18n/i18n.ts", "src/i18n/messages.en.json"],
  ui: ["src/ui/Button.ts", "src/ui/ProductTile.ts"],
  testing: ["test/pricing.test.ts"],
  contributing: ["README.md"],
  deployment: ["package.json", "README.md"],
  faq: ["README.md", "src/cart/totals.ts", "src/checkout/checkout.ts"],
};

// The seed's products, which have no product page copy yet.
const COPY = ["bottle-steel", "cap-wool", "hoodie-zip", "mug-stoneware", "notebook-dot", "sticker-pack", "tee-classic", "tote-canvas"];

export function storefrontTasks(prefix) {
  const p = (path) => `${prefix}/${path}`;
  const tasks = [];

  for (const [code, values] of Object.entries(TRANSLATIONS)) {
    const file = `src/i18n/messages.${code}.json`;
    tasks.push({
      agent: `translator-${code}`,
      kind: "i18n",
      task: `Translate the cart and checkout strings into ${LANGUAGES[code]}`,
      intent: [p(file)],
      apply: async (root) => {
        const path = join(root, p(file));
        const messages = JSON.parse(await readFile(path, "utf8"));
        const missing = KEYS.filter((k) => !(k in messages));
        if (missing.length === 0) return null;
        KEYS.forEach((k, i) => (messages[k] ??= values[i]));
        await writeFile(path, JSON.stringify(messages, null, 2) + "\n");
        return {
          subject: `i18n(${code}): translate cart and checkout strings`,
          body: `${LANGUAGES[code]} shoppers saw English fallbacks in the cart and at checkout. I added the ${missing.length} missing keys and kept the {count} and {amount} placeholders.`,
        };
      },
    });
  }

  for (const [code, values] of Object.entries(NEW_LOCALES)) {
    const file = `src/i18n/messages.${code}.json`;
    tasks.push({
      agent: `translator-${code}`,
      kind: "i18n",
      task: `Add a ${LANGUAGES[code]} locale`,
      intent: [p(file)],
      apply: async (root) => {
        const path = join(root, p(file));
        const exists = await readFile(path, "utf8").then(() => true, () => false);
        if (exists) return null;
        const keys = ["nav.home", "nav.search", "nav.cart", ...KEYS];
        await writeFile(path, JSON.stringify(Object.fromEntries(keys.map((k, i) => [k, values[i]])), null, 2) + "\n");
        return {
          subject: `i18n(${code}): add a ${LANGUAGES[code]} locale`,
          body: `The store had no ${LANGUAGES[code]} strings. I added all ${keys.length} keys from messages.en.json and kept the {count} and {amount} placeholders.`,
        };
      },
    });
  }

  for (const [name, label] of Object.entries(COMPONENTS)) {
    const file = `src/ui/${name}.ts`;
    tasks.push({
      agent: `a11y-${name.toLowerCase()}`,
      kind: "a11y",
      task: `Give ${name} an accessible name for screen readers`,
      intent: [p(file)],
      apply: async (root) => {
        const path = join(root, p(file));
        let src = await readFile(path, "utf8");
        if (src.includes("aria-label")) return null;
        const fixed = label.startsWith("the ") ? null : label;
        src = src.replace(/return `<(\w+) class="([^"]+)">\$\{(.+)\}<\/(\w+)>`;/, (_m, tag, cls, inner, close) => {
          const aria = fixed ? JSON.stringify(fixed).slice(1, -1) : `\${${inner}}`;
          return `return \`<${tag} class="${cls}" aria-label="${aria}">\${${inner}}</${close}>\`;`;
        });
        src = src.replace(`export function ${name}(`, `/** Renders ${name} with an accessible name (${label}). */\nexport function ${name}(`);
        await writeFile(path, src);
        return {
          subject: `ui(${name}): add an accessible name`,
          body: `Screen readers announced ${name} without a label. I added aria-label from ${label} and documented it.`,
        };
      },
    });
  }

  for (const [sku, name, cents, tags] of PRODUCTS) {
    const file = `src/catalog/products/${sku}.json`;
    tasks.push({
      agent: `catalog-${sku.split("-")[0]}`,
      kind: "catalog",
      task: `Add ${name} to the catalog`,
      intent: [p(file)],
      apply: async (root) => {
        const path = join(root, p(file));
        await mkdir(dirname(path), { recursive: true });
        await writeFile(path, JSON.stringify({ sku, name, priceCents: cents, currency: "USD", tags, stock: 40 }, null, 2) + "\n");
        return { subject: `catalog: add ${name}`, body: `Merchandising asked for ${name} at $${(cents / 100).toFixed(2)}. I added it with ${tags.join(", ")} tags and 40 units in stock.` };
      },
    });
  }

  for (const [slug, module, body] of TESTS) {
    const file = `test/${slug}.test.ts`;
    tasks.push({
      agent: `tester-${slug}`,
      kind: "tests",
      task: `Write unit tests for ${module}`,
      intent: [p(file)],
      apply: async (root) => {
        const path = join(root, p(file));
        await writeFile(path, `import { test } from "node:test";\nimport assert from "node:assert/strict";\n${body}`);
        return { subject: `test: cover ${module}`, body: `${module} had no tests. I covered the main path and the error case.` };
      },
    });
  }

  for (const [slug, sources] of Object.entries(DOCS)) {
    const file = `docs/${slug}.md`;
    tasks.push({
      agent: `docs-${slug}`,
      kind: "docs",
      model: true,
      task: `Write the ${slug} documentation page from the source`,
      intent: [p(file)],
      apply: async (root, ctx) => {
        const path = join(root, p(file));
        const current = await readFile(path, "utf8");
        if (!current.includes("To be written.")) return null;
        const code = (await Promise.all(sources.map(async (s) => `--- ${s}\n${await readFile(join(root, p(s)), "utf8")}`))).join("\n");
        const title = current.split("\n")[0];
        const text = await ctx.assist(
          "You write concise developer documentation in Markdown. Reply with the page body only: no front matter, no title line, under 220 words, with at most one short code example.",
          `Write the body of the page "${title.replace(/^# /, "")}" for a small TypeScript storefront, based on this source:\n\n${code}`,
        );
        await writeFile(path, `${title}\n\n${text.trim()}\n`);
        return { subject: `docs: write the ${slug} page`, body: `The ${slug} page was a stub. I drafted it from ${sources.join(", ")} with gpt-oss-120b on Workers AI.` };
      },
    });
  }

  for (const sku of COPY) {
    const file = `src/catalog/copy/${sku}.md`;
    tasks.push({
      agent: `copy-${sku.split("-")[0]}`,
      kind: "copy",
      model: true,
      task: `Write the product page copy for ${sku}`,
      intent: [p(file)],
      apply: async (root, ctx) => {
        const path = join(root, p(file));
        const exists = await readFile(path, "utf8").then(() => true, () => false);
        if (exists) return null;
        const product = JSON.parse(await readFile(join(root, p(`src/catalog/products/${sku}.json`)), "utf8"));
        const text = await ctx.assist(
          "You write product page copy for a small online store. Reply with Markdown only: one italic tagline, one paragraph under 60 words, then three short bullet points. No title, no price, no claims the product data does not support.",
          `Product data:\n${JSON.stringify(product, null, 2)}`,
        );
        await mkdir(dirname(path), { recursive: true });
        await writeFile(path, `# ${product.name}\n\n${text.trim()}\n`);
        return { subject: `catalog: product copy for ${product.name}`, body: `${product.name} had no product page copy. I wrote it from the product data with gpt-oss-120b on Workers AI.` };
      },
    });
  }

  // Same file, different functions: the second to land is merged line by line.
  tasks.push({
    agent: "perf-subtotal",
    kind: "merge",
    task: "Compute the cart subtotal with a single reduce",
    intent: [p("src/cart/totals.ts")],
    apply: async (root) =>
      edit(root, p("src/cart/totals.ts"), (s) =>
        s.replace(
          "  let total = 0;\n  for (const line of lines) {\n    total += line.unitCents * line.quantity;\n  }\n  return total;",
          "  return lines.reduce((sum, line) => sum + line.unitCents * line.quantity, 0);",
        ),
      ).then((changed) => changed && { subject: "cart: compute the subtotal with reduce", body: "The loop was fine but noisy; reduce states the intent directly." }),
  });
  tasks.push({
    agent: "pricing-shipping",
    kind: "merge",
    task: "Raise the free-shipping threshold to $75",
    intent: [p("src/cart/totals.ts")],
    apply: async (root) =>
      edit(root, p("src/cart/totals.ts"), (s) => s.replace("if (subtotalCents >= 5000) return 0;", "if (subtotalCents >= 7500) return 0;")).then(
        (changed) => changed && { subject: "cart: free shipping from $75", body: "Finance moved the free-shipping threshold from $50 to $75 for Q4." },
      ),
  });
  tasks.push({
    agent: "docs-readme",
    kind: "merge",
    task: "Describe how to add a locale in the README",
    intent: [p("README.md")],
    apply: async (root) =>
      edit(root, p("README.md"), (s) => s + "\n## Adding a locale\n\nCopy `src/i18n/messages.en.json` to `messages.<code>.json` and translate the values. Keep `{placeholders}` as they are.\n").then(
        (changed) => changed && { subject: "docs: explain how to add a locale", body: "Translators kept asking where strings live." },
      ),
  });
  tasks.push({
    agent: "docs-intro",
    kind: "merge",
    task: "Tighten the README introduction",
    intent: [p("README.md")],
    apply: async (root) =>
      edit(root, p("README.md"), (s) =>
        s.replace(
          "A small TypeScript storefront used to demo many agents working on one\ncodebase at once with Gitslice and Cloudflare Artifacts.",
          "A small TypeScript storefront that many agents change at the same time,\nthrough Gitslice and Cloudflare Artifacts.",
        ),
      ).then((changed) => changed && { subject: "docs: tighten the README introduction", body: "Shorter first sentence." }),
  });

  // Same line: one lands, the other gets a conflict signal and reworks.
  tasks.push({
    agent: "tax-2026",
    kind: "conflict",
    task: "Apply the 2026 sales tax rate of 8.25%",
    intent: [p("src/cart/totals.ts")],
    apply: async (root) =>
      edit(root, p("src/cart/totals.ts"), (s) => s.replace(/export const TAX_RATE = [0-9.]+;/, "export const TAX_RATE = 0.0825;")).then(
        (changed) => changed && { subject: "cart: 2026 sales tax rate", body: "The 2026 schedule sets the default rate to 8.25%." },
      ),
  });
  tasks.push({
    agent: "tax-region",
    kind: "conflict",
    task: "Use the new region's 8.75% tax rate",
    intent: [p("src/cart/totals.ts")],
    // Forks with everyone else, pushes once tax-2026 has landed: a sure
    // same-line collision.
    pushAfter: "tax-2026",
    apply: async (root) => {
      const file = p("src/cart/totals.ts");
      const rework = (await readFile(join(root, file), "utf8")).includes("TAX_RATE = 0.0825;");
      const changed = await edit(root, file, (s) =>
        rework
          ? // After the conflict: keep the 2026 default, add the region's rate.
            s
              .replace(
                "export const TAX_RATE = 0.0825;",
                "export const TAX_RATE = 0.0825;\n\n// The new region charges more; checkout passes region for its addresses.\nexport const REGION_TAX_RATE = 0.0875;",
              )
              .replace(
                "export function tax(subtotalCents: number): number {\n  return Math.round(subtotalCents * TAX_RATE);\n}",
                "export function tax(subtotalCents: number, region = false): number {\n  return Math.round(subtotalCents * (region ? REGION_TAX_RATE : TAX_RATE));\n}",
              )
          : s.replace(/export const TAX_RATE = [0-9.]+;/, "export const TAX_RATE = 0.0875;"),
      );
      if (!changed) return null;
      return rework
        ? {
            subject: "cart: tax rate for the new region",
            body: "Reworked after a conflict with tax-2026: kept its 8.25% default and added the region's 8.75% rate, which tax() applies when checkout passes region.",
          }
        : { subject: "cart: tax rate for the new region", body: "Launching in a region whose sales tax is 8.75%." };
    },
  });

  // Protected path: the review agent escalates to a human.
  tasks.push({
    agent: "payments-retry",
    kind: "escalate",
    task: "Retry payment capture on 503 with backoff",
    intent: [p("src/checkout/payments.ts")],
    apply: async (root) =>
      edit(root, p("src/checkout/payments.ts"), (s) =>
        s.replace(
          `  const response = await fetch("https://payments.example.invalid/capture", {
    method: "POST",
    headers: { "content-type": "application/json", "idempotency-key": orderId },
    body: JSON.stringify({ orderId, amountCents, method }),
  });`,
          `  let response = await capture(orderId, amountCents, method);
  // The idempotency key makes retries safe: the processor charges once.
  for (let attempt = 1; response.status === 503 && attempt <= 3; attempt++) {
    await new Promise((resolve) => setTimeout(resolve, 200 * 2 ** attempt));
    response = await capture(orderId, amountCents, method);
  }`,
        ) +
          `
function capture(orderId: string, amountCents: number, method: PaymentMethod): Promise<Response> {
  return fetch("https://payments.example.invalid/capture", {
    method: "POST",
    headers: { "content-type": "application/json", "idempotency-key": orderId },
    body: JSON.stringify({ orderId, amountCents, method }),
  });
}
`,
      ).then((changed) => changed && { subject: "payments: retry capture on 503", body: "The processor sheds load with 503s at peak; three idempotent retries with backoff recover most captures." }),
  });

  // A broken push the review agent rejects, then the fix.
  tasks.push({
    agent: "translator-ga",
    kind: "review",
    task: "Add an Irish locale",
    intent: [p("src/i18n/messages.ga.json")],
    apply: async (root) => {
      await writeFile(join(root, p("src/i18n/messages.ga.json")), '{\n  "nav.home": "Baile",\n  "nav.search": "Cuardaigh",\n  "nav.cart": "Cairt",\n}\n');
      return { subject: "i18n(ga): add an Irish locale", body: "Started the Irish locale with the navigation strings." };
    },
    fix: async (root) => {
      await writeFile(join(root, p("src/i18n/messages.ga.json")), '{\n  "nav.home": "Baile",\n  "nav.search": "Cuardaigh",\n  "nav.cart": "Cairt"\n}\n');
      return { subject: "i18n(ga): fix the trailing comma", body: "The review agent pointed out invalid JSON; removed the trailing comma." };
    },
  });

  // Two agents push invalid changes and then go quiet (stall). The review
  // agent rejects them, and after a few seconds a fixer agent forks each
  // one's repository and repairs it.
  tasks.push({
    agent: "translator-gd",
    kind: "fixer",
    stall: true,
    task: "Add a Scottish Gaelic locale",
    intent: [p("src/i18n/messages.gd.json")],
    apply: async (root) => {
      await writeFile(join(root, p("src/i18n/messages.gd.json")), '{\n  "nav.home": "Dachaigh",\n  "nav.search": "Lorg"\n  "nav.cart": "Cairt",\n}\n');
      return { subject: "i18n(gd): add a Scottish Gaelic locale", body: "Started the Scottish Gaelic locale with the navigation strings." };
    },
  });
  tasks.push({
    agent: "catalog-teapot",
    kind: "fixer",
    stall: true,
    task: "Add the Cast Iron Teapot to the catalog",
    intent: [p("src/catalog/products/teapot-cast.json")],
    apply: async (root) => {
      const path = join(root, p("src/catalog/products/teapot-cast.json"));
      await mkdir(dirname(path), { recursive: true });
      await writeFile(path, '{\n  "sku": "teapot-cast",\n  "name": "Cast Iron Teapot",\n  "priceCents": "5400",\n  "tags": ["kitchen", "tea"],\n  "stock": 12\n}\n');
      return { subject: "catalog: add the Cast Iron Teapot", body: "Merchandising asked for the teapot at $54.00 in the kitchen range, 12 in stock." };
    },
  });

  return tasks;
}

async function edit(root, rel, fn) {
  const path = join(root, rel);
  const before = await readFile(path, "utf8");
  const after = fn(before);
  if (after === before) return false;
  await writeFile(path, after);
  return true;
}
