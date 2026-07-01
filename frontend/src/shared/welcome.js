// Welcome-modal acknowledgement state, kept in a standalone module so
// the component file only exports components (Fast Refresh friendly).
const WELCOME_STORAGE_KEY = 'ehstash:welcome-acked';

export function isWelcomeAcked() {
  try { return localStorage.getItem(WELCOME_STORAGE_KEY) === '1'; } catch { return false; }
}

export function ackWelcome() {
  try { localStorage.setItem(WELCOME_STORAGE_KEY, '1'); } catch { /* ignore */ }
}
