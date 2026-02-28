import { useState, useRef, useEffect } from 'react';

/**
 * Smoothly animates a numeric value from its previous value to the new target
 * using an ease-out cubic curve via requestAnimationFrame.
 *
 * @param {number|null} target   - The target value. Pass null to clear (shows "—").
 * @param {number}      duration - Animation duration in ms (default 500).
 * @returns {number|null}        - The current interpolated value.
 */
export function useCountUp(target, duration = 500) {
    const [displayed, setDisplayed] = useState(target);
    const prevRef = useRef(target);
    const frameRef = useRef(null);
    const startTimeRef = useRef(null);

    useEffect(() => {
        // Cancel any in-flight animation
        if (frameRef.current !== null) {
            cancelAnimationFrame(frameRef.current);
            frameRef.current = null;
        }

        if (target === null || target === undefined) {
            prevRef.current = null;
            setDisplayed(null);
            return;
        }

        const from = prevRef.current ?? 0;
        const to = target;
        prevRef.current = target;

        if (from === to) {
            setDisplayed(to);
            return;
        }

        startTimeRef.current = null;

        const step = (timestamp) => {
            if (startTimeRef.current === null) startTimeRef.current = timestamp;
            const elapsed = timestamp - startTimeRef.current;
            const t = Math.min(elapsed / duration, 1);
            // ease-out cubic: fast start, gentle finish
            const eased = 1 - Math.pow(1 - t, 3);
            const current = Math.round(from + (to - from) * eased);
            setDisplayed(current);

            if (t < 1) {
                frameRef.current = requestAnimationFrame(step);
            } else {
                frameRef.current = null;
            }
        };

        frameRef.current = requestAnimationFrame(step);

        return () => {
            if (frameRef.current !== null) {
                cancelAnimationFrame(frameRef.current);
                frameRef.current = null;
            }
        };
    }, [target, duration]);

    return displayed;
}
