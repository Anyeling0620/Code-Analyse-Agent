/**
 * The manual's seven section hues. Each hue is a divider-board colour: it is
 * used at full strength for tabs, swatches and the current-sheet band, never
 * as a tint behind long-form reading.
 *
 * `on` is the text colour that passes contrast on that board.
 */
export const SECTION_HUES = [
  { name: 'oxide', value: '#A8461F', on: '#FBF8EE' },
  { name: 'chrome', value: '#E0B11F', on: '#1B2024' },
  { name: 'grass', value: '#4F6D33', on: '#FBF8EE' },
  { name: 'teal', value: '#2A6E6A', on: '#FBF8EE' },
  { name: 'ultramarine', value: '#3D5C9E', on: '#FBF8EE' },
  { name: 'violet', value: '#6A579C', on: '#FBF8EE' },
  { name: 'sienna', value: '#96583C', on: '#FBF8EE' },
] as const;

export type SectionHue = (typeof SECTION_HUES)[number];

/** Deterministic hue for an arbitrary id (sessions, messages). */
export function sectionHueFor(seed: string): SectionHue {
  let hash = 0;
  for (let i = 0; i < seed.length; i += 1) {
    hash = (hash * 31 + seed.charCodeAt(i)) >>> 0;
  }
  return SECTION_HUES[hash % SECTION_HUES.length];
}

/** Hue by visible order; used for message tabs and the reading rail. */
export function sectionHueAt(index: number): SectionHue {
  const normalized = ((index % SECTION_HUES.length) + SECTION_HUES.length) % SECTION_HUES.length;
  return SECTION_HUES[normalized];
}

/** Short, stable identifier for session/trace labels. */
export function shortId(value: string, length = 8): string {
  const text = (value || '').trim();
  if (text.length <= length) {
    return text;
  }
  return text.slice(0, length);
}

/** `#03` style label for the current message leaf. */
export function sectionLabel(index: number): string {
  return `§${String(index + 1).padStart(2, '0')}`;
}
