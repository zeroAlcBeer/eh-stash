import { useState, useEffect } from 'react';

const TAG_TRANS_URL =
    'https://raw.githubusercontent.com/scooderic/exhentai-tags-chinese-translation/refs/heads/master/dist/ehtags-cn.json';

// Module-level cache so we only fetch once per session
// dict: Map<englishValue, chineseTranslation>
let _cache = null;
let _promise = null;

function loadTranslations() {
    if (_cache) return Promise.resolve(_cache);
    if (!_promise) {
        _promise = fetch(TAG_TRANS_URL)
            .then((r) => r.json())
            .then((arr) => {
                // JSON format: [{ k: "english value", v: "中文翻译；..." }, ...]
                // Build a plain object for fast O(1) lookup
                const dict = Object.create(null);
                for (const entry of arr) {
                    if (entry.k) dict[entry.k] = entry.v ?? entry.k;
                }
                _cache = dict;
                return dict;
            })
            .catch((err) => {
                _promise = null; // allow retry on next call
                console.warn('[TagTranslation] failed to load:', err);
                return null;
            });
    }
    return _promise;
}

/**
 * Hook that lazily fetches the EH tag Chinese-translation dictionary.
 * @param {boolean} enabled – only fetches when true
 * @returns {{ translate: (value: string) => string | null, isLoaded: boolean }}
 *
 * JSON format: array of { k: "english tag value", v: "中文；..." }
 * translate("glasses") → "眼镜" (first semicolon-delimited term)
 */
export function useTagTranslation(enabled) {
    const [dict, setDict] = useState(_cache);

    useEffect(() => {
        if (!enabled) return;
        if (_cache) {
            setDict(_cache);
            return;
        }
        loadTranslations().then((data) => {
            if (data) setDict(data);
        });
    }, [enabled]);

    const translate = (value) => {
        if (!dict || !value) return null;
        const raw = dict[value] ?? null;
        if (!raw) return null;
        // v may be "翻译1；翻译2；…", take the first term only
        return raw.split('；')[0].trim() || raw;
    };

    return { translate, isLoaded: !!dict };
}
