import type { BulkResult } from '../api/bulk'

/**
 * Reason code the bulk endpoints return per ID when a user initiated search
 * was refused because the global "Enable automatic grabbing" switch is off.
 *
 * Before #2669 those handlers answered ok:true and the search was dropped in a
 * background goroutine, so the button flashed and nothing happened. Matching on
 * the code rather than on the server's English text keeps the message
 * translatable and lets the UI point at the setting.
 */
export const AUTO_GRAB_DISABLED_CODE = 'auto_grab_disabled'

/**
 * True when any entry in a bulk response was refused for that reason. The
 * switch is global, so in practice it is either every search entry or none,
 * but one refused entry is already enough to tell the user nothing ran.
 */
export function isAutoGrabRefusal(res: BulkResult | null | undefined): boolean {
  if (!res?.results) return false
  return Object.values(res.results).some(r => r?.code === AUTO_GRAB_DISABLED_CODE)
}
