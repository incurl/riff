/**
 * Locates search terms inside rendered text so a hit can be highlighted in place.
 *
 * The server has two matching modes, and this file tracks neither exactly — it is a
 * highlight, not a filter, so being slightly more permissive than the server is
 * harmless (an over-highlighted word is inside a row that already matched) while being
 * stricter would hide a match the server did return.
 *
 *  - LIKE (the default, `RIFF_FTS` unset). SQLite's LIKE case-folds ASCII only, so a
 *    non-ASCII term matches only its exact spelling. This file folds case, so it will
 *    highlight a case variant the LIKE query did *not* match. That is the safe direction:
 *    the row is already in the result set for some other reason, or the highlight is
 *    simply absent. It cannot invent a row.
 *  - FTS5 (`RIFF_FTS=1`). `unicode61` folds case across the full Unicode range and
 *    `remove_diacritics 2` folds diacritics, so folding here is much closer to the
 *    server. Note it still is not identical: unicode61 does not perform the full
 *    case-folding that maps `ß` to `ss`, so `ß` is not highlighted from `ss`.
 *
 * Folding can change a string's length, so matching runs on the folded text and every
 * folded code unit remembers which original offset it came from. The ranges returned
 * are therefore always valid offsets into the original text.
 *
 * Neither mode tokenises: a search for `incident` highlights inside `incident-report`,
 * and FTS will not return that row for `incident*`-less matching unless the whole
 * token is present.
 */

export interface TextRange {
  /** Inclusive start offset in the original text (UTF-16 code units). */
  start: number;
  /** Exclusive end offset in the original text. */
  end: number;
}

interface FoldedText {
  folded: string;
  /** `offsets[i]` is the original offset of folded code unit `i`; `offsets[folded.length]` is the original length. */
  offsets: number[];
}

const foldCodePoint = (codePoint: string): string => codePoint.toUpperCase().toLowerCase();

const foldText = (text: string): FoldedText => {
  let folded = "";
  const offsets: number[] = [];
  let originalOffset = 0;
  for (const codePoint of text) {
    const foldedCodePoint = foldCodePoint(codePoint);
    for (let i = 0; i < foldedCodePoint.length; i++) {
      offsets.push(originalOffset);
    }
    folded += foldedCodePoint;
    originalOffset += codePoint.length;
  }
  offsets.push(originalOffset);
  return { folded, offsets };
};

/** Folds and de-duplicates search terms once so they can be matched against many text nodes. */
export const prepareSearchTerms = (terms: readonly string[]): string[] => {
  const prepared = new Set<string>();
  for (const term of terms) {
    const folded = foldText(term.trim()).folded;
    if (folded) prepared.add(folded);
  }
  return Array.from(prepared);
};

/**
 * Every occurrence of any prepared term in `text`, as sorted, non-overlapping ranges.
 * Adjacent or overlapping hits (two terms sharing characters, or a term appearing
 * back-to-back) merge into one range so a highlight never paints the same character twice.
 */
export const findSearchMatches = (text: string, preparedTerms: readonly string[]): TextRange[] => {
  if (!text || preparedTerms.length === 0) return [];

  const { folded, offsets } = foldText(text);
  const hits: TextRange[] = [];
  for (const term of preparedTerms) {
    let from = folded.indexOf(term);
    while (from !== -1) {
      // A hit may end partway through a code point that folded to several units (`ß` → `ss`);
      // widen it to that code point's end so the range never splits an original character.
      let endIndex = from + term.length;
      while (endIndex < folded.length && offsets[endIndex] === offsets[endIndex - 1]) endIndex++;
      hits.push({ start: offsets[from], end: offsets[endIndex] });
      from = folded.indexOf(term, from + 1);
    }
  }
  if (hits.length === 0) return [];

  hits.sort((a, b) => a.start - b.start || a.end - b.end);
  const merged: TextRange[] = [];
  for (const hit of hits) {
    const last = merged[merged.length - 1];
    if (last && hit.start <= last.end) {
      last.end = Math.max(last.end, hit.end);
    } else {
      merged.push({ ...hit });
    }
  }
  return merged;
};
