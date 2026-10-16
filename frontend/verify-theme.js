const { chromium } = require("playwright-core");

(async () => {
  const browser = await chromium.launch({
    executablePath: "C:\\Program Files\\Google\\Chrome\\Application\\chrome.exe",
    headless: true,
  });
  const ctx = await browser.newContext({ viewport: { width: 1440, height: 900 } });

  // Light login
  let page = await ctx.newPage();
  await page.goto("http://localhost:3000/login", { waitUntil: "networkidle" });
  await page.screenshot({ path: "../shot-login-light.png" });
  await page.close();

  // Dark login
  const ctxDark = await browser.newContext({ viewport: { width: 1440, height: 900 } });
  await ctxDark.addInitScript(() => localStorage.setItem("theme", "dark"));
  page = await ctxDark.newPage();
  await page.goto("http://localhost:3000/login", { waitUntil: "networkidle" });
  await page.screenshot({ path: "../shot-login-dark.png" });

  // Dark widget (public chat UI)
  await page.goto("http://localhost:3000/widget", { waitUntil: "networkidle" });
  await page.screenshot({ path: "../shot-widget-dark.png" });
  await page.close();

  await browser.close();
  console.log("done");
})().catch((e) => { console.error(e); process.exit(1); });
