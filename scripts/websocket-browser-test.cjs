// Run against the Actions-built binary; no application build or npm dependency is needed locally.
const { chromium } = require('../tmp/ws-browser/node_modules/playwright');
const { spawn, spawnSync } = require('node:child_process');
const fs = require('node:fs');
const http = require('node:http');
const assert = require('node:assert/strict');

async function main() {
  const children = [];
	process.on('exit', () => { for (const child of children) child.kill('SIGTERM'); });
  const report = [];
  let serverLog = '';
  fs.mkdirSync('tmp/ws-browser-results', { recursive: true });
  fs.writeFileSync('tmp/ws-browser.yml', 'rtspTransports: [tcp]\nrtmp: false\nhls: false\nwebrtc: false\nsrt: false\nmoq: false\nws: true\npaths:\n  all_others:\n');
  const server = process.env.WS_TEST_IMAGE
    ? spawn('docker', ['run', '--rm', '--network', 'host',
      '-v', `${process.cwd()}/tmp/ws-browser.yml:/config.yml:ro`, process.env.WS_TEST_IMAGE, '/config.yml'])
    : spawn('./binaries/tdt-media-stream-linux-amd64', ['tmp/ws-browser.yml']);
  children.push(server);
  server.stdout.on('data', b => { serverLog += b; });
  server.stderr.on('data', b => { serverLog += b; });
  const web = http.createServer((req, res) => {
    res.setHeader('Content-Type', 'text/html; charset=utf-8');
    res.end(fs.readFileSync('docs/websocket-player.html'));
  });
  await new Promise(resolve => web.listen(8895, '127.0.0.1', resolve));
  const browser = await chromium.launch({ headless: true });
  const page = await browser.newPage();
  page.setDefaultTimeout(15000);
  const errors = [];
  page.on('pageerror', e => errors.push(e.message));
  try {
    for (const codec of ['h264', 'h265']) {
      let ffmpegLog = '';
      const ffmpeg = spawn('ffmpeg', ['-hide_banner', '-loglevel', 'error', '-re', '-f', 'lavfi', '-i',
        'testsrc2=size=320x180:rate=25', '-an', '-c:v', codec === 'h264' ? 'libx264' : 'libx265',
        '-threads', '2', '-g', '25', '-bf', '2', ...(codec === 'h265' ? ['-x265-params', 'pools=1:frame-threads=1'] : []),
        '-pix_fmt', 'yuv420p', '-rtsp_transport', 'tcp', '-f', 'rtsp', `rtsp://127.0.0.1:8554/test/${codec}`]);
      children.push(ffmpeg);
      ffmpeg.stderr.on('data', b => { ffmpegLog += b; });
      const start = Date.now();
      while (!serverLog.includes(`is publishing to path 'test/${codec}'`)) {
        if (Date.now() - start > 15000) throw Error(`RTSP publisher not ready: ${serverLog}\n${ffmpegLog}`);
        await new Promise(resolve => setTimeout(resolve, 100));
      }
      await page.goto('http://127.0.0.1:8895');
      const wire = await page.evaluate(codec => new Promise((resolve, reject) => {
        const socket = new WebSocket(`ws://127.0.0.1:8894/test/${codec}`);
        socket.binaryType = 'arraybuffer';
        const timer = setTimeout(() => { socket.close(); reject(Error('wire timeout')); }, 8000);
        let ci, firstFrame, frames = 0, previousPTS, reordered = false;
        const nalus = [];
        socket.onerror = () => reject(Error('wire connection failed'));
        socket.onmessage = ({ data }) => {
          try {
            const view = new DataView(data), type = view.getUint8(0);
            if (type === 1) {
              if (ci || frames) throw Error('unexpected CodecInfo order');
              if (view.getUint8(1) !== (codec === 'h264' ? 4 : 5)) throw Error('wrong codec');
              ci = Array.from(new Uint8Array(data));
            } else if (type === 2) {
              if (!ci) throw Error('missing CodecInfo');
              if (!frames && view.getUint8(9) !== 1) throw Error('first frame is not random access');
              if (!frames) firstFrame = Array.from(new Uint8Array(data));
              const pts = Number(view.getBigUint64(1));
              reordered ||= previousPTS !== undefined && pts < previousPTS;
              previousPTS = pts;
              const count = view.getUint16(10); let offset = 12;
              if (!count) throw Error('empty AU');
              for (let i = 0; i < count; i++) {
                const size = view.getUint32(offset); offset += 4;
                if (!size || offset + size > data.byteLength) throw Error('bad NALU length');
                nalus.push(Array.from(new Uint8Array(data, offset, size))); offset += size;
              }
              if (offset !== data.byteLength) throw Error('unexpected frame tail');
              if (++frames === 40) { clearTimeout(timer); socket.close(); resolve({ frames, reordered, ci, firstFrame, nalus }); }
            }
          } catch (e) { clearTimeout(timer); socket.close(); reject(e); }
        };
      }), codec);
      assert(wire.reordered, `${codec}: B-frame PTS ordering was not exercised`);
      fs.writeFileSync(`tmp/ws-browser-results/${codec}-codec.bin`, Buffer.from(wire.ci));
      fs.writeFileSync(`tmp/ws-browser-results/${codec}-frame.bin`, Buffer.from(wire.firstFrame));
      const streamFile = `tmp/ws-browser-results/${codec}.${codec}`;
      fs.writeFileSync(streamFile, Buffer.concat(wire.nalus.flatMap(n => [Buffer.from([0, 0, 0, 1]), Buffer.from(n)])));
      const decode = spawnSync('ffmpeg', ['-hide_banner', '-f', codec === 'h264' ? 'h264' : 'hevc', '-i', streamFile, '-f', 'null', '-'], { encoding: 'utf8', timeout: 30000 });
      fs.writeFileSync(`tmp/ws-browser-results/${codec}-decode.log`, decode.stderr || '');
      assert.equal(decode.status, 0, decode.stderr);
      const decodedFrames = Number([...decode.stderr.matchAll(/frame=\s*(\d+)/g)].at(-1)?.[1]);
      assert(decodedFrames >= 25, `${codec}: only ${decodedFrames} frames decoded from WS output`);
      await page.locator('#url').fill(`ws://127.0.0.1:8894/test/${codec}`);
      await page.locator('#play').click();
      await page.waitForFunction(() => /已显示 (\d+) 帧/.test(document.querySelector('#status').textContent) || /失败/.test(document.querySelector('#status').textContent), { timeout: 10000 });
      let status = await page.locator('#status').textContent();
      const supported = status.includes('已显示');
      if (supported) {
        await page.waitForFunction(() => Number(document.querySelector('#status').textContent.match(/已显示 (\d+) 帧/)?.[1]) >= 25, { timeout: 10000 });
        status = await page.locator('#status').textContent();
        await page.screenshot({ path: `tmp/ws-browser-results/${codec}.png` });
      }
      if (codec === 'h264') assert(supported, status);
      report.push({ codec, protocolFrames: wire.frames, ffmpegDecodedFrames: decodedFrames, bFramePTSReordered: wire.reordered, webCodecsRendered: supported, status });
      await page.locator('#stop').click();
      ffmpeg.kill('SIGTERM');
    }
    assert.deepEqual(errors, []);
  } finally {
    fs.writeFileSync('tmp/ws-browser-results/report.json', JSON.stringify({ browser: browser.version(), report, errors }, null, 2));
    fs.writeFileSync('tmp/ws-browser-results/server.log', serverLog);
    await browser.close();
    web.close();
    for (const child of children) child.kill('SIGTERM');
  }
  console.log(JSON.stringify(report, null, 2));
}

main().catch(e => { console.error(e); process.exit(1); });
