const puppeteer = require("puppeteer");
const path = require("path");

(async () => {
  const dir = __dirname;
  const browser = await puppeteer.launch({
    headless: true,
    args: ["--no-sandbox", "--allow-file-access-from-files"],
  });
  const page = await browser.newPage();
  await page.goto("file://" + path.join(dir, "VISUAL-GUIDE.html"), {
    waitUntil: "load",
  });
  await page.emulateMediaType("print");
  await page.pdf({
    path: path.join(dir, "..", "Encryl-Visual-Guide.pdf"),
    format: "A4",
    printBackground: true,
    displayHeaderFooter: true,
    headerTemplate:
      '<div style="font-size:7pt;color:#7b8a99;width:100%;padding:0 16mm;">Encryl — A Visual Guide</div>',
    footerTemplate:
      '<div style="font-size:7pt;color:#7b8a99;width:100%;padding:0 16mm;text-align:right;">' +
      '<span class="pageNumber"></span> / <span class="totalPages"></span></div>',
    margin: { top: "18mm", bottom: "20mm", left: "16mm", right: "16mm" },
  });
  await browser.close();
  console.log("pdf written");
})();
