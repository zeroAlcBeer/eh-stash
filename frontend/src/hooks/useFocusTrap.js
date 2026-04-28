import { useEffect, useRef } from 'react';

/**
 * Traps keyboard focus inside a modal/dialog container.
 * Also handles Escape key to close.
 *
 * @param {boolean} active - Whether the trap is active
 * @param {() => void} onEscape - Called when Escape is pressed
 * @returns {React.RefObject} - Ref to attach to the dialog container
 */
export function useFocusTrap(active, onEscape) {
  const ref = useRef(null);

  useEffect(() => {
    if (!active) return;

    const container = ref.current;
    if (!container) return;

    const focusableSelector =
      'a[href], button:not([disabled]), input:not([disabled]), select:not([disabled]), textarea:not([disabled]), [tabindex]:not([tabindex="-1"])';

    // Focus first focusable element on open
    const firstFocusable = container.querySelector(focusableSelector);
    firstFocusable?.focus();

    const handleKeyDown = (e) => {
      if (e.key === 'Escape') {
        e.stopPropagation();
        onEscape?.();
        return;
      }

      if (e.key !== 'Tab') return;

      const focusableElements = container.querySelectorAll(focusableSelector);
      if (focusableElements.length === 0) return;

      const first = focusableElements[0];
      const last = focusableElements[focusableElements.length - 1];

      if (e.shiftKey) {
        if (document.activeElement === first) {
          e.preventDefault();
          last.focus();
        }
      } else {
        if (document.activeElement === last) {
          e.preventDefault();
          first.focus();
        }
      }
    };

    document.addEventListener('keydown', handleKeyDown, true);
    return () => document.removeEventListener('keydown', handleKeyDown, true);
  }, [active, onEscape]);

  return ref;
}
