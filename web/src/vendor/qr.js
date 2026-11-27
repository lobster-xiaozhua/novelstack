// 第十阶段：极简 QR 码生成（基于零依赖的 qrcode-generator，MIT）。
// 只用于把控制台的 LAN 配对载荷渲染成 SVG 供 App 扫描；不引入 npm 依赖。
import qrcode from './qrcode-core.js'

/**
 * 生成 QR 码的 SVG 字符串。
 * @param {string} text 要编码的文本（配对载荷是 ASCII JSON）
 * @param {number} cell 单格像素
 * @returns {string} <svg ...>...</svg>
 */
export function qrSvg(text, cell = 6) {
  // typeNumber=0 让库自动选择最小版本；纠错级别 M。
  const qr = qrcode(0, 'M')
  qr.addData(text)
  qr.make()
  const count = qr.getModuleCount()
  const margin = 2
  const size = (count + margin * 2) * cell
  const parts = [`<svg xmlns="http://www.w3.org/2000/svg" width="${size}" height="${size}" viewBox="0 0 ${size} ${size}" shape-rendering="crispEdges">`]
  parts.push(`<rect width="100%" height="100%" fill="#ffffff"/>`)
  for (let r = 0; r < count; r++) {
    for (let c = 0; c < count; c++) {
      if (qr.isDark(r, c)) {
        const x = (c + margin) * cell
        const y = (r + margin) * cell
        parts.push(`<rect x="${x}" y="${y}" width="${cell}" height="${cell}" fill="#000000"/>`)
      }
    }
  }
  parts.push('</svg>')
  return parts.join('')
}

/** 构造 LAN 配对载荷（与 App 端 ApiEndpointManager.parseLanPairing 对齐）。 */
export function lanPairingPayload(code, endpoint) {
  return JSON.stringify({ v: 1, endpoint: endpoint || '', code: code })
}
