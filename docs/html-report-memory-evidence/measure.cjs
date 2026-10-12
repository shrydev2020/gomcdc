const { chromium } = require('playwright');
const { execFileSync } = require('node:child_process');
const { pathToFileURL } = require('node:url');

const executablePath = process.env.CHROME_EXECUTABLE ||
  '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome';
const metricNames = ['JSHeapUsedSize', 'JSHeapTotalSize', 'LayoutDuration', 'RecalcStyleDuration'];

async function measure(stage, pageSession, browserSession) {
  const { processInfo } = await browserSession.send('SystemInfo.getProcessInfo');
  const rendererRSS = processInfo.filter(process => process.type === 'renderer').map(process => ({
    pid: process.id,
    rssKiB: Number(execFileSync('ps', ['-o', 'rss=', '-p', String(process.id)], {
      encoding: 'utf8',
    }).trim()),
  }));
  const { metrics } = await pageSession.send('Performance.getMetrics');
  return {
    stage,
    dom: await pageSession.send('Memory.getDOMCounters'),
    metrics: metrics.filter(metric => metricNames.includes(metric.name)),
    rendererRSS,
  };
}

async function main() {
  const [file, action] = process.argv.slice(2);
  if (!file || (action && action !== 'interact')) {
    throw new Error('Usage: measure.cjs SINGLE_HTML [interact]');
  }
  const browser = await chromium.launch({ executablePath, headless: true, args: ['--disable-extensions'] });
  try {
    const page = await browser.newPage({ viewport: { width: 1440, height: 1000 } });
    const pageSession = await page.context().newCDPSession(page);
    const browserSession = await browser.newBrowserCDPSession();
    await pageSession.send('Performance.enable');
    const results = [await measure('blank', pageSession, browserSession)];
    const start = Date.now();
    await page.goto(pathToFileURL(file).href, { waitUntil: 'load', timeout: 60000 });
    results.push({ ...await measure('loaded', pageSession, browserSession), elapsedMs: Date.now() - start });
    const counts = await page.evaluate(() => ({
      elements: document.querySelectorAll('*').length,
      closedDetails: document.querySelectorAll('details:not([open])').length,
      sourcePanels: document.querySelectorAll('.source-code').length,
      title: document.title,
    }));
    if (action === 'interact') {
      await page.locator('.package-card').first().scrollIntoViewIfNeeded();
      results.push(await measure('first-package', pageSession, browserSession));
      await page.locator('.details-block').first().evaluate(element => { element.open = true; });
      await page.locator('.details-block').first().scrollIntoViewIfNeeded();
      results.push(await measure('first-decision', pageSession, browserSession));
    }
    console.log(JSON.stringify({ browser: browser.version(), path: file, counts, results }, null, 2));
  } finally {
    await browser.close();
  }
}

main().catch(error => {
  console.error(error);
  process.exitCode = 1;
});
