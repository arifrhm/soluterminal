#!/usr/bin/env node
//
// Regenerates every web + desktop app icon from pane-mark.svg, the website's
// mark (website/src/favicon.svg): a near-black rounded tile holding three
// panes, the top-right one lime. Needs Node only; the PNG, ICO and ICNS
// encoders below avoid image dependencies.
//
//   node icon-source/generate.js
//
// Three renderings of the same mark:
//   tile   the SVG exactly as drawn, transparent outside the rounded tile
//          (desktop icons from 40 px up, favicons, the top bar)
//   bleed  an opaque square with the panes inset, for surfaces that crop
//          (apple-touch rounds its own corners, PWA maskable crops to a circle)
//   small  hand-placed panes on the pixel grid for 16/20/24/32 px, where a
//          straight downscale smears the 4-unit gaps into mush (tray, small
//          Explorer and taskbar entries)

const fs = require("fs");
const path = require("path");
const zlib = require("zlib");

const ROOT = path.resolve(__dirname, "..");
const WEB = path.join(ROOT, "web/public");
const TAURI = path.join(ROOT, "desktop/src-tauri/icons");
const TRAY = path.join(TAURI, "tray");
const SOURCE = path.join(__dirname, "pane-mark.svg");

// ---- Source -------------------------------------------------------------

function attr(tag, name, fallback) {
  const m = tag.match(new RegExp(`\\s${name}="([^"]*)"`));
  return m ? m[1] : fallback;
}

function readMark(file) {
  const svg = fs.readFileSync(file, "utf8");
  const viewBox = attr(svg.match(/<svg[^>]*>/)[0], "viewBox").split(/\s+/).map(Number);
  const rects = [...svg.matchAll(/<rect\b[^>]*>/g)].map(([tag]) => ({
    x: Number(attr(tag, "x", 0)),
    y: Number(attr(tag, "y", 0)),
    w: Number(attr(tag, "width")),
    h: Number(attr(tag, "height")),
    r: Number(attr(tag, "rx", 0)),
    fill: hex(attr(tag, "fill")),
  }));
  const [tile, ...panes] = rects;
  return { size: viewBox[2], tile, panes };
}

function hex(value) {
  const n = parseInt(value.slice(1), 16);
  return [(n >> 16) & 255, (n >> 8) & 255, n & 255];
}

const MARK = readMark(SOURCE);
const INK = MARK.tile.fill;
const LIME = MARK.panes.find((p) => p.fill[0] > 128).fill;
const DIM = MARK.panes.find((p) => p.fill[0] <= 128).fill;

// At 16 and 20 px the dim panes sit at about 1.8:1 on the tile and drop out
// on a dark taskbar; lifting them keeps the three-pane layout readable.
const SMALL_DIM = hex("#56604f");

// Pixel-grid layouts for the small sizes, panes as [x0, y0, x1, y1] in
// pixels: left pane, lime pane, lower-right pane. Gaps are a whole 2 px so
// they never blur shut, and the margins stay symmetric.
const SMALL = {
  16: { tileR: 3.5, paneR: 0.8, dim: SMALL_DIM, panes: [[2, 2, 7, 14], [9, 2, 14, 8], [9, 10, 14, 14]] },
  20: { tileR: 4.4, paneR: 1, dim: SMALL_DIM, panes: [[3, 3, 9, 17], [11, 3, 17, 10], [11, 12, 17, 17]] },
  24: { tileR: 5.25, paneR: 1.25, dim: DIM, panes: [[4, 4, 11, 20], [13, 4, 20, 13], [13, 15, 20, 20]] },
  32: { tileR: 7, paneR: 2, dim: DIM, panes: [[5, 5, 15, 27], [17, 5, 27, 17], [17, 19, 27, 27]] },
};

// ---- Rasteriser ---------------------------------------------------------

function roundedRectDistance(px, py, rect) {
  const r = Math.min(rect.r, rect.w / 2, rect.h / 2);
  const qx = Math.abs(px - (rect.x + rect.w / 2)) - (rect.w / 2 - r);
  const qy = Math.abs(py - (rect.y + rect.h / 2)) - (rect.h / 2 - r);
  const outside = Math.hypot(Math.max(qx, 0), Math.max(qy, 0));
  const inside = Math.min(Math.max(qx, qy), 0);
  return outside + inside - r;
}

// Area coverage by 16×16 supersampling, only for pixels near an edge.
const SS = 16;
function coverage(x, y, rect) {
  const d = roundedRectDistance(x + 0.5, y + 0.5, rect);
  if (d <= -0.75) return 1;
  if (d >= 0.75) return 0;
  let hits = 0;
  for (let sy = 0; sy < SS; sy++) {
    for (let sx = 0; sx < SS; sx++) {
      if (roundedRectDistance(x + (sx + 0.5) / SS, y + (sy + 0.5) / SS, rect) <= 0) hits++;
    }
  }
  return hits / (SS * SS);
}

// Straight-alpha "source over" of a solid colour at the given alpha.
function blend(data, idx, rgb, alpha) {
  if (alpha <= 0) return;
  const da = data[idx + 3] / 255;
  const outA = alpha + da * (1 - alpha);
  for (let c = 0; c < 3; c++) {
    data[idx + c] = Math.round((rgb[c] * alpha + data[idx + c] * da * (1 - alpha)) / outA);
  }
  data[idx + 3] = Math.round(outA * 255);
}

// Draws pixel-space rects ({x, y, w, h, r, fill}) in order on a size² canvas.
function raster(size, rects) {
  const data = Buffer.alloc(size * size * 4);
  for (let y = 0; y < size; y++) {
    for (let x = 0; x < size; x++) {
      const idx = (y * size + x) * 4;
      for (const rect of rects) blend(data, idx, rect.fill, coverage(x, y, rect));
    }
  }
  return data;
}

function scaled(rect, k, offset = 0) {
  return {
    x: (rect.x + offset) * k,
    y: (rect.y + offset) * k,
    w: rect.w * k,
    h: rect.h * k,
    r: rect.r * k,
    fill: rect.fill,
  };
}

// The SVG as drawn: rounded tile with transparent corners.
function renderTile(size) {
  const k = size / MARK.size;
  return raster(size, [MARK.tile, ...MARK.panes].map((r) => scaled(r, k)));
}

// Opaque full-bleed square; `view` is its side in mark units, so a larger
// view leaves more padding around the panes.
function renderBleed(size, view) {
  const k = size / view;
  const offset = (view - MARK.size) / 2;
  const ground = { x: 0, y: 0, w: size, h: size, r: 0, fill: INK };
  return raster(size, [ground, ...MARK.panes.map((r) => scaled(r, k, offset))]);
}

function renderSmall(size) {
  const spec = SMALL[size];
  const tile = { x: 0, y: 0, w: size, h: size, r: spec.tileR, fill: INK };
  const fills = [spec.dim, LIME, spec.dim];
  const panes = spec.panes.map(([x0, y0, x1, y1], i) => ({
    x: x0,
    y: y0,
    w: x1 - x0,
    h: y1 - y0,
    r: spec.paneR,
    fill: fills[i],
  }));
  return raster(size, [tile, ...panes]);
}

// The desktop rendering at any size: hand-tuned where a layout exists.
function renderDesktop(size) {
  return SMALL[size] ? renderSmall(size) : renderTile(size);
}

// ---- Encoders -----------------------------------------------------------

function encodePng(size, rgba) {
  const raw = Buffer.alloc((size * 4 + 1) * size);
  for (let y = 0; y < size; y++) {
    rgba.copy(raw, y * (size * 4 + 1) + 1, y * size * 4, (y + 1) * size * 4);
  }
  const ihdr = Buffer.alloc(13);
  ihdr.writeUInt32BE(size, 0);
  ihdr.writeUInt32BE(size, 4);
  ihdr[8] = 8; // bit depth
  ihdr[9] = 6; // RGBA
  return Buffer.concat([
    Buffer.from([0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a]),
    pngChunk("IHDR", ihdr),
    pngChunk("IDAT", zlib.deflateSync(raw, { level: 9 })),
    pngChunk("IEND", Buffer.alloc(0)),
  ]);
}

function pngChunk(type, data) {
  const name = Buffer.from(type, "ascii");
  const out = Buffer.alloc(12 + data.length);
  out.writeUInt32BE(data.length, 0);
  name.copy(out, 4);
  data.copy(out, 8);
  out.writeUInt32BE(crc32(Buffer.concat([name, data])), 8 + data.length);
  return out;
}

const CRC_TABLE = new Uint32Array(256).map((_, n) => {
  let c = n;
  for (let k = 0; k < 8; k++) c = c & 1 ? 0xedb88320 ^ (c >>> 1) : c >>> 1;
  return c >>> 0;
});

function crc32(buffer) {
  let c = 0xffffffff;
  for (const byte of buffer) c = CRC_TABLE[(c ^ byte) & 0xff] ^ (c >>> 8);
  return (c ^ 0xffffffff) >>> 0;
}

// ICO entries hold PNGs as-is (supported since Vista).
function buildIco(images) {
  const dir = Buffer.alloc(6 + 16 * images.length);
  dir.writeUInt16LE(1, 2);
  dir.writeUInt16LE(images.length, 4);
  let offset = dir.length;
  images.forEach(({ size, png }, i) => {
    const e = 6 + 16 * i;
    dir.writeUInt8(size >= 256 ? 0 : size, e + 0);
    dir.writeUInt8(size >= 256 ? 0 : size, e + 1);
    dir.writeUInt16LE(1, e + 4);
    dir.writeUInt16LE(32, e + 6);
    dir.writeUInt32LE(png.length, e + 8);
    dir.writeUInt32LE(offset, e + 12);
    offset += png.length;
  });
  return Buffer.concat([dir, ...images.map((img) => img.png)]);
}

function buildIcns(entries) {
  const chunks = entries.flatMap(({ type, png }) => {
    const head = Buffer.alloc(8);
    head.write(type, 0, "ascii");
    head.writeUInt32BE(png.length + 8, 4);
    return [head, png];
  });
  const body = Buffer.concat(chunks);
  const header = Buffer.alloc(8);
  header.write("icns", 0, "ascii");
  header.writeUInt32BE(body.length + 8, 4);
  return Buffer.concat([header, body]);
}

// ---- Outputs ------------------------------------------------------------

const memo = new Map();
function once(key, make) {
  if (!memo.has(key)) memo.set(key, make());
  return memo.get(key);
}
const desktopRgba = (s) => once(`d${s}`, () => renderDesktop(s));
const desktopPng = (s) => once(`dp${s}`, () => encodePng(s, desktopRgba(s)));
const tilePng = (s) => once(`tp${s}`, () => encodePng(s, renderTile(s)));

function write(file, buffer) {
  fs.mkdirSync(path.dirname(file), { recursive: true });
  fs.writeFileSync(file, buffer);
  console.log(`  ${path.relative(ROOT, file)}`);
}

console.log("icons written:");

// Web / PWA. The SVG is the favicon; PNGs back it up for the manifest,
// notifications and browsers without SVG favicons.
write(path.join(WEB, "favicon.svg"), fs.readFileSync(SOURCE));
write(path.join(WEB, "icon-192.png"), tilePng(192));
write(path.join(WEB, "icon-512.png"), tilePng(512));
write(path.join(WEB, "icon-mark-192.png"), tilePng(192));
// iOS fills transparency with black and rounds the corners itself; same
// padding as website/src/apple-touch-icon.png.
write(path.join(WEB, "apple-touch-icon.png"), encodePng(180, renderBleed(180, 76)));
// Maskable: the pane block's corners stay inside the 40 % safe circle.
write(path.join(WEB, "icon-maskable-192.png"), encodePng(192, renderBleed(192, 84)));
write(path.join(WEB, "icon-maskable-512.png"), encodePng(512, renderBleed(512, 84)));

// Tauri bundle icons.
write(path.join(TAURI, "32x32.png"), desktopPng(32));
write(path.join(TAURI, "128x128.png"), desktopPng(128));
write(path.join(TAURI, "128x128@2x.png"), desktopPng(256));
write(path.join(TAURI, "icon.png"), desktopPng(512));

// Windows exe/installer resource. Tauri also uses the FIRST entry as the
// default window icon (taskbar button, Alt+Tab), so 48 px leads: it scales
// down cleanly to the 24/30/36 px taskbar slots, where 16 px would blow up.
const icoSizes = [48, 16, 20, 24, 32, 40, 64, 256];
write(
  path.join(TAURI, "icon.ico"),
  buildIco(icoSizes.map((size) => ({ size, png: desktopPng(size) }))),
);

write(
  path.join(TAURI, "icon.icns"),
  buildIcns([
    { type: "icp4", png: desktopPng(16) },
    { type: "icp5", png: desktopPng(32) },
    { type: "ic11", png: desktopPng(32) },
    { type: "ic12", png: desktopPng(64) },
    { type: "ic07", png: desktopPng(128) },
    { type: "ic08", png: desktopPng(256) },
    { type: "ic13", png: desktopPng(256) },
    { type: "ic09", png: desktopPng(512) },
    { type: "ic14", png: desktopPng(512) },
    { type: "ic10", png: desktopPng(1024) },
  ]),
);

// Windows tray: raw RGBA the shell embeds and picks from by the system's
// small-icon size (desktop/src-tauri/src/tray_icon.rs), so the notification
// area gets pixels drawn for it instead of a rescaled 16 px.
for (const size of [16, 20, 24, 32, 40, 48]) {
  write(path.join(TRAY, `tray-${size}.rgba`), desktopRgba(size));
}
