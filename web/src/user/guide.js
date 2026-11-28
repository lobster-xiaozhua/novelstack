// 新手引导的本地状态。
//
// 刻意用 localStorage 而不是 IndexedDB / 服务端：
//  - 引导只跟「这一台设备上的这个浏览器」有关，用户换设备重新看一遍是合理的；
//  - 清空离线缓存时不应该把引导状态一起删掉（属于 UX 偏好，不是数据）；
//  - localStorage 同步可读，首帧就能决定要不要盖引导，不会闪一下。

const KEY_ONBOARD = 'novelstack.onboarded.v1'
const KEY_COACH = 'novelstack.coach.v1'

function read(key) {
  try {
    return localStorage.getItem(key)
  } catch (_) {
    return null
  }
}

function write(key, value) {
  try {
    localStorage.setItem(key, value)
  } catch (_) {
    // 隐私模式下 localStorage 可能抛异常：引导状态丢失不影响主流程，静默即可。
  }
}

export function hasOnboarded() {
  return read(KEY_ONBOARD) === '1'
}

export function markOnboarded() {
  write(KEY_ONBOARD, '1')
}

function coachSet() {
  try {
    const raw = read(KEY_COACH)
    return raw ? new Set(JSON.parse(raw)) : new Set()
  } catch (_) {
    return new Set()
  }
}

/** 某个页面的蒙层引导是否已经看过。 */
export function coachSeen(name) {
  return coachSet().has(name)
}

export function markCoachSeen(name) {
  const set = coachSet()
  set.add(name)
  write(KEY_COACH, JSON.stringify([...set]))
}

/** 设置页「重看新手引导」用。 */
export function resetGuide() {
  write(KEY_ONBOARD, '')
  write(KEY_COACH, '[]')
}
