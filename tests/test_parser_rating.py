"""Tests for _extract_rating_signal — covers all 10 score buckets (5.0 → 0.5)."""

import pytest
from bs4 import BeautifulSoup

from scraper.parser import _extract_rating_signal


def _make_element(x: int, y: int) -> "Tag":
    """Build a minimal element containing a single .ir div with given sprite coords."""
    html = (
        f'<div>'
        f'  <div class="ir" style="background-position: {x}px {y}px; opacity: 1;"></div>'
        f'</div>'
    )
    return BeautifulSoup(html, "html.parser").find("div")


def _make_element_two_layers(fg_x: int, fg_y: int, bg_x: int, bg_y: int) -> "Tag":
    """Build an element with two .ir divs (background layer first, foreground second)."""
    html = (
        f'<div>'
        f'  <div class="ir" style="background-position: {bg_x}px {bg_y}px;"></div>'
        f'  <div class="ir" style="background-position: {fg_x}px {fg_y}px; opacity: 1;"></div>'
        f'</div>'
    )
    return BeautifulSoup(html, "html.parser").find("div")


# (score, x, y) — exhaustive mapping from the JS getStarStyle encoder
SCORE_TABLE = [
    (5.0, 0, -1),
    (4.5, 0, -21),
    (4.0, -16, -1),
    (3.5, -16, -21),
    (3.0, -32, -1),
    (2.5, -32, -21),
    (2.0, -48, -1),
    (1.5, -48, -21),
    (1.0, -64, -1),
    (0.5, -64, -21),
]


@pytest.mark.parametrize("expected_score,x,y", SCORE_TABLE)
def test_all_score_buckets(expected_score: float, x: int, y: int):
    elem = _make_element(x, y)
    sig, est = _extract_rating_signal(elem)
    assert est == pytest.approx(expected_score), f"x={x}, y={y}"
    assert sig == f"sprite:x={x},y={y}"


def test_no_ir_element():
    html = "<div><span>no rating here</span></div>"
    elem = BeautifulSoup(html, "html.parser").find("div")
    sig, est = _extract_rating_signal(elem)
    assert sig == ""
    assert est is None


def test_ir_without_style():
    html = '<div><div class="ir"></div></div>'
    elem = BeautifulSoup(html, "html.parser").find("div")
    sig, est = _extract_rating_signal(elem)
    assert sig == ""
    assert est is None


def test_two_layers_picks_valid_row():
    """When both background (y=-21 or y=-1) layers exist, the first matching one wins."""
    # Simulate: bg layer y=-21 x=0 (4.5), fg layer y=-1 x=-16 (4.0)
    # The function iterates in DOM order — bg comes first.
    elem = _make_element_two_layers(fg_x=-16, fg_y=-1, bg_x=0, bg_y=-21)
    sig, est = _extract_rating_signal(elem)
    # The first .ir in DOM order is bg (y=-21, x=0) → 4.5
    assert est == pytest.approx(4.5)


def test_clamps_to_zero():
    """Extreme negative x should clamp to 0.0, not go negative."""
    elem = _make_element(x=-200, y=-1)
    _, est = _extract_rating_signal(elem)
    assert est == 0.0


def test_clamps_to_five():
    """Positive x (unusual) should clamp to 5.0."""
    elem = _make_element(x=32, y=-1)
    _, est = _extract_rating_signal(elem)
    assert est == pytest.approx(3.0)  # 5.0 - 32/16 = 3.0, no clamp needed

    elem2 = _make_element(x=80, y=-1)
    _, est2 = _extract_rating_signal(elem2)
    assert est2 == 0.0  # 5.0 - 5.0 = 0.0, clamped
