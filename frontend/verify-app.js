const { chromium } = require("playwright-core");

(async () => {
  const browser = await chromium.launch({
    executablePath: "C:\\Program Files\\Google\\Chrome\\Application\\chrome.exe",
    headless: true,
  });
  const ctx = await browser.newContext({ viewport: { width: 1440, height: 900 } });
  await ctx.addInitScript(() => localStorage.setItem("theme", "dark"));
  const page = await ctx.newPage();
  page.setDefaultTimeout(20000);

  // ---- login ----
  await page.goto("http://localhost:3000/login", { waitUntil: "networkidle" });
  await page.locator('input[placeholder*="用户名"]').fill("admin");
  await page.locator('input[type="password"]').first().fill("LocalVerify-2026!");
  await page.locator('button[type="submit"]').first().click();
  await page.waitForURL(/\/(ai-test|inbox)/, { timeout: 20000 });
  console.log("login ok ->", page.url());

  // ---- ai-test: MOCK chat ----
  await page.goto("http://localhost:3000/ai-test", { waitUntil: "networkidle" });
  await page.waitForTimeout(1500);
  const box = page.getByPlaceholder(/输入要测试|test message/i).first();
  if (await box.count()) {
    await box.fill("Hello, what can you do?");
    await box.press("Enter");
    await page.waitForTimeout(6000);
  } else {
    console.log("WARN: chat input not found");
  }
  await page.screenshot({ path: "../verify-aitest.png" });
  console.log("ai-test shot");

  // ---- inbox ----
  await page.goto("http://localhost:3000/inbox", { waitUntil: "networkidle" });
  await page.waitForTimeout(2000);
  await page.screenshot({ path: "../verify-inbox.png" });
  console.log("inbox shot");

  // ---- admin ----
  await page.goto("http://localhost:3000/admin", { waitUntil: "networkidle" });
  await page.waitForTimeout(2000);
  await page.screenshot({ path: "../verify-admin.png" });
  console.log("admin shot");

  // ---- platforms ----
  await page.goto("http://localhost:3000/platforms", { waitUntil: "networkidle" });
  await page.waitForTimeout(2000);
  await page.screenshot({ path: "../verify-platforms.png" });
  console.log("platforms shot");

  await browser.close();
  console.log("done");
})().catch((e) => { console.error(e.message); process.exit(1); });
