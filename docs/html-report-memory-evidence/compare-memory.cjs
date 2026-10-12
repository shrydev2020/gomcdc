const { chromium } = require('playwright');
const fs = require('node:fs');
const path = require('node:path');
const { execFileSync } = require('node:child_process');
const { pathToFileURL } = require('node:url');

const executablePath = process.env.CHROME_EXECUTABLE ||
  '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome';
const metricNames = ['JSHeapUsedSize', 'LayoutDuration', 'RecalcStyleDuration'];

async function sample(stage, pageSession, browserSession) {
  const { processInfo } = await browserSession.send('SystemInfo.getProcessInfo');
  const renderers = processInfo.filter(process => process.type === 'renderer');
  if (renderers.length === 0) throw new Error(`No renderer process at ${stage}`);
  const rss = renderers.map(process => ({
    pid: process.id,
    rssKiB: Number(execFileSync('ps', ['-o', 'rss=', '-p', String(process.id)], {
      encoding: 'utf8',
    }).trim()),
  }));
  const { metrics } = await pageSession.send('Performance.getMetrics');
  return {
    stage,
    dom: await pageSession.send('Memory.getDOMCounters'),
    rss,
    maxRendererMiB: Math.max(...rss.map(process => process.rssKiB)) / 1024,
    metrics: metrics.filter(metric => metricNames.includes(metric.name)),
  };
}

function evaluationPages(entry) {
  const html = fs.readFileSync(entry, 'utf8');
  const match = html.match(/href="(report-[a-f0-9]+)\/style\.css"/);
  if (!match) throw new Error(`Generation stylesheet link missing in ${entry}`);
  const directory = path.join(path.dirname(entry), match[1]);
  return fs.readdirSync(directory)
    .filter(name => name.includes('-evaluations-cols-') && name.endsWith('.html'))
    .sort()
    .slice(0, 100)
    .map(name => path.join(directory, name));
}

async function measureCase(label, file, run) {
  const browser = await chromium.launch({
    executablePath,
    headless: true,
    ignoreDefaultArgs: ['--disable-back-forward-cache'],
  });
  try {
    const page = await browser.newPage({
      viewport: { width: 1440, height: 1000 },
      javaScriptEnabled: false,
    });
    const pageSession = await page.context().newCDPSession(page);
    const browserSession = await browser.newBrowserCDPSession();
    await pageSession.send('Performance.enable');
    const remote = [];
    page.on('request', request => {
      if (!request.url().startsWith('file:')) remote.push(request.url());
    });
    const initial = await sample('blank', pageSession, browserSession);
    const start = Date.now();
    await page.goto(pathToFileURL(file).href, { waitUntil: 'load' });
    const result = {
      label, run, browser: browser.version(), fileBytes: fs.statSync(file).size,
      loadMs: Date.now() - start, initial,
      loaded: await sample('loaded', pageSession, browserSession), remote,
    };
    if (label === 'bounded-pages' && run === 1) {
      result.navigation = [];
      result.navigationTimesMs = [];
      const pages = evaluationPages(file);
      for (const [index, target] of pages.entries()) {
        const start = Date.now();
        await page.goto(pathToFileURL(target).href, { waitUntil: 'load' });
        result.navigationTimesMs.push(Date.now() - start);
        if ([9, 49, 99].includes(index)) {
          result.navigation.push(await sample(`navigation-${index + 1}`, pageSession, browserSession));
        }
      }
      await page.goBack();
      await page.goForward();
      result.navigation.push(await sample('back-forward', pageSession, browserSession));
    }
    return result;
  } finally {
    await browser.close();
  }
}

async function main() {
  const [single, paged] = process.argv.slice(2);
  if (!single || !paged) throw new Error('Usage: compare-memory.cjs SINGLE_HTML PAGED_INDEX');
  const cases = [];
  for (const [label, file] of [['current-single-html', single], ['bounded-pages', paged]]) {
    for (let run = 1; run <= 5; run++) cases.push(await measureCase(label, file, run));
  }
  console.log(JSON.stringify({
    note: 'Five fresh browsers per case; JavaScript disabled; maximum instantaneous renderer RSS in the dedicated browser; standard BFCache behavior. Both outputs use the same saved result.',
    cases,
  }, null, 2));
}

main().catch(error => {
  console.error(error);
  process.exitCode = 1;
});
