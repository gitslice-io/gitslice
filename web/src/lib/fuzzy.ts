// Fuzzy matching for a file finder: the typed letters must appear in the path
// in order, and the score favors what people mean by a short query: the file's
// own name over its folders, the start of a word (after / - _ . or a capital),
// and letters that follow each other. The scoring follows fzy's.

export interface FuzzyMatch {
  path: string;
  score: number;
  // Positions in the path of the letters that matched, for highlighting.
  indexes: number[];
}

const GAP_LEADING = -0.005;
const GAP_TRAILING = -0.005;
const GAP_INNER = -0.01;
const MATCH_CONSECUTIVE = 1;
const BONUS_SLASH = 0.9;
const BONUS_WORD = 0.8;
const BONUS_CAPITAL = 0.7;
const BONUS_DOT = 0.6;
// A match inside the file's name outranks one that spreads over its folders.
const BASENAME_BONUS = 2;

function bonuses(text: string): number[] {
  const out: number[] = [];
  let previous = "/";
  for (const char of text) {
    let bonus = 0;
    if (previous === "/") {
      bonus = BONUS_SLASH;
    } else if (previous === "-" || previous === "_" || previous === " ") {
      bonus = BONUS_WORD;
    } else if (previous === ".") {
      bonus = BONUS_DOT;
    } else if (previous >= "a" && previous <= "z" && char >= "A" && char <= "Z") {
      bonus = BONUS_CAPITAL;
    }
    out.push(bonus);
    previous = char;
  }
  return out;
}

function isSubsequence(needle: string, haystack: string): boolean {
  let i = 0;
  for (let j = 0; j < haystack.length && i < needle.length; j++) {
    if (haystack[j] === needle[i]) {
      i++;
    }
  }
  return i === needle.length;
}

// align finds the best placement of the query's letters in text. Both are
// compared in lower case; the bonuses come from the text as written.
function align(query: string, text: string): { score: number; indexes: number[] } | null {
  const lowerQuery = query.toLowerCase();
  const lowerText = text.toLowerCase();
  const n = lowerQuery.length;
  const m = lowerText.length;
  if (n === 0 || n > m || !isSubsequence(lowerQuery, lowerText)) {
    return null;
  }
  const bonus = bonuses(text);
  const NEG = Number.NEGATIVE_INFINITY;
  // best[i][j]: the best score of matching query[0..i] within text[0..j].
  // ends[i][j]: the same with query[i] matched exactly at text[j].
  const best: number[][] = [];
  const ends: number[][] = [];
  for (let i = 0; i < n; i++) {
    best.push(new Array<number>(m).fill(NEG));
    ends.push(new Array<number>(m).fill(NEG));
    let previousBest = NEG;
    const gap = i === n - 1 ? GAP_TRAILING : GAP_INNER;
    for (let j = 0; j < m; j++) {
      if (lowerQuery[i] === lowerText[j]) {
        let score = NEG;
        if (i === 0) {
          score = j * GAP_LEADING + bonus[j];
        } else if (j > 0) {
          score = Math.max(best[i - 1][j - 1] + bonus[j], ends[i - 1][j - 1] + MATCH_CONSECUTIVE);
        }
        ends[i][j] = score;
        previousBest = Math.max(score, previousBest + gap);
      } else {
        previousBest = previousBest + gap;
      }
      best[i][j] = previousBest;
    }
  }
  if (best[n - 1][m - 1] === NEG) {
    return null;
  }
  // Walk back from the best end to say which letters matched.
  const indexes = new Array<number>(n);
  let matchRequired = false;
  for (let i = n - 1, j = m - 1; i >= 0; i--) {
    for (; j >= 0; j--) {
      if (ends[i][j] !== NEG && (matchRequired || ends[i][j] === best[i][j])) {
        matchRequired = i > 0 && j > 0 && best[i][j] === ends[i - 1][j - 1] + MATCH_CONSECUTIVE;
        indexes[i] = j--;
        break;
      }
    }
  }
  return { score: best[n - 1][m - 1], indexes };
}

// fuzzyMatch scores one path against a query (spaces in it are ignored), or
// says it does not match.
export function fuzzyMatch(query: string, path: string): FuzzyMatch | null {
  const needle = query.replace(/\s+/g, "");
  if (!needle) {
    return null;
  }
  const baseStart = path.lastIndexOf("/") + 1;
  const base = align(needle, path.slice(baseStart));
  if (base) {
    return {
      path,
      score: base.score + BASENAME_BONUS,
      indexes: base.indexes.map((index) => index + baseStart)
    };
  }
  const whole = align(needle, path);
  return whole ? { path, score: whole.score, indexes: whole.indexes } : null;
}

// rankPaths returns the best matches first, at most limit of them; shorter
// paths win a tie.
export function rankPaths(query: string, paths: readonly string[], limit = 50): FuzzyMatch[] {
  const matches: FuzzyMatch[] = [];
  for (const path of paths) {
    const match = fuzzyMatch(query, path);
    if (match) {
      matches.push(match);
    }
  }
  matches.sort((a, b) => b.score - a.score || a.path.length - b.path.length || a.path.localeCompare(b.path));
  return matches.slice(0, limit);
}
