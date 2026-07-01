import { useState, useEffect } from 'react';

// User-facing settings stored in localStorage.
// Communicated cross-component via a window CustomEvent so any hook
// instance updates without a React context.

const ALLOW_COSPLAY_KEY = 'ehstash:allow-cosplay';
const CHANGE_EVENT = 'ehstash:settings-changed';

export function getAllowCosplay() {
  try { return localStorage.getItem(ALLOW_COSPLAY_KEY) === '1'; } catch { return false; }
}

function setAllowCosplay(value) {
  try { localStorage.setItem(ALLOW_COSPLAY_KEY, value ? '1' : '0'); } catch { /* ignore */ }
  window.dispatchEvent(new CustomEvent(CHANGE_EVENT));
}

export function useAllowCosplay() {
  const [v, setV] = useState(getAllowCosplay);
  useEffect(() => {
    const handler = () => setV(getAllowCosplay());
    window.addEventListener(CHANGE_EVENT, handler);
    return () => window.removeEventListener(CHANGE_EVENT, handler);
  }, []);
  return [v, setAllowCosplay];
}
